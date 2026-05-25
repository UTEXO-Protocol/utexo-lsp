package lspapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const lightningAddressTestPeerPubkey = "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newLightningAddressTestAPI(t *testing.T, domainURL, shortDescription string, lspClient *NodeClient) (*API, LightningAddressAccount) {
	t.Helper()

	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})

	api := &API{
		cfg: Config{
			LightningAddressDomainURL:           domainURL,
			LightningAddressShortDescription:    shortDescription,
			LightningAddressMinSendableMsat:     1_000,
			LightningAddressMaxSendableMsat:     4_000_000,
			APayInboundInvoiceExpiry:            defaultAPayInboundInvoiceExpiry,
			APayOutboundInvoiceExpiry:           defaultAPayOutboundInvoiceExpiry,
			APayInboundMinFinalCltvExpiryDelta:  defaultAPayInboundMinFinalCltvExpiryDelta,
			APayOutboundMinFinalCltvExpiryDelta: defaultAPayOutboundMinFinalCltvExpiryDelta,
		},
		db:        store,
		lspClient: lspClient,
	}

	account, err := api.ensureLightningAddressAccount(context.Background(), lightningAddressTestPeerPubkey)
	require.NoError(t, err)
	t.Logf("minted lightning address username: %s", account.Username)

	return api, account
}

func seedAsyncOrderHashes(t *testing.T, api *API, peerPubkey string, start, count int64) {
	t.Helper()

	hashes := make([]AsyncOrderNewHashInput, 0, count)
	for offset := int64(0); offset < count; offset++ {
		hashIndex := start + offset
		hashes = append(hashes, AsyncOrderNewHashInput{
			HashIndex:   strconv.FormatInt(hashIndex, 10),
			PaymentHash: fmt.Sprintf("%064x", hashIndex),
		})
	}

	_, rpcErr, err := api.db.ApplyAsyncOrderNew(context.Background(), AsyncOrderNewRequest{
		PeerPubkey:      peerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes:          hashes,
	})
	require.NoError(t, err)
	require.Nil(t, rpcErr)
}

func TestLightningAddressDiscovery(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username, nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp LightningAddressDiscoveryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	require.Equal(t, "https://example.com/pay/callback/"+url.PathEscape(account.Username), resp.Callback)
	require.Equal(t, "payRequest", resp.Tag)
	require.EqualValues(t, 1_000, resp.MinSendable)
	require.EqualValues(t, 4_000_000, resp.MaxSendable)

	wantMetadata := `[["text/identifier","` + account.Username + `@example.com"],["text/plain","Payment to txalkan"]]`
	require.Equal(t, wantMetadata, resp.Metadata)
}

func TestLightningAddressDiscoveryRejectsDomainPath(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com/app", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username, nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "path is not allowed")
}

func TestLightningAddressDiscoveryDefaultsShortDescriptionToIdentifier(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+strings.ToUpper(account.Username), nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp LightningAddressDiscoveryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	wantMetadata := `[["text/identifier","` + account.Username + `@example.com"],["text/plain","` + account.Username + `@example.com"]]`
	require.Equal(t, wantMetadata, resp.Metadata)
}

func TestLightningAddressCallbackIncludesDescriptionHash(t *testing.T) {
	var received map[string]any
	const assetID = "rgb:EIkAVQvq-WbAb5JG-CYxbUER-oqDNwne-ZNxBDID-p0cpf9U"

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", newInvoiceStubClient(t, &received, http.StatusOK, map[string]string{"invoice": "lnbc1testinvoice"}))
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000&asset_id="+url.QueryEscape(assetID)+"&asset_amount=10", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp LightningAddressCallbackResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "lnbc1testinvoice", resp.PR)
	t.Logf("minted lightning address invoice: %s", resp.PR)
	require.Empty(t, resp.Routes)
	_, ok := received["description_hash"]
	require.True(t, ok)
	_, ok = received["payment_hash"]
	require.True(t, ok)
	require.Equal(t, assetID, received["asset_id"])
	require.Equal(t, float64(10), received["asset_amount"])
	require.Equal(t, float64(defaultAPayInboundMinFinalCltvExpiryDelta), received["min_final_cltv_expiry_delta"])
}

func TestLightningAddressCallbackPersistsRotatingInvoiceSlots(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", newInvoiceStubClient(t, nil, http.StatusOK, map[string]string{"invoice": "lnbc1testinvoice"}))
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 2)
	const assetID = "rgb:EIkAVQvq-WbAb5JG-CYxbUER-oqDNwne-ZNxBDID-p0cpf9U"
	const assetAmount = 10
	store := api.db.(*SQLStore)
	formatNullInt64 := func(v sql.NullInt64) string {
		if !v.Valid {
			return "NULL"
		}
		return fmt.Sprintf("%d", v.Int64)
	}
	formatNullString := func(v sql.NullString) string {
		if !v.Valid {
			return "NULL"
		}
		return v.String
	}
	logOrderSnapshot := func(label string) int64 {
		var orderID int64
		var peerPubkey string
		var orderStatus string
		var orderCurrentInvoiceSlot sql.NullInt64
		var orderCurrentHashIndex sql.NullInt64
		var orderCurrentPaymentHash sql.NullString
		err := store.db.QueryRowContext(context.Background(), `
			SELECT order_id, peer_pubkey, status, current_invoice_slot, current_hash_index, current_payment_hash
			FROM async_orders
			WHERE peer_pubkey = ?
			`, strings.ToLower(lightningAddressTestPeerPubkey)).Scan(&orderID, &peerPubkey, &orderStatus, &orderCurrentInvoiceSlot, &orderCurrentHashIndex, &orderCurrentPaymentHash)
		require.NoError(t, err, "%s lookup async order", label)
		t.Logf("%s async order: order_id=%d peer_pubkey=%s status=%s current_invoice_slot=%s current_hash_index=%s current_payment_hash=%s", label, orderID, peerPubkey, orderStatus, formatNullInt64(orderCurrentInvoiceSlot), formatNullInt64(orderCurrentHashIndex), formatNullString(orderCurrentPaymentHash))
		return orderID
	}

	req1 := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000&asset_id="+url.QueryEscape(assetID)+"&asset_amount=10", nil)
	rr1 := httptest.NewRecorder()
	api.routes().ServeHTTP(rr1, req1)
	require.Equal(t, http.StatusOK, rr1.Code, rr1.Body.String())
	logOrderSnapshot("after callback 1")

	req2 := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000&asset_id="+url.QueryEscape(assetID)+"&asset_amount=10", nil)
	rr2 := httptest.NewRecorder()
	api.routes().ServeHTTP(rr2, req2)
	require.Equal(t, http.StatusOK, rr2.Code, rr2.Body.String())

	orderID := logOrderSnapshot("after callback 2")

	var count int64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM async_rotating_invoices WHERE order_id = ?`, orderID).Scan(&count))
	require.EqualValues(t, 2, count)

	rows, err := store.db.QueryContext(context.Background(), `
		SELECT id, invoice_slot, hash_index, payment_hash, asset_id, asset_amount, invoice_string, amount_msat, expires_at, status
		FROM async_rotating_invoices
		WHERE order_id = ?
		ORDER BY invoice_slot ASC
	`, orderID)
	require.NoError(t, err)
	defer rows.Close()

	var slots []int64
	var hashes []string
	for rows.Next() {
		var id int64
		var slot int64
		var hashIndex int64
		var paymentHash string
		var rowAssetID sql.NullString
		var rowAssetAmount sql.NullInt64
		var invoiceString sql.NullString
		var amountMsat int64
		var expiresAt time.Time
		var status AsyncInvoiceStatus
		require.NoError(t, rows.Scan(&id, &slot, &hashIndex, &paymentHash, &rowAssetID, &rowAssetAmount, &invoiceString, &amountMsat, &expiresAt, &status))
		t.Logf("async invoice: id=%d invoice_slot=%d hash_index=%d payment_hash=%s invoice_string=%s amount_msat=%d expires_at=%s status=%s", id, slot, hashIndex, paymentHash, formatNullString(invoiceString), amountMsat, expiresAt.Format(time.RFC3339Nano), status)
		require.Equal(t, asyncInvoiceStatusActive, status)
		require.True(t, rowAssetID.Valid)
		require.Equal(t, assetID, rowAssetID.String)
		require.True(t, rowAssetAmount.Valid)
		require.EqualValues(t, assetAmount, rowAssetAmount.Int64)
		require.Positive(t, hashIndex)
		slots = append(slots, slot)
		hashes = append(hashes, paymentHash)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{1, 2}, slots)
	require.NotEmpty(t, hashes[0])
	require.NotEmpty(t, hashes[1])
	require.NotEqual(t, hashes[0], hashes[1])

	var currentSlot int64
	var currentHashIndex int64
	var currentPaymentHash string
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT current_invoice_slot, current_hash_index, current_payment_hash FROM async_orders WHERE order_id = ?`, orderID).Scan(&currentSlot, &currentHashIndex, &currentPaymentHash))
	require.EqualValues(t, 2, currentSlot)
	require.Positive(t, currentHashIndex)
	require.Equal(t, hashes[1], currentPaymentHash)
	var orderStatus AsyncOrderStatus
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT status FROM async_orders WHERE order_id = ?`, orderID).Scan(&orderStatus))
	require.Equal(t, asyncOrderStatusExhausted, orderStatus)
	t.Logf("current async order state: current_invoice_slot=%d current_hash_index=%d current_payment_hash=%s", currentSlot, currentHashIndex, currentPaymentHash)
}

func TestLightningAddressCallbackFailsIfDescriptionHashRejected(t *testing.T) {
	var requestCount atomic.Int32

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", newInvoiceStubClient(t, nil, http.StatusBadRequest, map[string]string{"error": "description_hash unsupported"}, &requestCount))
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.EqualValues(t, 1, requestCount.Load())
	require.Contains(t, rr.Body.String(), "error constructing invoice")
}

func TestLightningAddressCallbackFailsWithoutUploadedHashes(t *testing.T) {
	var requestCount atomic.Int32

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", newInvoiceStubClient(t, nil, http.StatusOK, map[string]string{"invoice": "lnbc1testinvoice"}, &requestCount))
	api.cfg.LNInvoicePath = "/lninvoice"

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.Zero(t, requestCount.Load())
	require.Contains(t, rr.Body.String(), "async hash pool is empty")
}

func TestLightningAddressCallbackExhaustsSingleHashPool(t *testing.T) {
	var requestCount atomic.Int32

	invoicesStubClient := newInvoiceStubClient(t, nil, http.StatusOK, map[string]string{"invoice": "lnbc1singlehashinvoice"}, &requestCount)

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", invoicesStubClient)
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)
	store := api.db.(*SQLStore)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000", nil)
	rr1 := httptest.NewRecorder()
	api.routes().ServeHTTP(rr1, req)
	require.Equal(t, http.StatusOK, rr1.Code, rr1.Body.String())

	// trying to retrieve an invoice with only one payment hash in the pool, expecting to receive an error.
	rr2 := httptest.NewRecorder()
	api.routes().ServeHTTP(rr2, req)
	require.Equal(t, http.StatusInternalServerError, rr2.Code, rr2.Body.String())
	require.EqualValues(t, 1, requestCount.Load())
	require.Contains(t, rr2.Body.String(), "async hash pool is empty")

	var invoiceCount int64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM async_rotating_invoices`).Scan(&invoiceCount))
	require.EqualValues(t, 1, invoiceCount)

	var (
		invoiceSlot   int64
		hashIndex     int64
		paymentHash   string
		invoiceString sql.NullString
		invoiceStatus AsyncInvoiceStatus
	)
	require.NoError(t, store.db.QueryRowContext(context.Background(), `
		SELECT invoice_slot, hash_index, payment_hash, invoice_string, status
		FROM async_rotating_invoices
		LIMIT 1
	`).Scan(&invoiceSlot, &hashIndex, &paymentHash, &invoiceString, &invoiceStatus))
	require.EqualValues(t, 1, invoiceSlot)
	require.EqualValues(t, 1, hashIndex)
	require.Equal(t, fmt.Sprintf("%064x", 1), paymentHash)
	require.True(t, invoiceString.Valid)
	require.Equal(t, "lnbc1singlehashinvoice", invoiceString.String)
	require.Equal(t, asyncInvoiceStatusActive, invoiceStatus)

	var (
		poolCount  int64
		poolStatus AsyncPoolStatus
	)
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*), MIN(status) FROM async_hash_pool`).Scan(&poolCount, &poolStatus))
	require.EqualValues(t, 1, poolCount)
	require.Equal(t, asyncPoolStatusConsumed, poolStatus)

	var (
		orderStatus        AsyncOrderStatus
		currentInvoiceSlot sql.NullInt64
		currentHashIndex   sql.NullInt64
		currentPaymentHash sql.NullString
	)
	require.NoError(t, store.db.QueryRowContext(context.Background(), `
		SELECT status, current_invoice_slot, current_hash_index, current_payment_hash
		FROM async_orders
		WHERE peer_pubkey = ?
	`, strings.ToLower(lightningAddressTestPeerPubkey)).Scan(&orderStatus, &currentInvoiceSlot, &currentHashIndex, &currentPaymentHash))
	require.Equal(t, asyncOrderStatusExhausted, orderStatus)
	require.True(t, currentInvoiceSlot.Valid)
	require.EqualValues(t, 1, currentInvoiceSlot.Int64)
	require.True(t, currentHashIndex.Valid)
	require.EqualValues(t, 1, currentHashIndex.Int64)
	require.True(t, currentPaymentHash.Valid)
	require.Equal(t, paymentHash, currentPaymentHash.String)
}

func TestLightningAddressCallbackLookupNormalizesUsernameCase(t *testing.T) {
	var received map[string]any

	invoicesStubClient := newInvoiceStubClient(t, &received, http.StatusOK, map[string]string{"invoice": "lnbc1testinvoice"})

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", invoicesStubClient)
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(strings.ToUpper(account.Username))+"?amount=3000000", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	_, ok := received["payment_hash"]
	require.True(t, ok, "expected invoice request to be issued, got %#v", received)
}

func TestLightningAddressCallbackRejectsIncompleteAssetParams(t *testing.T) {
	var requestCount atomic.Int32

	invoicesStubClient := newInvoiceStubClient(t, nil, http.StatusOK, map[string]string{"invoice": "lnbc1testinvoice"}, &requestCount)

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", invoicesStubClient)
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000&asset_id=rgb:test", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Zero(t, requestCount.Load())
	require.Contains(t, rr.Body.String(), "asset_id and asset_amount must be provided together")
}

func TestLightningAddressCallbackReleasesReservationAfterInvoiceError(t *testing.T) {
	var requestCount atomic.Int32

	// NOTE: stub client will throw an error.
	invoicesStubClient := newInvoiceStubClient(t, nil, http.StatusInternalServerError, map[string]string{"error": "error"}, &requestCount)

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", invoicesStubClient)
	api.cfg.LNInvoicePath = "/lninvoice"
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)
	store := api.db.(*SQLStore)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.EqualValues(t, 1, requestCount.Load())

	var invoiceCount int64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM async_rotating_invoices`).Scan(&invoiceCount))
	require.EqualValues(t, 1, invoiceCount)

	var status AsyncInvoiceStatus
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT status FROM async_rotating_invoices LIMIT 1`).Scan(&status))
	require.Equal(t, asyncInvoiceStatusFailed, status)

	var availableCount int64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM async_hash_pool WHERE status = ?`, asyncPoolStatusAvailable).Scan(&availableCount))
	require.EqualValues(t, 1, availableCount)

	var orderStatus AsyncOrderStatus
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT status FROM async_orders WHERE peer_pubkey = ?`, strings.ToLower(lightningAddressTestPeerPubkey)).Scan(&orderStatus))
	require.Equal(t, asyncOrderStatusActive, orderStatus)
}

type invoiceStubRoundTripper struct {
	t            *testing.T
	received     *map[string]any
	statusCode   int
	responseBody any
	requestCount *atomic.Int32
}

func (rt *invoiceStubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.requestCount != nil {
		rt.requestCount.Add(1)
	}
	require.Equal(rt.t, http.MethodPost, req.Method)
	require.Equal(rt.t, "/lninvoice", req.URL.Path)
	body, err := io.ReadAll(req.Body)
	require.NoError(rt.t, err)
	if rt.received != nil {
		require.NoError(rt.t, json.Unmarshal(body, rt.received))
		_, ok := (*rt.received)["description_hash"]
		require.True(rt.t, ok, "expected description_hash in request body: %s", string(body))
		_, ok = (*rt.received)["payment_hash"]
		require.True(rt.t, ok, "expected payment_hash in request body: %s", string(body))
		require.Equal(rt.t, float64(defaultAPayInboundMinFinalCltvExpiryDelta), (*rt.received)["min_final_cltv_expiry_delta"], "unexpected min_final_cltv_expiry_delta in request body: %s", string(body))
	}

	buf, err := json.Marshal(rt.responseBody)
	require.NoError(rt.t, err)

	return &http.Response{
		StatusCode: rt.statusCode,
		Status:     http.StatusText(rt.statusCode),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(buf))),
		Request:    req,
	}, nil
}

func newInvoiceStubClient(t *testing.T, received *map[string]any, statusCode int, responseBody any, requestCount ...*atomic.Int32) *NodeClient {
	t.Helper()

	var counter *atomic.Int32
	if len(requestCount) > 0 {
		counter = requestCount[0]
	}

	return &NodeClient{
		baseURL: "http://invoice-stub",
		http: &http.Client{
			Transport: &invoiceStubRoundTripper{
				t:            t,
				received:     received,
				statusCode:   statusCode,
				responseBody: responseBody,
				requestCount: counter,
			},
		},
	}
}
