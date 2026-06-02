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
)

const lightningAddressTestPeerPubkey = "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newLightningAddressTestAPI(t *testing.T, domainURL, shortDescription string, lspClient *NodeClient) (*API, LightningAddressAccount) {
	t.Helper()

	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
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
	if err != nil {
		t.Fatalf("ensure lightning address account: %v", err)
	}
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
	if err != nil {
		t.Fatalf("seed async order hashes: %v", err)
	}
	if rpcErr != nil {
		t.Fatalf("seed async order hashes rpc error: %v", rpcErr)
	}
}

func TestLightningAddressDiscovery(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username, nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp LightningAddressDiscoveryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Callback != "https://example.com/pay/callback/"+url.PathEscape(account.Username) {
		t.Fatalf("unexpected callback: %s", resp.Callback)
	}
	if resp.Tag != "payRequest" {
		t.Fatalf("unexpected tag: %s", resp.Tag)
	}
	if resp.MinSendable != 1_000 || resp.MaxSendable != 4_000_000 {
		t.Fatalf("unexpected sendable range: %+v", resp)
	}

	wantMetadata := `[["text/identifier","` + account.Username + `@example.com"],["text/plain","Payment to txalkan"]]`
	if resp.Metadata != wantMetadata {
		t.Fatalf("unexpected metadata: %s", resp.Metadata)
	}
}

func TestLightningAddressDiscoveryRejectsDomainPath(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com/app", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username, nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "path is not allowed") {
		t.Fatalf("unexpected response body: %s", rr.Body.String())
	}
}

func TestLightningAddressByPubkeyCanonicalizesValidPubkey(t *testing.T) {
	api, _ := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)
	account, err := api.ensureLightningAddressAccount(context.Background(), lightningAddressValidTestPeerPubkey)
	if err != nil {
		t.Fatalf("ensure lightning address account: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/lightning_address/by_pubkey/"+strings.ToUpper(lightningAddressValidTestPeerPubkey), nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp LightningAddressByPubkeyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Username != account.Username || resp.Domain != "example.com" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestLightningAddressByPubkeyRejectsInvalidCurvePoint(t *testing.T) {
	api, _ := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/lightning_address/by_pubkey/"+"02"+strings.Repeat("ff", 32), nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
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

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp LightningAddressCallbackResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PR != "lnbc1testinvoice" {
		t.Fatalf("unexpected invoice: %s", resp.PR)
	}
	t.Logf("minted lightning address invoice: %s", resp.PR)
	if len(resp.Routes) != 0 {
		t.Fatalf("expected empty routes, got %#v", resp.Routes)
	}
	if _, ok := received["description_hash"]; !ok {
		t.Fatalf("expected description_hash in request body: %#v", received)
	}
	if _, ok := received["payment_hash"]; !ok {
		t.Fatalf("expected payment_hash in request body: %#v", received)
	}
	if got, ok := received["asset_id"]; !ok || got != assetID {
		t.Fatalf("expected asset_id in request body: %#v", received)
	}
	if got, ok := received["asset_amount"]; !ok || got != float64(10) {
		t.Fatalf("expected asset_amount in request body: %#v", received)
	}
	if got := received["min_final_cltv_expiry_delta"]; got != float64(defaultAPayInboundMinFinalCltvExpiryDelta) {
		t.Fatalf("unexpected min_final_cltv_expiry_delta: got %#v", got)
	}
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
		if err := store.db.QueryRowContext(context.Background(), `
			SELECT order_id, peer_pubkey, status, current_invoice_slot, current_hash_index, current_payment_hash
			FROM async_orders
			WHERE peer_pubkey = ?
		`, strings.ToLower(lightningAddressTestPeerPubkey)).Scan(&orderID, &peerPubkey, &orderStatus, &orderCurrentInvoiceSlot, &orderCurrentHashIndex, &orderCurrentPaymentHash); err != nil {
			t.Fatalf("%s lookup async order: %v", label, err)
		}
		t.Logf("%s async order: order_id=%d peer_pubkey=%s status=%s current_invoice_slot=%s current_hash_index=%s current_payment_hash=%s", label, orderID, peerPubkey, orderStatus, formatNullInt64(orderCurrentInvoiceSlot), formatNullInt64(orderCurrentHashIndex), formatNullString(orderCurrentPaymentHash))
		return orderID
	}

	req1 := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000&asset_id="+url.QueryEscape(assetID)+"&asset_amount=10", nil)
	rr1 := httptest.NewRecorder()
	api.routes().ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first callback expected 200, got %d: %s", rr1.Code, rr1.Body.String())
	}
	logOrderSnapshot("after callback 1")

	req2 := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000&asset_id="+url.QueryEscape(assetID)+"&asset_amount=10", nil)
	rr2 := httptest.NewRecorder()
	api.routes().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second callback expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}

	orderID := logOrderSnapshot("after callback 2")

	var count int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM async_rotating_invoices WHERE order_id = ?`, orderID).Scan(&count); err != nil {
		t.Fatalf("count async invoices: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 async invoices, got %d", count)
	}

	rows, err := store.db.QueryContext(context.Background(), `
		SELECT id, invoice_slot, hash_index, payment_hash, asset_id, asset_amount, invoice_string, amount_msat, expires_at, status
		FROM async_rotating_invoices
		WHERE order_id = ?
		ORDER BY invoice_slot ASC
	`, orderID)
	if err != nil {
		t.Fatalf("list async invoices: %v", err)
	}
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
		if err := rows.Scan(&id, &slot, &hashIndex, &paymentHash, &rowAssetID, &rowAssetAmount, &invoiceString, &amountMsat, &expiresAt, &status); err != nil {
			t.Fatalf("scan async invoice: %v", err)
		}
		t.Logf("async invoice: id=%d invoice_slot=%d hash_index=%d payment_hash=%s invoice_string=%s amount_msat=%d expires_at=%s status=%s", id, slot, hashIndex, paymentHash, formatNullString(invoiceString), amountMsat, expiresAt.Format(time.RFC3339Nano), status)
		if status != asyncInvoiceStatusActive {
			t.Fatalf("expected active invoice status, got %s", status)
		}
		if !rowAssetID.Valid || rowAssetID.String != assetID {
			t.Fatalf("expected asset_id to be persisted, got %#v", rowAssetID)
		}
		if !rowAssetAmount.Valid || rowAssetAmount.Int64 != assetAmount {
			t.Fatalf("expected asset_amount to be persisted, got %#v", rowAssetAmount)
		}
		if hashIndex <= 0 {
			t.Fatalf("expected positive hash index, got %d", hashIndex)
		}
		slots = append(slots, slot)
		hashes = append(hashes, paymentHash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate async invoices: %v", err)
	}
	if len(slots) != 2 || slots[0] != 1 || slots[1] != 2 {
		t.Fatalf("unexpected invoice slots: %#v", slots)
	}
	if hashes[0] == "" || hashes[1] == "" {
		t.Fatalf("expected populated payment hashes: %#v", hashes)
	}
	if hashes[0] == hashes[1] {
		t.Fatalf("expected distinct payment hashes, got %#v", hashes)
	}

	var currentSlot int64
	var currentHashIndex int64
	var currentPaymentHash string
	if err := store.db.QueryRowContext(context.Background(), `SELECT current_invoice_slot, current_hash_index, current_payment_hash FROM async_orders WHERE order_id = ?`, orderID).Scan(&currentSlot, &currentHashIndex, &currentPaymentHash); err != nil {
		t.Fatalf("lookup current order state: %v", err)
	}
	if currentSlot != 2 {
		t.Fatalf("expected current slot 2, got %d", currentSlot)
	}
	if currentHashIndex <= 0 {
		t.Fatalf("expected current hash index to be set, got %d", currentHashIndex)
	}
	if currentPaymentHash != hashes[1] {
		t.Fatalf("expected current payment hash to match latest invoice, got %s want %s", currentPaymentHash, hashes[1])
	}
	var orderStatus AsyncOrderStatus
	if err := store.db.QueryRowContext(context.Background(), `SELECT status FROM async_orders WHERE order_id = ?`, orderID).Scan(&orderStatus); err != nil {
		t.Fatalf("lookup order status: %v", err)
	}
	if orderStatus != asyncOrderStatusExhausted {
		t.Fatalf("expected exhausted order status, got %s", orderStatus)
	}
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

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 request and no fallback, got %d", requestCount.Load())
	}
	if !strings.Contains(rr.Body.String(), "error constructing invoice") {
		t.Fatalf("unexpected response body: %s", rr.Body.String())
	}
}

func TestLightningAddressCallbackFailsWithoutUploadedHashes(t *testing.T) {
	var requestCount atomic.Int32

	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", newInvoiceStubClient(t, nil, http.StatusOK, map[string]string{"invoice": "lnbc1testinvoice"}, &requestCount))
	api.cfg.LNInvoicePath = "/lninvoice"

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+"?amount=3000000", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
	if requestCount.Load() != 0 {
		t.Fatalf("expected no invoice request when hash pool is empty, got %d", requestCount.Load())
	}
	if !strings.Contains(rr.Body.String(), "async hash pool is empty") {
		t.Fatalf("unexpected response body: %s", rr.Body.String())
	}
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
	if req.Method != http.MethodPost {
		rt.t.Errorf("unexpected method: %s", req.Method)
	}
	if req.URL.Path != "/lninvoice" {
		rt.t.Errorf("unexpected path: %s", req.URL.Path)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		rt.t.Errorf("read body: %v", err)
	}
	if rt.received != nil {
		if err := json.Unmarshal(body, rt.received); err != nil {
			rt.t.Errorf("unmarshal body: %v", err)
		}
		if _, ok := (*rt.received)["description_hash"]; !ok {
			rt.t.Errorf("expected description_hash in request body: %s", string(body))
		}
		if _, ok := (*rt.received)["payment_hash"]; !ok {
			rt.t.Errorf("expected payment_hash in request body: %s", string(body))
		}
		if got := (*rt.received)["min_final_cltv_expiry_delta"]; got != float64(defaultAPayInboundMinFinalCltvExpiryDelta) {
			rt.t.Errorf("unexpected min_final_cltv_expiry_delta in request body: %s", string(body))
		}
	}

	buf, err := json.Marshal(rt.responseBody)
	if err != nil {
		rt.t.Errorf("marshal response: %v", err)
	}

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
