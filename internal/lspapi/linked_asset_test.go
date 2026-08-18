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

	"utexo-lsp/pkg/node_client"
)

const (
	linkedParentAssetID = "rgb:parent-LnUSDT"
	linkedChildAssetID  = "rgb:child-bridgeUSDT"
)

// linkedAssetNode is a stand-in for the LSP's own rgb-lightning-node: the peer
// holds one parent-asset channel, and the parent/child pair is linked with a
// settled Link transfer — i.e. the LSP is the linker, as verifyLinkedPair demands.
type linkedAssetNode struct {
	t *testing.T

	peerPubkey     string
	peerChannels   []node_client.Channel
	metadata       map[string]node_client.AssetMetadataResponse
	linkTransfers  map[string][]node_client.Transfer
	invoiceRequest map[string]any
}

func newLinkedAssetNode(t *testing.T) *linkedAssetNode {
	t.Helper()
	parent := linkedParentAssetID
	child := linkedChildAssetID
	return &linkedAssetNode{
		t:          t,
		peerPubkey: lightningAddressTestPeerPubkey,
		peerChannels: []node_client.Channel{
			{PeerPubkey: lightningAddressTestPeerPubkey, AssetID: &parent, IsUsable: true},
		},
		metadata: map[string]node_client.AssetMetadataResponse{
			parent: {AssetSchema: "Ifa", Name: "LnUSDT", Ticker: "LNUSDT", Precision: 6, LinkedToAssetID: &child},
			child:  {AssetSchema: "Ifa", Name: "bridgeUSDT", Ticker: "BRUSDT", Precision: 6, LinkedFromAssetID: &parent},
		},
		linkTransfers: map[string][]node_client.Transfer{
			parent: {{Idx: 1, Kind: node_client.TransferKindLink, Status: node_client.TransferStatusSettled}},
		},
	}
}

func (n *linkedAssetNode) RoundTrip(req *http.Request) (*http.Response, error) {
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
	case "/listtransfers":
		assetID, _ := body["asset_id"].(string)
		return jsonStubResponse(n.t, http.StatusOK, map[string]any{"transfers": n.linkTransfers[assetID]}, req), nil
	case "/lninvoice":
		n.invoiceRequest = body
		return jsonStubResponse(n.t, http.StatusOK, map[string]string{"invoice": "lnbc1hodlinvoice"}, req), nil
	default:
		n.t.Fatalf("unexpected request path: %s", req.URL.Path)
		return nil, nil
	}
}

func (n *linkedAssetNode) client() *node_client.Client {
	return node_client.NewClient("http://linked-asset-stub", "", &http.Client{Transport: n})
}

// newLinkedAssetTestAPI serves the parent asset, what the receiver's channel is in,
// so the child is only reachable through the link.
func newLinkedAssetTestAPI(t *testing.T, node *linkedAssetNode) (*API, LightningAddressAccount) {
	t.Helper()
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", node.client())
	api.cfg.SupportedAssetIDs = []string{linkedParentAssetID}
	return api, account
}

func TestResolveInvoiceAssetPairConvertsLinkedChildToPayoutAsset(t *testing.T) {
	node := newLinkedAssetNode(t)
	api, account := newLinkedAssetTestAPI(t, node)

	child := linkedChildAssetID
	amount := uint64(500_000)
	pair, err := api.resolveInvoiceAssetPair(context.Background(), account, &child, &amount)
	if err != nil {
		t.Fatalf("resolve asset pair: %v", err)
	}
	if !pair.Converted {
		t.Fatalf("expected a converted pair, got %+v", pair)
	}
	if optionalAssetID(pair.InboundAssetID) != linkedChildAssetID {
		t.Fatalf("inbound leg should stay the quoted asset, got %+v", pair.InboundAssetID)
	}
	if optionalAssetID(pair.OutboundAssetID) != linkedParentAssetID {
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
	if optionalAssetID(stored.PayoutAssetID) != linkedParentAssetID {
		t.Fatalf("expected payout asset to be persisted, got %+v", stored.PayoutAssetID)
	}
}

func TestResolveInvoiceAssetPairLeavesPayoutAssetUnconverted(t *testing.T) {
	node := newLinkedAssetNode(t)
	api, account := newLinkedAssetTestAPI(t, node)

	parent := linkedParentAssetID
	amount := uint64(500_000)
	pair, err := api.resolveInvoiceAssetPair(context.Background(), account, &parent, &amount)
	if err != nil {
		t.Fatalf("resolve asset pair: %v", err)
	}
	if pair.Converted {
		t.Fatalf("paying in the payout asset must not convert: %+v", pair)
	}
	if optionalAssetID(pair.OutboundAssetID) != linkedParentAssetID {
		t.Fatalf("unexpected outbound leg: %+v", pair.OutboundAssetID)
	}
}

func TestResolveInvoiceAssetPairRejectsUnlinkedAsset(t *testing.T) {
	node := newLinkedAssetNode(t)
	unlinked := "rgb:unrelated-asset"
	node.metadata[unlinked] = node_client.AssetMetadataResponse{AssetSchema: "Nia", Name: "Unrelated", Precision: 6}
	api, account := newLinkedAssetTestAPI(t, node)

	amount := uint64(500_000)
	if _, err := api.resolveInvoiceAssetPair(context.Background(), account, &unlinked, &amount); err == nil {
		t.Fatal("expected an unlinked asset to be rejected")
	}
}

// A pair whose link the LSP cannot prove locally is refused even though the
// child's genesis declares the parent: linked_from_asset_id travels to everyone,
// so on its own it authorizes nothing.
func TestResolveInvoiceAssetPairRejectsPairLinkedBySomeoneElse(t *testing.T) {
	node := newLinkedAssetNode(t)
	node.linkTransfers[linkedParentAssetID] = nil
	api, account := newLinkedAssetTestAPI(t, node)

	child := linkedChildAssetID
	amount := uint64(500_000)
	_, err := api.resolveInvoiceAssetPair(context.Background(), account, &child, &amount)
	if err == nil || !strings.Contains(err.Error(), "no settled Link transfer") {
		t.Fatalf("expected a missing-link-proof rejection, got %v", err)
	}
}

func TestResolveInvoiceAssetPairRejectsPrecisionMismatch(t *testing.T) {
	node := newLinkedAssetNode(t)
	child := node.metadata[linkedChildAssetID]
	child.Precision = 0
	node.metadata[linkedChildAssetID] = child
	api, account := newLinkedAssetTestAPI(t, node)

	childID := linkedChildAssetID
	amount := uint64(500_000)
	_, err := api.resolveInvoiceAssetPair(context.Background(), account, &childID, &amount)
	if err == nil || !strings.Contains(err.Error(), "precision") {
		t.Fatalf("expected a precision rejection, got %v", err)
	}
}

// The callback must decide before it reserves: a rejected pair may not spend a
// payment hash out of the receiver's pre-signed batch.
func TestLightningAddressCallbackRejectsUnlinkedAssetWithoutSpendingAHash(t *testing.T) {
	node := newLinkedAssetNode(t)
	unlinked := "rgb:unrelated-asset"
	node.metadata[unlinked] = node_client.AssetMetadataResponse{AssetSchema: "Nia", Name: "Unrelated", Precision: 6}
	api, account := newLinkedAssetTestAPI(t, node)
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+
		"?amount=3000000&asset_id="+url.QueryEscape(unlinked)+"&asset_amount=500000", nil)
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
	node := newLinkedAssetNode(t)
	api, account := newLinkedAssetTestAPI(t, node)
	seedAsyncOrderHashes(t, api, lightningAddressTestPeerPubkey, 1, 1)

	req := httptest.NewRequest(http.MethodGet, "/pay/callback/"+url.PathEscape(account.Username)+
		"?amount=3000000&asset_id="+url.QueryEscape(linkedChildAssetID)+"&asset_amount=500000", nil)
	rr := httptest.NewRecorder()
	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := node.invoiceRequest["asset_id"]; got != linkedChildAssetID {
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
	if inboundAsset != linkedChildAssetID || outboundAsset != linkedParentAssetID {
		t.Fatalf("unexpected legs: inbound=%s outbound=%s", inboundAsset, outboundAsset)
	}
	if inboundAmount != 500_000 || outboundAmount != 500_000 {
		t.Fatalf("expected 1:1 amounts, got inbound=%d outbound=%d", inboundAmount, outboundAmount)
	}
}

func TestLightningAddressDiscoveryAdvertisesPayoutAndLinkedAssets(t *testing.T) {
	node := newLinkedAssetNode(t)
	api, account := newLinkedAssetTestAPI(t, node)

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
	if resp.PayoutAsset == nil || resp.PayoutAsset.AssetID != linkedParentAssetID {
		t.Fatalf("expected the parent as payout asset, got %+v", resp.PayoutAsset)
	}
	if resp.PayoutAsset.Precision != 6 || resp.PayoutAsset.Ticker != "LNUSDT" {
		t.Fatalf("payout asset should carry display metadata, got %+v", resp.PayoutAsset)
	}
	if len(resp.AcceptedAssets) != 2 || resp.AcceptedAssets[1].AssetID != linkedChildAssetID {
		t.Fatalf("expected the linked child to be advertised, got %+v", resp.AcceptedAssets)
	}
}
