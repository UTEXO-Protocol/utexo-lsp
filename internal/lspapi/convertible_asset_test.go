package lspapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"utexo-lsp/pkg/node_client"
)

const (
	payoutTestAssetID = "rgb:payout-LnUSDT"
	bridgeTestAssetID = "rgb:bridge-bUSDT"
)

// convertibleAssetNode is a stand-in for the LSP's own rgb-lightning-node: the
// peer holds one payout-asset channel, and the two assets are ordinary,
// unrelated contracts — no linked_from/linked_to, no Link transfer.
//
// There is deliberately no /listtransfers case: the stub fails the test if the
// LSP ever goes looking for a link proof again.
type convertibleAssetNode struct {
	t *testing.T

	peerPubkey     string
	peerChannels   []node_client.Channel
	metadata       map[string]node_client.AssetMetadataResponse
	invoiceRequest map[string]any
}

func newConvertibleAssetNode(t *testing.T) *convertibleAssetNode {
	t.Helper()
	payout := payoutTestAssetID
	return &convertibleAssetNode{
		t:          t,
		peerPubkey: lightningAddressTestPeerPubkey,
		peerChannels: []node_client.Channel{
			{PeerPubkey: lightningAddressTestPeerPubkey, AssetID: &payout, IsUsable: true},
		},
		metadata: map[string]node_client.AssetMetadataResponse{
			payoutTestAssetID: {AssetSchema: "Ifa", Name: "LnUSDT", Ticker: "LNUSDT", Precision: 6},
			bridgeTestAssetID: {AssetSchema: "Ifa", Name: "bridgeUSDT", Ticker: "BUSDT", Precision: 6},
		},
	}
}

func (n *convertibleAssetNode) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			n.t.Fatalf("read request body: %v", err)
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}

	switch req.URL.Path {
	case "/listchannels":
		return jsonStubResponse(n.t, http.StatusOK, map[string]any{"channels": n.peerChannels}, req), nil
	case "/assetmetadata":
		assetID, _ := body["asset_id"].(string)
		meta, ok := n.metadata[assetID]
		if !ok {
			return jsonStubResponse(n.t, http.StatusForbidden, map[string]string{"error": "unknown contract id"}, req), nil
		}
		return jsonStubResponse(n.t, http.StatusOK, meta, req), nil
	case "/lninvoice":
		n.invoiceRequest = body
		return jsonStubResponse(n.t, http.StatusOK, map[string]string{"invoice": "lnbc1hodlinvoice"}, req), nil
	default:
		n.t.Fatalf("unexpected request path: %s", req.URL.Path)
		return nil, nil
	}
}

func (n *convertibleAssetNode) client() *node_client.Client {
	return node_client.NewClient("http://convertible-asset-stub", "", &http.Client{Transport: n})
}

// newConvertibleAssetTestAPI serves the payout asset — the one the cron opens
// channels in — and accepts canonical USDT over a channel the peer funded
// itself, which is what CONVERTIBLE_ASSET_IDS means. CONVERTIBLE_PAIRS is the
// operator's statement that the two are interchangeable at 1:1.
func newConvertibleAssetTestAPI(t *testing.T, node *convertibleAssetNode) (*API, LightningAddressAccount) {
	t.Helper()
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", node.client())
	api.cfg.SupportedAssetIDs = []string{payoutTestAssetID}
	api.cfg.ConvertibleAssetIDs = []string{bridgeTestAssetID}
	api.cfg.ConvertiblePairs = [][2]string{{payoutTestAssetID, bridgeTestAssetID}}
	return api, account
}

func TestResolveInvoiceAssetPairConvertsBridgeAssetToPayoutAsset(t *testing.T) {
	node := newConvertibleAssetNode(t)
	api, account := newConvertibleAssetTestAPI(t, node)

	bridge := bridgeTestAssetID
	amount := uint64(500_000)
	pair, err := api.resolveInvoiceAssetPair(context.Background(), account, &bridge, &amount)
	if err != nil {
		t.Fatalf("resolve asset pair: %v", err)
	}
	if !pair.Converted {
		t.Fatalf("expected a converted pair, got %+v", pair)
	}
	if optionalAssetID(pair.InboundAssetID) != bridgeTestAssetID {
		t.Fatalf("inbound leg should stay the quoted asset, got %+v", pair.InboundAssetID)
	}
	if optionalAssetID(pair.OutboundAssetID) != payoutTestAssetID {
		t.Fatalf("outbound leg should be the payout asset, got %+v", pair.OutboundAssetID)
	}
	if pair.OutboundAssetAmount == nil || *pair.OutboundAssetAmount != amount {
		t.Fatalf("expected a 1:1 rate, got %+v", pair.OutboundAssetAmount)
	}

	// The payout asset is pinned on the account, so it survives the channel.
	stored, err := api.db.GetLightningAddressAccountByPeerPubkey(context.Background(), account.PeerPubkey)
	if err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if optionalAssetID(stored.PayoutAssetID) != payoutTestAssetID {
		t.Fatalf("expected payout asset to be persisted, got %+v", stored.PayoutAssetID)
	}
}

func TestResolveInvoiceAssetPairLeavesPayoutAssetUnconverted(t *testing.T) {
	node := newConvertibleAssetNode(t)
	api, account := newConvertibleAssetTestAPI(t, node)

	payout := payoutTestAssetID
	amount := uint64(500_000)
	pair, err := api.resolveInvoiceAssetPair(context.Background(), account, &payout, &amount)
	if err != nil {
		t.Fatalf("resolve asset pair: %v", err)
	}
	if pair.Converted {
		t.Fatalf("paying in the payout asset must not convert: %+v", pair)
	}
	if optionalAssetID(pair.OutboundAssetID) != payoutTestAssetID {
		t.Fatalf("unexpected outbound leg: %+v", pair.OutboundAssetID)
	}
}

// An asset this LSP neither serves nor accepts is refused before anything else:
// this is what keeps the conversion branch from quoting arbitrary contracts.
func TestResolveInvoiceAssetPairRejectsAssetThisLSPDoesNotDeliver(t *testing.T) {
	node := newConvertibleAssetNode(t)
	foreign := "rgb:unrelated-asset"
	node.metadata[foreign] = node_client.AssetMetadataResponse{AssetSchema: "Nia", Name: "Unrelated", Precision: 6}
	api, account := newConvertibleAssetTestAPI(t, node)

	amount := uint64(500_000)
	_, err := api.resolveInvoiceAssetPair(context.Background(), account, &foreign, &amount)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected an undeliverable asset to be rejected, got %v", err)
	}
}

// Payout-eligible is not the same as convertible: an asset the LSP would happily
// pay out over an existing channel still may not be quoted against a *different*
// payout asset unless the operator declared the pair.
func TestResolveInvoiceAssetPairRejectsPairOutsideConvertiblePairs(t *testing.T) {
	node := newConvertibleAssetNode(t)
	other := "rgb:second-bridge"
	node.metadata[other] = node_client.AssetMetadataResponse{AssetSchema: "Ifa", Name: "otherUSDT", Ticker: "OUSDT", Precision: 6}
	api, account := newConvertibleAssetTestAPI(t, node)
	api.cfg.ConvertibleAssetIDs = append(api.cfg.ConvertibleAssetIDs, other)

	amount := uint64(500_000)
	_, err := api.resolveInvoiceAssetPair(context.Background(), account, &other, &amount)
	if err == nil || !strings.Contains(err.Error(), "CONVERTIBLE_PAIRS") {
		t.Fatalf("expected an undeclared pair to be rejected, got %v", err)
	}
}

func TestResolveInvoiceAssetPairRejectsPrecisionMismatch(t *testing.T) {
	node := newConvertibleAssetNode(t)
	bridgeMeta := node.metadata[bridgeTestAssetID]
	bridgeMeta.Precision = 0
	node.metadata[bridgeTestAssetID] = bridgeMeta
	api, account := newConvertibleAssetTestAPI(t, node)

	bridge := bridgeTestAssetID
	amount := uint64(500_000)
	_, err := api.resolveInvoiceAssetPair(context.Background(), account, &bridge, &amount)
	if err == nil || !strings.Contains(err.Error(), "precision") {
		t.Fatalf("expected a precision rejection, got %v", err)
	}
}

// The callback must decide before it reserves: a rejected pair may not spend a
// payment hash out of the receiver's pre-signed batch.
func TestLightningAddressCallbackRejectsUnconvertibleAssetWithoutSpendingAHash(t *testing.T) {
	node := newConvertibleAssetNode(t)
	foreign := "rgb:unrelated-asset"
	node.metadata[foreign] = node_client.AssetMetadataResponse{AssetSchema: "Nia", Name: "Unrelated", Precision: 6}
	api, account := newConvertibleAssetTestAPI(t, node)
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+
		"?amount=3000000&asset_id="+url.QueryEscape(foreign)+"&asset_amount=500000", nil)
	rr := httptest.NewRecorder()
	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if node.invoiceRequest != nil {
		t.Fatalf("no invoice should have been minted: %#v", node.invoiceRequest)
	}

	store := api.db.(*SQLStore)
	var available int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM async_hash_pool WHERE status = ?`, asyncPoolStatusAvailable).Scan(&available); err != nil {
		t.Fatalf("count available hashes: %v", err)
	}
	if available != 1 {
		t.Fatalf("expected the hash pool to be untouched, got %d available", available)
	}
}

// The invoice the payer pays is denominated in what it asked for; the receiver's
// asset only appears on the outbound leg.
func TestLightningAddressCallbackQuotesInboundLegAndStoresBoth(t *testing.T) {
	node := newConvertibleAssetNode(t)
	api, account := newConvertibleAssetTestAPI(t, node)
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+
		"?amount=3000000&asset_id="+url.QueryEscape(bridgeTestAssetID)+"&asset_amount=500000", nil)
	rr := httptest.NewRecorder()
	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := node.invoiceRequest["asset_id"]; got != bridgeTestAssetID {
		t.Fatalf("hodl invoice should be in the payer's asset, got %#v", got)
	}

	store := api.db.(*SQLStore)
	var inboundAsset, outboundAsset string
	var inboundAmount, outboundAmount int64
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT asset_id, asset_amount, outbound_asset_id, outbound_asset_amount FROM async_rotating_invoices LIMIT 1`).
		Scan(&inboundAsset, &inboundAmount, &outboundAsset, &outboundAmount); err != nil {
		t.Fatalf("load rotating invoice: %v", err)
	}
	if inboundAsset != bridgeTestAssetID || outboundAsset != payoutTestAssetID {
		t.Fatalf("unexpected legs: inbound=%s outbound=%s", inboundAsset, outboundAsset)
	}
	if inboundAmount != 500_000 || outboundAmount != 500_000 {
		t.Fatalf("expected 1:1 amounts, got inbound=%d outbound=%d", inboundAmount, outboundAmount)
	}
}

func TestLightningAddressDiscoveryAdvertisesPayoutAndConvertibleAssets(t *testing.T) {
	node := newConvertibleAssetNode(t)
	api, account := newConvertibleAssetTestAPI(t, node)

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
	if resp.PayoutAsset == nil || resp.PayoutAsset.AssetID != payoutTestAssetID {
		t.Fatalf("expected the served asset as payout asset, got %+v", resp.PayoutAsset)
	}
	if resp.PayoutAsset.Precision != 6 || resp.PayoutAsset.Ticker != "LNUSDT" {
		t.Fatalf("payout asset should carry display metadata, got %+v", resp.PayoutAsset)
	}
	if len(resp.AcceptedAssets) != 2 || resp.AcceptedAssets[1].AssetID != bridgeTestAssetID {
		t.Fatalf("expected the bridge asset to be advertised, got %+v", resp.AcceptedAssets)
	}
}

// Discovery must not promise what the callback would refuse: with the pair
// removed, canonical USDT is still payout-eligible but no longer quotable.
func TestLightningAddressDiscoveryDropsAssetsWithoutADeclaredPair(t *testing.T) {
	node := newConvertibleAssetNode(t)
	api, _ := newConvertibleAssetTestAPI(t, node)
	api.cfg.ConvertiblePairs = nil

	accepted := api.acceptedAssets(context.Background(), payoutTestAssetID)
	if len(accepted) != 1 || accepted[0].AssetID != payoutTestAssetID {
		t.Fatalf("expected only the payout asset, got %+v", accepted)
	}
}

// The reverse direction: the peer funded its own channel in canonical USDT
// while the cron opened it one in the served asset, so the LSP has to be told
// which of the two it is paid in — PAYOUT_ASSET_PREFERENCE.

func withBothChannels(node *convertibleAssetNode) {
	bridge := bridgeTestAssetID
	node.peerChannels = append(node.peerChannels, node_client.Channel{
		PeerPubkey: lightningAddressTestPeerPubkey, AssetID: &bridge, IsUsable: true,
	})
}

func TestPayoutAssetPrefersConvertibleChannelWhenConfigured(t *testing.T) {
	node := newConvertibleAssetNode(t)
	withBothChannels(node)
	api, account := newConvertibleAssetTestAPI(t, node)
	api.cfg.PayoutAssetPreference = []string{bridgeTestAssetID}

	payout := payoutTestAssetID
	amount := uint64(200_000)
	pair, err := api.resolveInvoiceAssetPair(context.Background(), account, &payout, &amount)
	if err != nil {
		t.Fatalf("resolve asset pair: %v", err)
	}
	if !pair.Converted {
		t.Fatalf("expected served → bridge conversion, got %+v", pair)
	}
	if optionalAssetID(pair.OutboundAssetID) != bridgeTestAssetID {
		t.Fatalf("outbound leg should be the convertible payout asset, got %+v", pair.OutboundAssetID)
	}
	if pair.OutboundAssetAmount == nil || *pair.OutboundAssetAmount != amount {
		t.Fatalf("expected a 1:1 rate, got %+v", pair.OutboundAssetAmount)
	}
}

// A convertible asset is deliverable without being provisioned, and with a single
// asset channel no preference list is needed.
func TestPayoutAssetResolvesFromConvertibleOnlyChannel(t *testing.T) {
	node := newConvertibleAssetNode(t)
	bridge := bridgeTestAssetID
	node.peerChannels = []node_client.Channel{
		{PeerPubkey: lightningAddressTestPeerPubkey, AssetID: &bridge, IsUsable: true},
	}
	api, account := newConvertibleAssetTestAPI(t, node)

	payout := payoutTestAssetID
	amount := uint64(200_000)
	pair, err := api.resolveInvoiceAssetPair(context.Background(), account, &payout, &amount)
	if err != nil {
		t.Fatalf("resolve asset pair: %v", err)
	}
	if optionalAssetID(pair.OutboundAssetID) != bridgeTestAssetID {
		t.Fatalf("expected the bridge asset as payout asset, got %+v", pair.OutboundAssetID)
	}
}

// Without the convertible list the same channel is invisible, which is the
// behaviour every existing deployment has today.
func TestPayoutAssetIgnoresConvertibleChannelWhenNotConfigured(t *testing.T) {
	node := newConvertibleAssetNode(t)
	bridge := bridgeTestAssetID
	node.peerChannels = []node_client.Channel{
		{PeerPubkey: lightningAddressTestPeerPubkey, AssetID: &bridge, IsUsable: true},
	}
	api, account := newConvertibleAssetTestAPI(t, node)
	api.cfg.ConvertibleAssetIDs = nil
	api.cfg.ConvertiblePairs = nil

	if got := api.payoutAssetFromChannelList(account.PeerPubkey, node.peerChannels); got != "" {
		t.Fatalf("expected no payout asset, got %q", got)
	}
	amount := uint64(200_000)
	if _, err := api.resolveInvoiceAssetPair(context.Background(), account, &bridge, &amount); err == nil {
		t.Fatal("expected an asset this LSP cannot deliver to be rejected")
	}
}

// Two eligible assets and no preference: the LSP still refuses to guess rather
// than pinning one and quietly making every future payment land in it.
func TestPayoutAssetStaysAmbiguousWithoutPreference(t *testing.T) {
	node := newConvertibleAssetNode(t)
	withBothChannels(node)
	api, account := newConvertibleAssetTestAPI(t, node)

	if got := api.payoutAssetFromChannelList(account.PeerPubkey, node.peerChannels); got != "" {
		t.Fatalf("expected an ambiguous payout asset, got %q", got)
	}
}

func TestSkipProvisioningWhenPeerIsPaidOutInAnotherAsset(t *testing.T) {
	node := newConvertibleAssetNode(t)
	bridge := bridgeTestAssetID
	// The peer's only channel is the one it funded itself, in canonical USDT.
	node.peerChannels = []node_client.Channel{
		{PeerPubkey: lightningAddressTestPeerPubkey, AssetID: &bridge, IsUsable: true},
	}
	api, account := newConvertibleAssetTestAPI(t, node)

	payout := payoutTestAssetID
	reason := api.skipProvisioning(account.PeerPubkey, &payout, node.peerChannels, &account)
	if reason == "" {
		t.Fatal("expected the cron to skip the served asset for a peer paid out in the bridge asset")
	}
	if !strings.Contains(reason, bridgeTestAssetID) {
		t.Fatalf("reason should name the payout asset, got %q", reason)
	}

	// The payout asset itself is still provisioned: the rule is "follow the
	// peer", not "never open".
	if reason := api.skipProvisioning(account.PeerPubkey, &bridge, node.peerChannels, &account); reason != "" {
		t.Fatalf("expected the payout asset to stay provisionable, got %q", reason)
	}
}

func TestProvisionsServedAssetForAPeerWithNoChannels(t *testing.T) {
	node := newConvertibleAssetNode(t)
	node.peerChannels = nil
	api, account := newConvertibleAssetTestAPI(t, node)

	payout := payoutTestAssetID
	if reason := api.skipProvisioning(account.PeerPubkey, &payout, nil, &account); reason != "" {
		t.Fatalf("a peer with no channels must still be provisioned, got %q", reason)
	}
}

// A pinned payout asset outranks the channel list: it is what discovery already
// promised the payer, so the cron must not provision against it.
func TestSkipProvisioningHonoursPinnedPayoutAsset(t *testing.T) {
	node := newConvertibleAssetNode(t)
	node.peerChannels = nil
	api, account := newConvertibleAssetTestAPI(t, node)
	bridge := bridgeTestAssetID
	account.PayoutAssetID = &bridge

	payout := payoutTestAssetID
	if reason := api.skipProvisioning(account.PeerPubkey, &payout, nil, &account); reason == "" {
		t.Fatal("expected a pinned payout asset to block provisioning of another one")
	}
}

// The grace window covers the gap between a client connecting and its own
// funding tx appearing, which is the only moment it is indistinguishable from a
// peer waiting to be provisioned.
func TestChannelProvisionGraceDefersAFreshPeer(t *testing.T) {
	node := newConvertibleAssetNode(t)
	node.peerChannels = nil
	api, account := newConvertibleAssetTestAPI(t, node)
	api.cfg.ChannelProvisionGrace = time.Minute
	account.CreatedAt = time.Now()

	payout := payoutTestAssetID
	if reason := api.skipProvisioning(account.PeerPubkey, &payout, nil, &account); reason == "" {
		t.Fatal("expected provisioning to be deferred inside the grace window")
	}

	account.CreatedAt = time.Now().Add(-2 * time.Minute)
	if reason := api.skipProvisioning(account.PeerPubkey, &payout, nil, &account); reason != "" {
		t.Fatalf("expected provisioning once the grace elapsed, got %q", reason)
	}
}

// ── /lightning_receive: canonical USDT in, LNUSDT out ────────────────────────
//
// The mirror image of the checkout: here the OUTBOUND leg is fixed (the receiver
// already signed the BOLT11) and the inbound one is open, so resolution runs the
// other way round. Clients omit rgb_invoice.asset_id and never learn the
// counterpart's contract id.

func receiveLNInvoice(assetID string, assetAmount int64) *node_client.DecodeLNInvoiceResponse {
	return &node_client.DecodeLNInvoiceResponse{
		AmtMsat:     3_000_000,
		AssetID:     assetID,
		AssetAmount: assetAmount,
		PayeePubkey: lightningAddressTestPeerPubkey,
	}
}

func TestResolveReceiveAssetPairResolvesTheCounterpartWhenAssetIDIsOmitted(t *testing.T) {
	api, _ := newConvertibleAssetTestAPI(t, newConvertibleAssetNode(t))

	pair, err := api.resolveReceiveAssetPair(context.Background(), &RGBInvoiceInput{}, receiveLNInvoice(payoutTestAssetID, 500_000))
	if err != nil {
		t.Fatalf("resolveReceiveAssetPair: %v", err)
	}
	if optionalAssetID(pair.InboundAssetID) != bridgeTestAssetID {
		t.Fatalf("expected the on-chain leg in canonical USDT, got %+v", pair.InboundAssetID)
	}
	if optionalAssetID(pair.OutboundAssetID) != payoutTestAssetID {
		t.Fatalf("expected the lightning leg in the payout asset, got %+v", pair.OutboundAssetID)
	}
	if !pair.Converted {
		t.Fatal("expected the pair to be marked converted")
	}
}

func TestResolveReceiveAssetPairKeepsTheSameAssetWhenNoPairIsDeclared(t *testing.T) {
	api, _ := newConvertibleAssetTestAPI(t, newConvertibleAssetNode(t))
	api.cfg.ConvertiblePairs = nil

	pair, err := api.resolveReceiveAssetPair(context.Background(), &RGBInvoiceInput{}, receiveLNInvoice(payoutTestAssetID, 500_000))
	if err != nil {
		t.Fatalf("resolveReceiveAssetPair: %v", err)
	}
	if optionalAssetID(pair.InboundAssetID) != payoutTestAssetID || pair.Converted {
		t.Fatalf("expected an unconverted same-asset receive, got %+v", pair)
	}
}

func TestResolveReceiveAssetPairRefusesToGuessBetweenTwoCounterparts(t *testing.T) {
	node := newConvertibleAssetNode(t)
	second := "rgb:second-USDT"
	node.metadata[second] = node_client.AssetMetadataResponse{AssetSchema: "Ifa", Name: "otherUSDT", Ticker: "OUSDT", Precision: 6}
	api, _ := newConvertibleAssetTestAPI(t, node)
	api.cfg.ConvertibleAssetIDs = append(api.cfg.ConvertibleAssetIDs, second)
	api.cfg.ConvertiblePairs = append(api.cfg.ConvertiblePairs, [2]string{payoutTestAssetID, second})

	_, err := api.resolveReceiveAssetPair(context.Background(), &RGBInvoiceInput{}, receiveLNInvoice(payoutTestAssetID, 500_000))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected an ambiguity error naming both counterparts, got %v", err)
	}
	if !strings.Contains(err.Error(), bridgeTestAssetID) || !strings.Contains(err.Error(), second) {
		t.Fatalf("expected both candidates in the error, got %v", err)
	}
}

func TestResolveReceiveAssetPairHonoursAnExplicitAssetID(t *testing.T) {
	api, _ := newConvertibleAssetTestAPI(t, newConvertibleAssetNode(t))
	payout := payoutTestAssetID

	pair, err := api.resolveReceiveAssetPair(context.Background(), &RGBInvoiceInput{AssetID: &payout}, receiveLNInvoice(payoutTestAssetID, 500_000))
	if err != nil {
		t.Fatalf("resolveReceiveAssetPair: %v", err)
	}
	if optionalAssetID(pair.InboundAssetID) != payoutTestAssetID || pair.Converted {
		t.Fatalf("an explicit asset_id must win over the counterpart, got %+v", pair)
	}
}

func TestResolveReceiveAssetPairRejectsAnUndeclaredPair(t *testing.T) {
	node := newConvertibleAssetNode(t)
	stranger := "rgb:stranger"
	node.metadata[stranger] = node_client.AssetMetadataResponse{AssetSchema: "Ifa", Name: "stranger", Ticker: "STR", Precision: 6}
	api, _ := newConvertibleAssetTestAPI(t, node)

	strangerID := stranger
	_, err := api.resolveReceiveAssetPair(context.Background(), &RGBInvoiceInput{AssetID: &strangerID}, receiveLNInvoice(payoutTestAssetID, 500_000))
	if err == nil {
		t.Fatal("expected an asset outside CONVERTIBLE_PAIRS to be refused")
	}
}

func TestResolveReceiveAssetPairRequiresAnAssetOnTheLNInvoice(t *testing.T) {
	api, _ := newConvertibleAssetTestAPI(t, newConvertibleAssetNode(t))

	if _, err := api.resolveReceiveAssetPair(context.Background(), &RGBInvoiceInput{}, receiveLNInvoice("", 0)); err == nil {
		t.Fatal("expected a BTC-only ln_invoice to be refused")
	}
}

// The amount is the only thing tying the two legs together once they are
// different contracts, so a converted receive must pin it.
func TestReceiveAssignmentPinsTheAmountOnlyWhenConverted(t *testing.T) {
	decoded := receiveLNInvoice(payoutTestAssetID, 500_000)

	converted, err := receiveAssignmentJSON(nil, invoiceAssetPair{Converted: true}, decoded)
	if err != nil {
		t.Fatalf("receiveAssignmentJSON: %v", err)
	}
	if converted["type"] != "Fungible" || converted["value"] != uint64(500_000) {
		t.Fatalf("expected the inbound amount pinned to the ln invoice, got %+v", converted)
	}

	same, err := receiveAssignmentJSON(nil, invoiceAssetPair{}, decoded)
	if err != nil {
		t.Fatalf("receiveAssignmentJSON: %v", err)
	}
	if same["type"] != "Any" {
		t.Fatalf("a same-asset receive must keep Any, got %+v", same)
	}
}

func TestReceiveAssignmentRejectsAConvertedReceiveWithoutAnAmount(t *testing.T) {
	if _, err := receiveAssignmentJSON(nil, invoiceAssetPair{Converted: true}, receiveLNInvoice(payoutTestAssetID, 0)); err == nil {
		t.Fatal("expected a converted receive with no asset_amount to be refused")
	}
}
