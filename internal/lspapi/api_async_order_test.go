package lspapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type asyncOrderJSONRPCResponseTestEnvelope struct {
	JSONRPC string                `json:"jsonrpc"`
	ID      any                   `json:"id"`
	Result  AsyncOrderNewResponse `json:"result,omitempty"`
	Error   *AsyncOrderError      `json:"error,omitempty"`
}

func decodeAsyncOrderJSONRPCResponse(t *testing.T, body []byte) asyncOrderJSONRPCResponseTestEnvelope {
	t.Helper()

	var envelope asyncOrderJSONRPCResponseTestEnvelope
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope
}

func newAsyncOrderTestStore(t *testing.T) *SQLStore {
	t.Helper()

	return newPostgresTestStore(t)
}

func applyAsyncOrderNewForTest(t *testing.T, store *SQLStore, peerPubkey string, hashes []AsyncOrderNewHashInput) (AsyncOrderNewResponse, *AsyncOrderError) {
	t.Helper()

	resp, rpcErr, err := store.ApplyAsyncOrderNew(context.Background(), AsyncOrderNewRequest{
		PeerPubkey:      peerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes:          hashes,
	})
	require.NoError(t, err)
	return resp, rpcErr
}

func reserveAndFinalizeAsyncInvoiceForTest(t *testing.T, store *SQLStore, peerPubkey, username, paymentHash string, amountMsat uint64) AsyncRotatingInvoice {
	t.Helper()

	ctx := context.Background()
	inserted, err := store.InsertLightningAddressAccount(ctx, LightningAddressAccount{
		PeerPubkey: peerPubkey,
		Username:   username,
	})
	require.NoError(t, err)
	require.True(t, inserted)

	_, rpcErr, err := store.ApplyAsyncOrderNew(ctx, AsyncOrderNewRequest{
		PeerPubkey:      peerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes: []AsyncOrderNewHashInput{
			{
				HashIndex:   "1",
				PaymentHash: paymentHash,
			},
		},
	})
	require.NoError(t, err)
	require.Nil(t, rpcErr)

	reserved, err := store.ReserveLightningAddressInvoiceSlot(ctx, LightningAddressAccount{
		PeerPubkey: peerPubkey,
		Username:   username,
	}, amountMsat, nil, nil, time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.FinalizeLightningAddressInvoiceSlot(ctx, reserved.ID, "lnbc1claimflowtest"))

	return reserved
}

func paymentHashAndPreimageForTest(seed string) (string, string) {
	preimage := strings.Repeat(seed, 64)
	preimageBytes, _ := hex.DecodeString(preimage)
	sum := sha256.Sum256(preimageBytes)
	return hex.EncodeToString(sum[:]), preimage
}

func loadAsyncOutboxCountForAction(t *testing.T, store *SQLStore, paymentHash string, action AsyncOutboxAction) int64 {
	t.Helper()

	var count int64
	err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*)
		FROM async_rotating_invoice_outbox
		WHERE payment_hash = $1 AND action = $2
	`, paymentHash, action).Scan(&count)
	require.NoError(t, err)
	return count
}

func TestAsyncOrderNewRequiresControlToken(t *testing.T) {
	api := &API{
		cfg: Config{
			HTTPTimeout: time.Second,
			// Intentionally leave APayBearerToken empty to verify fail-closed behavior.
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/new", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderNew(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
}

func TestAsyncOrderNewRejectsEmptyPeerPubkey(t *testing.T) {
	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/new", strings.NewReader(`{"id":"request-1","protocol_version":1,"hashes":[{"hash_index":"1","payment_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderNew(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	resp := decodeAsyncOrderJSONRPCResponse(t, rr.Body.Bytes())
	require.Equal(t, asyncOrderJSONRPCVersion, resp.JSONRPC)
	require.Equal(t, "request-1", resp.ID)
	require.NotNil(t, resp.Error)
	require.EqualValues(t, asyncOrderJSONRPCInvalidRequest, resp.Error.Code)
	require.Equal(t, "invalid request", resp.Error.Message)
}

func TestAsyncOrderNewReturnsJsonRpcEnvelope(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
		db: store,
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/new", strings.NewReader(`{"id":"request-2","peer_pubkey":"02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","protocol_version":1,"hashes":[{"hash_index":"1","payment_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"hash_index":"2","payment_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderNew(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	resp := decodeAsyncOrderJSONRPCResponse(t, rr.Body.Bytes())
	require.Equal(t, asyncOrderJSONRPCVersion, resp.JSONRPC)
	require.Equal(t, "request-2", resp.ID)
	require.Nil(t, resp.Error)
	require.Equal(t, asyncOrderProtocolVersion, resp.Result.ProtocolVersion)
	require.Equal(t, "1", resp.Result.OrderID)
	require.Equal(t, asyncOrderStatusActive, resp.Result.Status)
	require.Equal(t, "2", resp.Result.AcceptedThroughIndex)
	require.Equal(t, "3", resp.Result.NextIndexExpected)
	require.Equal(t, "2", resp.Result.UnusedHashes)
	require.Equal(t, "200", resp.Result.RefillBatchSize)
}

func TestInboundClaimableRequiresAuthToken(t *testing.T) {
	api := &API{
		cfg: Config{
			HTTPTimeout: time.Second,
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/claimable", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	api.handleInternalInboundInvoiceClaimable(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
}

func TestInboundClaimableRejectsMissingDeadline(t *testing.T) {
	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
	}

	body := strings.NewReader(`{"payment_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","amount_msat":3000000}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/claimable", body)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalInboundInvoiceClaimable(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "claim_deadline_height is required")
}

func TestPaymentSentRejectsMismatchedPreimage(t *testing.T) {
	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
	}

	body := strings.NewReader(`{"payment_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","payment_preimage":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/payment_sent", body)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderPaymentSent(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "payment_preimage does not match payment_hash")
}

func TestPaymentSentReturns503BeforeOutboundPaid(t *testing.T) {
	store := newAsyncOrderTestStore(t)
	paymentHash, paymentPreimage := paymentHashAndPreimageForTest("1")
	reserved := reserveAndFinalizeAsyncInvoiceForTest(t, store, "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "alice", paymentHash, 3_000_000)

	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(context.Background(), reserved.PaymentHash, 3_000_000, nil)
	require.NoError(t, err)
	require.True(t, transitioned)

	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
		db: store,
	}

	body := strings.NewReader(`{"payment_hash":"` + paymentHash + `","payment_preimage":"` + paymentPreimage + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/payment_sent", body)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderPaymentSent(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "payment_sent received before outbound payment was confirmed locally")
}

func TestPaymentSentIsIdempotentAfterOutboundClaimed(t *testing.T) {
	store := newAsyncOrderTestStore(t)
	paymentHash, paymentPreimage := paymentHashAndPreimageForTest("2")
	reserved := reserveAndFinalizeAsyncInvoiceForTest(t, store, "02bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "bob", paymentHash, 3_000_000)

	ctx := context.Background()
	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(ctx, reserved.PaymentHash, 3_000_000, nil)
	require.NoError(t, err)
	require.True(t, transitioned)

	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundRequested(ctx, reserved.PaymentHash)
	require.NoError(t, err)
	require.True(t, transitioned)

	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundPending(ctx, reserved.PaymentHash, "lnbc1outbound")
	require.NoError(t, err)
	require.True(t, transitioned)

	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundPaid(ctx, reserved.PaymentHash)
	require.NoError(t, err)
	require.True(t, transitioned)

	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundClaimed(ctx, reserved.PaymentHash, paymentPreimage)
	require.NoError(t, err)
	require.True(t, transitioned)

	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
		db: store,
	}

	body := strings.NewReader(`{"payment_hash":"` + paymentHash + `","payment_preimage":"` + paymentPreimage + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/payment_sent", body)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderPaymentSent(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(ctx, paymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusOutboundClaimed, current.Status)
	require.NotNil(t, current.PaymentPreimage)
	require.Equal(t, paymentPreimage, *current.PaymentPreimage)
}

func TestClaimablePersists(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	const peerPubkey = "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	ctx := context.Background()
	inserted, err := store.InsertLightningAddressAccount(ctx, LightningAddressAccount{
		PeerPubkey: peerPubkey,
		Username:   "alice",
	})
	require.NoError(t, err)
	require.True(t, inserted)

	_, rpcErr, err := store.ApplyAsyncOrderNew(ctx, AsyncOrderNewRequest{
		PeerPubkey:      peerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes: []AsyncOrderNewHashInput{
			{
				HashIndex:   "1",
				PaymentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	})
	require.NoError(t, err)
	require.Nil(t, rpcErr)

	_, err = store.MarkAsyncRotatingInvoiceClaimable(ctx, strings.Repeat("a", 64), 0, nil)
	require.ErrorIs(t, err, errAsyncRotatingInvoiceInvalidAmountMsat)

	assetID := "rgb-asset-a"
	assetAmount := uint64(10)

	reserved, err := store.ReserveLightningAddressInvoiceSlot(ctx, LightningAddressAccount{
		PeerPubkey: peerPubkey,
		Username:   "alice",
	}, 3_000_000, &assetID, &assetAmount, time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.FinalizeLightningAddressInvoiceSlot(ctx, reserved.ID, "lnbc1claimabletest"))

	claimDeadlineHeight := uint32(400)
	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(ctx, reserved.PaymentHash, 3_000_000, &claimDeadlineHeight)
	require.NoError(t, err)
	require.True(t, transitioned)

	var claimableAt sql.NullString
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT claimable_at FROM async_rotating_invoices WHERE payment_hash = $1 LIMIT 1`, reserved.PaymentHash).Scan(&claimableAt))
	require.True(t, claimableAt.Valid)
	require.NotEmpty(t, strings.TrimSpace(claimableAt.String))

	var outboxCount int64
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM async_rotating_invoice_outbox WHERE payment_hash = $1 AND action = $2`, reserved.PaymentHash, asyncOutboxActionRequestOutboundInvoice).Scan(&outboxCount))
	require.EqualValues(t, 1, outboxCount)

	job, ok, err := store.ClaimAsyncRotatingInvoiceOutboxJob(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, reserved.PaymentHash, job.PaymentHash)
	require.Equal(t, asyncOutboxActionRequestOutboundInvoice, job.Action)

	var rowAssetID sql.NullString
	var rowAssetAmount sql.NullInt64
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT asset_id, asset_amount FROM async_rotating_invoices WHERE payment_hash = $1 LIMIT 1`, reserved.PaymentHash).Scan(&rowAssetID, &rowAssetAmount))
	require.True(t, rowAssetID.Valid)
	require.Equal(t, "rgb-asset-a", rowAssetID.String)
	require.True(t, rowAssetAmount.Valid)
	require.EqualValues(t, 10, rowAssetAmount.Int64)
}

func TestAsyncOrderHTTPStatusMap(t *testing.T) {
	tests := []struct {
		name string
		code int64
		want int
	}{
		{name: "duplicate index", code: asyncOrderErrorDuplicateIndexConflict, want: http.StatusConflict},
		{name: "duplicate hash", code: asyncOrderErrorDuplicateHashConflict, want: http.StatusConflict},
		{name: "internal error", code: asyncOrderJSONRPCInternalError, want: http.StatusInternalServerError},
		{name: "validation error", code: asyncOrderErrorInvalidHashBatch, want: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, asyncOrderHTTPStatusFromErrorCode(tc.code))
		})
	}
}

func TestAsyncOrderAcceptedThroughIndexSurvivesPoolDeletion(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	resp, rpcErr, err := store.ApplyAsyncOrderNew(context.Background(), AsyncOrderNewRequest{
		PeerPubkey:      lightningAddressTestPeerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes: []AsyncOrderNewHashInput{
			{HashIndex: "1", PaymentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{HashIndex: "2", PaymentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	})
	require.NoError(t, err)
	require.Nil(t, rpcErr)
	require.Equal(t, "2", resp.AcceptedThroughIndex)

	orderID, err := strconv.ParseInt(resp.OrderID, 10, 64)
	require.NoError(t, err)

	var acceptedThroughIndex sql.NullInt64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT accepted_through_index FROM async_orders WHERE order_id = $1`, orderID).Scan(&acceptedThroughIndex))
	require.True(t, acceptedThroughIndex.Valid)
	require.EqualValues(t, 2, acceptedThroughIndex.Int64)

	_, err = store.db.ExecContext(context.Background(), `DELETE FROM async_hash_pool WHERE order_id = $1`, orderID)
	require.NoError(t, err)

	tx, err := store.db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tx.Rollback()
	})

	snapshot, err := store.asyncOrderSnapshotTx(context.Background(), tx, orderID)
	require.NoError(t, err)
	require.Equal(t, "2", snapshot.AcceptedThroughIndex)
	require.Equal(t, "3", snapshot.NextIndexExpected)
	require.Equal(t, "0", snapshot.UnusedHashes)
	require.Equal(t, asyncOrderStatusExhausted, snapshot.Status)
}

func TestAsyncOrderNewAcceptsIdempotentReplay(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	hashes := []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a", 64)},
		{HashIndex: "2", PaymentHash: strings.Repeat("b", 64)},
	}

	initialOrder, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, hashes)
	require.Nil(t, rpcErr)

	replayed, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, hashes)
	require.Nil(t, rpcErr)
	require.Equal(t, initialOrder.OrderID, replayed.OrderID)
	require.Equal(t, "2", replayed.AcceptedThroughIndex)
	require.Equal(t, "3", replayed.NextIndexExpected)
	require.Equal(t, "2", replayed.UnusedHashes)
}

func TestAsyncOrderNewAcceptsStrictAppendFromNextIndex(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	_, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a", 64)},
		{HashIndex: "2", PaymentHash: strings.Repeat("b", 64)},
	})
	require.Nil(t, rpcErr)

	appended, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "3", PaymentHash: strings.Repeat("c", 64)},
		{HashIndex: "4", PaymentHash: strings.Repeat("d", 64)},
	})
	require.Nil(t, rpcErr)
	require.Equal(t, "4", appended.AcceptedThroughIndex)
	require.Equal(t, "5", appended.NextIndexExpected)
	require.Equal(t, "4", appended.UnusedHashes)
}

func TestAsyncOrderNewRejectsDuplicateIndexWithDifferentHash(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	_, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a", 64)},
	})
	require.Nil(t, rpcErr)

	_, rpcErr = applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("b", 64)},
	})
	require.NotNil(t, rpcErr)
	require.EqualValues(t, asyncOrderErrorDuplicateIndexConflict, rpcErr.Code)
}

func TestAsyncOrderNewRejectsDuplicateHashWithDifferentIndex(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	_, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a", 64)},
	})
	require.Nil(t, rpcErr)

	_, rpcErr = applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "2", PaymentHash: strings.Repeat("a", 64)},
	})
	require.NotNil(t, rpcErr)
	require.EqualValues(t, asyncOrderErrorDuplicateHashConflict, rpcErr.Code)
}

func TestAsyncOrderNewRejectsGapInAppendBatch(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	_, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a", 64)},
		{HashIndex: "2", PaymentHash: strings.Repeat("b", 64)},
	})
	require.Nil(t, rpcErr)

	_, rpcErr = applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "4", PaymentHash: strings.Repeat("d", 64)},
	})
	require.NotNil(t, rpcErr)
	require.EqualValues(t, asyncOrderErrorInvalidHashBatch, rpcErr.Code)
}

func TestAsyncOrderNewRejectsMixedReplayAndAppendBatch(t *testing.T) {
	store := newAsyncOrderTestStore(t)

	_, rpcErr := applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "1", PaymentHash: strings.Repeat("a", 64)},
		{HashIndex: "2", PaymentHash: strings.Repeat("b", 64)},
	})
	require.Nil(t, rpcErr)

	_, rpcErr = applyAsyncOrderNewForTest(t, store, lightningAddressTestPeerPubkey, []AsyncOrderNewHashInput{
		{HashIndex: "2", PaymentHash: strings.Repeat("b", 64)},
		{HashIndex: "3", PaymentHash: strings.Repeat("c", 64)},
	})
	require.NotNil(t, rpcErr)
	require.EqualValues(t, asyncOrderErrorInvalidHashBatch, rpcErr.Code)
}
