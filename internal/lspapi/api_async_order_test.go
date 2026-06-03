package lspapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return envelope
}

func TestInternalAsyncOrderNewRequiresControlToken(t *testing.T) {
	api := &API{
		cfg: Config{
			HTTPTimeout: time.Second,
			// Intentionally leave APayBearerToken empty to verify fail-closed behavior.
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/new", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderNew(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalAsyncOrderNewRejectsEmptyPeerPubkey(t *testing.T) {
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

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := decodeAsyncOrderJSONRPCResponse(t, rr.Body.Bytes())
	if resp.JSONRPC != asyncOrderJSONRPCVersion {
		t.Fatalf("expected jsonrpc %q, got %q", asyncOrderJSONRPCVersion, resp.JSONRPC)
	}
	if resp.ID != "request-1" {
		t.Fatalf("expected id request-1, got %#v", resp.ID)
	}
	if resp.Error == nil {
		t.Fatalf("expected jsonrpc error envelope, got %#v", resp)
	}
	if resp.Error.Code != asyncOrderJSONRPCInvalidRequest {
		t.Fatalf("expected invalid request code %d, got %d", asyncOrderJSONRPCInvalidRequest, resp.Error.Code)
	}
	if resp.Error.Message != "invalid request" {
		t.Fatalf("unexpected error message %q", resp.Error.Message)
	}
}

func TestInternalAsyncOrderNewReturnsJsonRpcEnvelope(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "async-order.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

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

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := decodeAsyncOrderJSONRPCResponse(t, rr.Body.Bytes())
	if resp.JSONRPC != asyncOrderJSONRPCVersion {
		t.Fatalf("expected jsonrpc %q, got %q", asyncOrderJSONRPCVersion, resp.JSONRPC)
	}
	if resp.ID != "request-2" {
		t.Fatalf("expected id request-2, got %#v", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("expected no jsonrpc error, got %#v", resp.Error)
	}
	if resp.Result.ProtocolVersion != asyncOrderProtocolVersion {
		t.Fatalf("expected protocol version %d, got %d", asyncOrderProtocolVersion, resp.Result.ProtocolVersion)
	}
	if resp.Result.OrderID != "1" {
		t.Fatalf("expected order id 1, got %q", resp.Result.OrderID)
	}
	if resp.Result.Status != "active" {
		t.Fatalf("expected active status, got %q", resp.Result.Status)
	}
	if resp.Result.AcceptedThroughIndex != 2 {
		t.Fatalf("expected accepted_through_index 2, got %d", resp.Result.AcceptedThroughIndex)
	}
	if resp.Result.NextIndexExpected != 3 {
		t.Fatalf("expected next_index_expected 3, got %d", resp.Result.NextIndexExpected)
	}
	if resp.Result.UnusedHashes != 2 {
		t.Fatalf("expected unused_hashes 2, got %d", resp.Result.UnusedHashes)
	}
	if resp.Result.RefillBatchSize != 200 {
		t.Fatalf("expected refill_batch_size 200, got %d", resp.Result.RefillBatchSize)
	}
}

func TestInternalAsyncOrderNewAcceptsNumericHashIndex(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "async-order-numeric-index.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	api := &API{
		cfg: Config{
			HTTPTimeout:     time.Second,
			APayBearerToken: "secret",
		},
		db: store,
	}

	body := strings.NewReader(`{"id":"request-numeric","peer_pubkey":"02bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","protocol_version":1,"hashes":[{"hash_index":1,"payment_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/new", body)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderNew(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := decodeAsyncOrderJSONRPCResponse(t, rr.Body.Bytes())
	if resp.Error != nil {
		t.Fatalf("expected no jsonrpc error, got %#v", resp.Error)
	}
	if resp.Result.AcceptedThroughIndex != 1 {
		t.Fatalf("expected accepted_through_index 1, got %d", resp.Result.AcceptedThroughIndex)
	}
}

func TestAsyncOrderRotatingInvoiceClaimablePersists(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "async-order.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	const peerPubkey = "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	ctx := context.Background()
	inserted, err := store.InsertLightningAddressAccount(ctx, LightningAddressAccount{
		PeerPubkey: peerPubkey,
		Username:   "alice",
	})
	if err != nil {
		t.Fatalf("insert lightning address account: %v", err)
	}
	if !inserted {
		t.Fatalf("expected lightning address account insert")
	}

	if _, rpcErr, err := store.ApplyAsyncOrderNew(ctx, AsyncOrderNewRequest{
		PeerPubkey:      peerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes: []AsyncOrderNewHashInput{
			{
				HashIndex:   "1",
				PaymentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	}); err != nil {
		t.Fatalf("apply async order new: %v", err)
	} else if rpcErr != nil {
		t.Fatalf("apply async order new rpc error: %+v", rpcErr)
	}

	if _, err := store.MarkAsyncRotatingInvoiceClaimable(ctx, strings.Repeat("a", 64), 0, nil); !errors.Is(err, errAsyncRotatingInvoiceInvalidAmountMsat) {
		t.Fatalf("expected invalid amount_msat error, got %v", err)
	}

	assetID := "rgb-asset-a"
	assetAmount := uint64(10)

	reserved, err := store.ReserveLightningAddressInvoiceSlot(ctx, LightningAddressAccount{
		PeerPubkey: peerPubkey,
		Username:   "alice",
	}, 3_000_000, &assetID, &assetAmount, time.Hour)
	if err != nil {
		t.Fatalf("reserve invoice slot: %v", err)
	}
	if err := store.FinalizeLightningAddressInvoiceSlot(ctx, reserved.ID, "lnbc1claimabletest"); err != nil {
		t.Fatalf("finalize invoice slot: %v", err)
	}

	claimDeadlineHeight := uint32(400)
	if transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(ctx, reserved.PaymentHash, 3_000_000, &claimDeadlineHeight); err != nil {
		t.Fatalf("mark claimable: %v", err)
	} else if !transitioned {
		t.Fatalf("expected claimable transition")
	}

	var claimableAt sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT claimable_at FROM async_rotating_invoices WHERE payment_hash = ? LIMIT 1`, reserved.PaymentHash).Scan(&claimableAt); err != nil {
		t.Fatalf("query claimable_at: %v", err)
	}
	if !claimableAt.Valid || strings.TrimSpace(claimableAt.String) == "" {
		t.Fatalf("expected claimable_at to be set, got %#v", claimableAt)
	}

	var outboxCount int64
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM async_rotating_invoice_outbox WHERE payment_hash = ? AND action = ?`, reserved.PaymentHash, asyncOutboxActionRequestOutboundInvoice).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox count: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected one request_outbound_invoice outbox row, got %d", outboxCount)
	}

	job, ok, err := store.ClaimAsyncRotatingInvoiceOutboxJob(ctx)
	if err != nil {
		t.Fatalf("claim outbox job: %v", err)
	}
	if !ok {
		t.Fatalf("expected an outbox job to be claimable")
	}
	if job.PaymentHash != reserved.PaymentHash {
		t.Fatalf("expected job payment_hash %s, got %s", reserved.PaymentHash, job.PaymentHash)
	}
	if job.Action != asyncOutboxActionRequestOutboundInvoice {
		t.Fatalf("expected job action %s, got %s", asyncOutboxActionRequestOutboundInvoice, job.Action)
	}

	var rowAssetID sql.NullString
	var rowAssetAmount sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT asset_id, asset_amount FROM async_rotating_invoices WHERE payment_hash = ? LIMIT 1`, reserved.PaymentHash).Scan(&rowAssetID, &rowAssetAmount); err != nil {
		t.Fatalf("query asset columns: %v", err)
	}
	if !rowAssetID.Valid || rowAssetID.String != "rgb-asset-a" {
		t.Fatalf("expected asset_id to be persisted, got %#v", rowAssetID)
	}
	if !rowAssetAmount.Valid || rowAssetAmount.Int64 != 10 {
		t.Fatalf("expected asset_amount to be persisted, got %#v", rowAssetAmount)
	}
}

func TestAsyncOrderHTTPStatusFromErrorCode(t *testing.T) {
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
			if got := asyncOrderHTTPStatusFromErrorCode(tc.code); got != tc.want {
				t.Fatalf("status for code %d = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

func TestAsyncOrderAcceptedThroughIndexSurvivesPoolDeletion(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "async-order.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	resp, rpcErr, err := store.ApplyAsyncOrderNew(context.Background(), AsyncOrderNewRequest{
		PeerPubkey:      lightningAddressTestPeerPubkey,
		ProtocolVersion: asyncOrderProtocolVersion,
		Hashes: []AsyncOrderNewHashInput{
			{HashIndex: "1", PaymentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{HashIndex: "2", PaymentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	})
	if err != nil {
		t.Fatalf("apply async order: %v", err)
	}
	if rpcErr != nil {
		t.Fatalf("apply async order rpc error: %+v", rpcErr)
	}
	if resp.AcceptedThroughIndex != 2 {
		t.Fatalf("unexpected accepted_through_index in response: %d", resp.AcceptedThroughIndex)
	}

	orderID, err := strconv.ParseInt(resp.OrderID, 10, 64)
	if err != nil {
		t.Fatalf("parse order id: %v", err)
	}

	var acceptedThroughIndex sql.NullInt64
	if err := store.db.QueryRowContext(context.Background(), `SELECT accepted_through_index FROM async_orders WHERE order_id = ?`, orderID).Scan(&acceptedThroughIndex); err != nil {
		t.Fatalf("lookup accepted_through_index: %v", err)
	}
	if !acceptedThroughIndex.Valid || acceptedThroughIndex.Int64 != 2 {
		t.Fatalf("expected persisted accepted_through_index 2, got %+v", acceptedThroughIndex)
	}

	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM async_hash_pool WHERE order_id = ?`, orderID); err != nil {
		t.Fatalf("delete async hash pool rows: %v", err)
	}

	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback()
	})

	snapshot, err := store.asyncOrderSnapshotTx(context.Background(), tx, orderID)
	if err != nil {
		t.Fatalf("snapshot async order: %v", err)
	}
	if snapshot.AcceptedThroughIndex != 2 {
		t.Fatalf("snapshot accepted_through_index = %d, want 2", snapshot.AcceptedThroughIndex)
	}
	if snapshot.NextIndexExpected != 3 {
		t.Fatalf("snapshot next_index_expected = %d, want 3", snapshot.NextIndexExpected)
	}
	if snapshot.UnusedHashes != 0 {
		t.Fatalf("snapshot unused_hashes = %d, want 0", snapshot.UnusedHashes)
	}
	if snapshot.Status != asyncOrderStatusExhausted {
		t.Fatalf("snapshot status = %s, want exhausted", snapshot.Status)
	}
}
