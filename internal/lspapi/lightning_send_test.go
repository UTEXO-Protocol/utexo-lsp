package lspapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"
)

const (
	lsTestPayeePubkey = "03eadf9800000000000000000000000000000000000000000000000000000000aa"
	lsTestLSPPubkey   = "02aaaaaa00000000000000000000000000000000000000000000000000000000bb"
	lsTestPayoutAsset = "rgb:LNUSDT-payout"
	lsTestBridgeAsset = "rgb:BUSDT-bridge"
	lsTestHash        = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	lsTestPreimage    = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

// lsNodeStub answers the handful of node endpoints /lightning_send touches. Each
// field overrides one response; zero values give a node that says yes to
// everything a happy-path quote asks.
type lsNodeStub struct {
	t *testing.T

	decodeAssetID     string
	decodeAssetAmount int64
	decodeAmtMsat     int64
	decodePayee       string
	decodeNetwork     string
	decodeFinalCltv   uint64
	decodeExpirySec   int64

	// nodeNetwork is what /networkinfo reports. Separate from decodeNetwork so a
	// test can put the invoice on a different chain than the node.
	nodeNetwork string

	channelAssetID string
	channelAsset   uint64

	precision int

	height int64

	sendPaymentStatus string

	// Recorded calls, for asserting what the LSP did and did not do.
	invoiceRequests []node_client.LNInvoiceRequest
	sentInvoices    []string
	cancelledHashes []string
}

func (s *lsNodeStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/decodelninvoice":
			_ = json.NewEncoder(w).Encode(node_client.DecodeLNInvoiceResponse{
				AmtMsat:                 s.decodeAmtMsat,
				ExpirySec:               s.decodeExpirySec,
				Timestamp:               time.Now().UTC().Unix(),
				AssetID:                 s.decodeAssetID,
				AssetAmount:             s.decodeAssetAmount,
				PaymentHash:             lsTestHash,
				PayeePubkey:             s.decodePayee,
				MinFinalCltvExpiryDelta: s.decodeFinalCltv,
				Network:                 s.decodeNetwork,
			})
		case "/nodeinfo":
			_ = json.NewEncoder(w).Encode(node_client.NodeInfoResponse{Pubkey: lsTestLSPPubkey})
		case "/networkinfo":
			_ = json.NewEncoder(w).Encode(node_client.NetworkInfoResponse{
				Network: s.nodeNetwork,
				Height:  s.height,
			})
		case "/listchannels":
			_ = json.NewEncoder(w).Encode(node_client.ListChannelsResponse{Channels: []node_client.Channel{{
				PeerPubkey:                s.decodePayee,
				AssetID:                   &s.channelAssetID,
				AssetLocalAmount:          s.channelAsset,
				IsUsable:                  true,
				NextOutboundHTLCLimitMsat: 100_000_000,
			}}})
		case "/assetmetadata":
			_ = json.NewEncoder(w).Encode(node_client.AssetMetadataResponse{
				AssetSchema: "Ifa",
				Precision:   s.precision,
			})
		case "/lninvoice":
			var req node_client.LNInvoiceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.invoiceRequests = append(s.invoiceRequests, req)
			_ = json.NewEncoder(w).Encode(node_client.LNInvoiceResponse{Invoice: "lnbcrt-hodl-" + lsTestHash})
		case "/sendpayment":
			var req node_client.SendPaymentRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.sentInvoices = append(s.sentInvoices, req.Invoice)
			status := s.sendPaymentStatus
			if status == "" {
				status = node_client.PaymentStatusPending
			}
			_ = json.NewEncoder(w).Encode(node_client.SendPaymentResponse{PaymentHash: lsTestHash, Status: status})
		case "/claimhodlinvoice":
			_ = json.NewEncoder(w).Encode(node_client.ClaimHodlInvoiceResponse{Success: true})
		case "/cancelhodlinvoice":
			var req node_client.CancelHodlInvoiceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.cancelledHashes = append(s.cancelledHashes, req.PaymentHash)
			_ = json.NewEncoder(w).Encode(struct{}{})
		default:
			http.NotFound(w, r)
		}
	}
}

func newLightningSendTestAPI(t *testing.T, stub *lsNodeStub, mutate func(*Config)) *API {
	t.Helper()

	if stub.decodeAssetID == "" {
		stub.decodeAssetID = lsTestBridgeAsset
	}
	if stub.decodeAssetAmount == 0 {
		stub.decodeAssetAmount = 500_000
	}
	if stub.decodeAmtMsat == 0 {
		stub.decodeAmtMsat = 3_000_000
	}
	if stub.decodePayee == "" {
		stub.decodePayee = lsTestPayeePubkey
	}
	if stub.decodeNetwork == "" {
		stub.decodeNetwork = "Regtest"
	}
	if stub.nodeNetwork == "" {
		stub.nodeNetwork = "Regtest"
	}
	if stub.decodeFinalCltv == 0 {
		stub.decodeFinalCltv = 42
	}
	if stub.decodeExpirySec == 0 {
		stub.decodeExpirySec = 3600
	}
	if stub.channelAssetID == "" {
		stub.channelAssetID = stub.decodeAssetID
	}
	if stub.channelAsset == 0 {
		stub.channelAsset = 1_000_000
	}
	if stub.height == 0 {
		stub.height = 1000
	}
	stub.t = t

	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	client := node_client.NewClient(srv.URL, "", srv.Client())

	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lightning-send.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := Config{
		HTTPTimeout:                        2 * time.Second,
		LightningSendEnabled:               true,
		APayInboundInvoiceExpiry:           time.Hour,
		APayInboundMinFinalCltvExpiryDelta: defaultAPayInboundMinFinalCltvExpiryDelta,
		APayClaimMarginBlocks:              defaultAPayClaimMarginBlocks,
		APayBearerToken:                    "secret",
		SupportedAssetIDs:                  []string{lsTestPayoutAsset},
		ConvertibleAssetIDs:                []string{lsTestBridgeAsset},
		ConvertiblePairs:                   [][2]string{{lsTestPayoutAsset, lsTestBridgeAsset}},
		GetInfoAssetsTTL:                   time.Minute,
		DefaultChannelAssetAmount:          1_000_000,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	return &API{cfg: cfg, db: store, lspClient: client, rgbClient: client}
}

func postLightningSend(t *testing.T, api *API, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/lightning_send", strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleLightningSend(rec, req)
	return rec
}

// The happy path: a BUSDT invoice quoted in LNUSDT. The quote is only worth
// anything if the HODL invoice carries the third party's hash — that identity is
// the entire atomicity guarantee — so this asserts it explicitly.
func TestLightningSendQuotesConvertedLegAgainstForeignHash(t *testing.T) {
	stub := &lsNodeStub{}
	api := newLightningSendTestAPI(t, stub, nil)

	rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external","pay_with_asset_id":"`+lsTestPayoutAsset+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp LightningSendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PaymentHash != lsTestHash {
		t.Fatalf("expected the third party's hash %s, got %s", lsTestHash, resp.PaymentHash)
	}
	if !resp.Converted {
		t.Fatal("expected the response to declare the legs converted")
	}
	if resp.Inbound.AssetID != lsTestPayoutAsset || resp.Outbound.AssetID != lsTestBridgeAsset {
		t.Fatalf("legs are the wrong way round: in=%s out=%s", resp.Inbound.AssetID, resp.Outbound.AssetID)
	}
	// 1:1 in base units is the whole rate; a spread would be fee_msat, not units.
	if resp.Inbound.AssetAmount != resp.Outbound.AssetAmount {
		t.Fatalf("expected 1:1, got in=%d out=%d", resp.Inbound.AssetAmount, resp.Outbound.AssetAmount)
	}

	if len(stub.invoiceRequests) != 1 {
		t.Fatalf("expected exactly one /lninvoice call, got %d", len(stub.invoiceRequests))
	}
	got := stub.invoiceRequests[0]
	if got.PaymentHash == nil || *got.PaymentHash != lsTestHash {
		t.Fatal("the HODL invoice must be created against the third party's payment hash")
	}
	if got.AssetID == nil || *got.AssetID != lsTestPayoutAsset {
		t.Fatal("the HODL invoice must be denominated in the asset the caller pays with")
	}
}

// Every rejection must happen before the node is asked for an invoice: a refused
// request that still parks a HODL invoice is a griefing vector.
func TestLightningSendRejectionsCreateNoInvoice(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*lsNodeStub)
		cfg     func(*Config)
		body    string
		wantMsg string
	}{
		{
			name:    "pair not declared convertible",
			cfg:     func(c *Config) { c.ConvertiblePairs = nil },
			body:    `{"invoice":"lnbcrt-external","pay_with_asset_id":"` + lsTestPayoutAsset + `"}`,
			wantMsg: "CONVERTIBLE_PAIRS",
		},
		{
			name:    "amountless invoice",
			mutate:  func(s *lsNodeStub) { s.decodeAmtMsat = -1 },
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "must carry an amount",
		},
		{
			name:    "no asset on the invoice",
			mutate:  func(s *lsNodeStub) { s.decodeAssetID = "none"; s.decodeAssetAmount = -1 },
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "asset_amount",
		},
		{
			name:    "payable to the lsp itself",
			mutate:  func(s *lsNodeStub) { s.decodePayee = lsTestLSPPubkey },
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "payable to this LSP itself",
		},
		{
			name:    "wrong network",
			mutate:  func(s *lsNodeStub) { s.decodeNetwork = "Mainnet" },
			cfg:     nil,
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "this LSP is on",
		},
		{
			name:    "no channel with enough of the delivery asset",
			mutate:  func(s *lsNodeStub) { s.channelAsset = 1 },
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "cannot deliver",
		},
		{
			name:    "cltv budget too small",
			mutate:  func(s *lsNodeStub) { s.decodeFinalCltv = 200 },
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "blocks of cltv",
		},
		{
			name:    "over the per-payment ceiling",
			cfg:     func(c *Config) { c.LightningSendMaxAssetAmount = 1000 },
			body:    `{"invoice":"lnbcrt-external"}`,
			wantMsg: "exceeds the per-payment limit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &lsNodeStub{}
			if tc.mutate != nil {
				tc.mutate(stub)
			}
			// The "none"/-1 sentinels above mean "leave empty", which the defaults
			// would otherwise fill back in.
			if stub.decodeAssetID == "none" {
				stub.decodeAssetID = ""
			}
			api := newLightningSendTestAPI(t, stub, tc.cfg)
			if stub.decodeAmtMsat == -1 {
				stub.decodeAmtMsat = 0
			}
			if stub.decodeAssetAmount == -1 {
				stub.decodeAssetAmount = 0
			}

			rec := postLightningSend(t, api, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("expected the reason to mention %q, got %s", tc.wantMsg, rec.Body.String())
			}
			if len(stub.invoiceRequests) != 0 {
				t.Fatalf("a rejected request must not create a HODL invoice, got %d", len(stub.invoiceRequests))
			}
		})
	}
}

// Disabled means the route does not exist, not that it answers differently.
func TestLightningSendDisabledIs404(t *testing.T) {
	api := newLightningSendTestAPI(t, &lsNodeStub{}, func(c *Config) { c.LightningSendEnabled = false })
	rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// Omitting pay_with_asset_id asks the LSP to resolve the counterpart, which it
// may only do when CONVERTIBLE_PAIRS leaves no choice.
func TestLightningSendResolvesSoleCounterpart(t *testing.T) {
	api := newLightningSendTestAPI(t, &lsNodeStub{}, nil)
	rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp LightningSendResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Inbound.AssetID != lsTestPayoutAsset {
		t.Fatalf("expected the sole counterpart %s, got %s", lsTestPayoutAsset, resp.Inbound.AssetID)
	}
}

func TestLightningSendAmbiguousCounterpartIsRefused(t *testing.T) {
	const third = "rgb:THIRD-asset"
	api := newLightningSendTestAPI(t, &lsNodeStub{}, func(c *Config) {
		c.SupportedAssetIDs = append(c.SupportedAssetIDs, third)
		c.ConvertiblePairs = append(c.ConvertiblePairs, [2]string{third, lsTestBridgeAsset})
	})
	rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ambiguous") {
		t.Fatalf("expected an ambiguity error, got %s", rec.Body.String())
	}
}

// The same hash must never back two relays: the first preimage would settle both
// held HTLCs.
func TestLightningSendRefusesDuplicateHash(t *testing.T) {
	stub := &lsNodeStub{}
	api := newLightningSendTestAPI(t, stub, nil)

	if rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`); rec.Code != http.StatusOK {
		t.Fatalf("first quote failed: %s", rec.Body.String())
	}
	rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on the second quote, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(stub.invoiceRequests) != 1 {
		t.Fatalf("the duplicate must not reach /lninvoice, got %d calls", len(stub.invoiceRequests))
	}
}

// The full relay: held → delivery paid → preimage → inbound claimed.
func TestLightningSendSettlesAfterPreimage(t *testing.T) {
	stub := &lsNodeStub{}
	api := newLightningSendTestAPI(t, stub, nil)
	ctx := context.Background()

	if rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`); rec.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", rec.Body.String())
	}

	deadline := uint32(stub.height + 200)
	if err := api.markLightningSendClaimable(ctx, lsTestHash, &deadline); err != nil {
		t.Fatalf("claimable: %v", err)
	}
	if err := api.lightningSendPayOutboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("pay outbound: %v", err)
	}
	if len(stub.sentInvoices) != 1 || stub.sentInvoices[0] != "lnbcrt-external" {
		t.Fatalf("expected the third party's invoice to be paid verbatim, got %v", stub.sentInvoices)
	}

	if err := api.markLightningSendPreimage(ctx, lsTestHash, lsTestPreimage); err != nil {
		t.Fatalf("preimage: %v", err)
	}
	if err := api.lightningSendClaimInboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("claim inbound: %v", err)
	}

	rec, err := api.db.LoadLightningSendByPaymentHash(ctx, lsTestHash)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.Status != lightningSendStateSettled {
		t.Fatalf("expected settled, got %s", rec.Status)
	}
}

// The LSP must not pay a delivery leg it can no longer collect against. Refusing
// here is what keeps a griefing payer from turning a relay into a loss.
func TestLightningSendRefusesToPayNearTheClaimDeadline(t *testing.T) {
	stub := &lsNodeStub{}
	api := newLightningSendTestAPI(t, stub, nil)
	ctx := context.Background()

	if rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`); rec.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", rec.Body.String())
	}

	// Ten blocks left against a 42-block delivery leg plus a 12-block margin.
	deadline := uint32(stub.height + 10)
	if err := api.markLightningSendClaimable(ctx, lsTestHash, &deadline); err != nil {
		t.Fatalf("claimable: %v", err)
	}
	if err := api.lightningSendPayOutboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("the job must absorb the refusal rather than retry forever: %v", err)
	}

	if len(stub.sentInvoices) != 0 {
		t.Fatalf("nothing may be paid past the deadline, got %v", stub.sentInvoices)
	}
	if len(stub.cancelledHashes) != 1 || stub.cancelledHashes[0] != lsTestHash {
		t.Fatalf("the held HTLC must be failed back, got %v", stub.cancelledHashes)
	}
	rec, _ := api.db.LoadLightningSendByPaymentHash(ctx, lsTestHash)
	if rec.Status != lightningSendStateFailed {
		t.Fatalf("expected a terminal failed state, got %s", rec.Status)
	}
}

// A delivery the node reports as failed refunds the caller immediately instead of
// holding its money to CLTV expiry.
func TestLightningSendRefundsOnReportedFailure(t *testing.T) {
	stub := &lsNodeStub{sendPaymentStatus: node_client.PaymentStatusFailed}
	api := newLightningSendTestAPI(t, stub, nil)
	ctx := context.Background()

	if rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`); rec.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", rec.Body.String())
	}
	deadline := uint32(stub.height + 200)
	if err := api.markLightningSendClaimable(ctx, lsTestHash, &deadline); err != nil {
		t.Fatalf("claimable: %v", err)
	}
	if err := api.lightningSendPayOutboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("a reported failure is terminal, not a retry: %v", err)
	}

	if len(stub.cancelledHashes) != 1 {
		t.Fatalf("expected the HODL to be cancelled, got %v", stub.cancelledHashes)
	}
	rec, _ := api.db.LoadLightningSendByPaymentHash(ctx, lsTestHash)
	if rec.Status != lightningSendStateCancelled {
		t.Fatalf("expected cancelled, got %s", rec.Status)
	}
}

// Both node webhooks fire for every held invoice and every sent payment, so they
// must route by hash rather than assume APay owns it.
func TestLightningSendOwnsHashRoutesWebhooks(t *testing.T) {
	api := newLightningSendTestAPI(t, &lsNodeStub{}, nil)
	ctx := context.Background()

	owned, err := api.lightningSendOwnsHash(ctx, lsTestHash)
	if err != nil {
		t.Fatalf("owns: %v", err)
	}
	if owned {
		t.Fatal("an unknown hash must fall through to the APay path")
	}

	if rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`); rec.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", rec.Body.String())
	}
	owned, err = api.lightningSendOwnsHash(ctx, lsTestHash)
	if err != nil {
		t.Fatalf("owns: %v", err)
	}
	if !owned {
		t.Fatal("a quoted hash must be routed to lightning_send")
	}
}

// Replays are the normal case for both webhooks, so neither may error on one.
func TestLightningSendWebhookReplaysAreIdempotent(t *testing.T) {
	stub := &lsNodeStub{}
	api := newLightningSendTestAPI(t, stub, nil)
	ctx := context.Background()

	if rec := postLightningSend(t, api, `{"invoice":"lnbcrt-external"}`); rec.Code != http.StatusOK {
		t.Fatalf("quote failed: %s", rec.Body.String())
	}
	deadline := uint32(stub.height + 200)
	for i := 0; i < 2; i++ {
		if err := api.markLightningSendClaimable(ctx, lsTestHash, &deadline); err != nil {
			t.Fatalf("claimable replay %d: %v", i, err)
		}
	}
	if err := api.lightningSendPayOutboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("pay: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := api.markLightningSendPreimage(ctx, lsTestHash, lsTestPreimage); err != nil {
			t.Fatalf("preimage replay %d: %v", i, err)
		}
	}
	if err := api.lightningSendClaimInboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// A second claim job is what an outbox redelivery looks like.
	if err := api.lightningSendClaimInboundJob(ctx, lsTestHash); err != nil {
		t.Fatalf("claim replay: %v", err)
	}
}

// The node notifies on every outbound payment it makes, most of which belong to
// neither flow — a lightning_receive delivery has no preimage bookkeeping here at
// all. Answering 500 to those made the node warn once per delivery and buried
// real failures in the noise.
func TestPaymentSentForAnUnknownHashIsNotAnError(t *testing.T) {
	api := newLightningSendTestAPI(t, &lsNodeStub{}, nil)

	// A real preimage/hash pair: the handler verifies sha256(preimage) before it
	// gets anywhere near deciding whose hash it is.
	const unknownPreimage = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	const unknownHash = "4540361a95b29edfc9ea42d255b5935348af1f949241358bda7b85753394ac10"

	body := `{"payment_hash":"` + unknownHash + `","payment_preimage":"` + unknownPreimage + `"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/payment_sent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	api.handleInternalAsyncOrderPaymentSent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a hash neither flow owns, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ignored":true`) {
		t.Fatalf("the response must say it did nothing, got %s", rec.Body.String())
	}
}

// A standing skip must be logged when it is decided, not on every 5s tick. The
// grace reason counts seconds, so it differs on each tick even though nothing
// changed — dedup therefore keys on the kind, never on the message.
func TestSkipProvisioningIsLoggedOncePerDecision(t *testing.T) {
	api := newLightningSendTestAPI(t, &lsNodeStub{}, nil)
	peer := lsTestPayeePubkey
	asset := lsTestPayoutAsset

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for i := 0; i < 5; i++ {
		api.noteSkip(peer, &asset, skipPayoutMismatch, "peer is paid out in something else")
	}
	if n := strings.Count(buf.String(), "skip openchannel"); n != 1 {
		t.Fatalf("expected one line for five identical ticks, got %d:\n%s", n, buf.String())
	}

	// A different reason for the same peer is news and must be logged.
	api.noteSkip(peer, &asset, skipProvisionGrace, "first seen 5s ago")
	if n := strings.Count(buf.String(), "skip openchannel"); n != 2 {
		t.Fatalf("a changed reason must be logged, got %d lines:\n%s", n, buf.String())
	}

	// Once the channel exists the memo is dropped, so the same reason recurring
	// later is reported again rather than swallowed.
	api.clearSkip(peer, &asset)
	api.noteSkip(peer, &asset, skipProvisionGrace, "first seen 5s ago")
	if n := strings.Count(buf.String(), "skip openchannel"); n != 3 {
		t.Fatalf("a reason recurring after a success must be logged, got %d lines:\n%s", n, buf.String())
	}
}
