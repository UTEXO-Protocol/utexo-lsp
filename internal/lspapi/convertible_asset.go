package lspapi

// Cross-asset conversion on the LSP's books.
//
// APay signs the payer's invoice with the LSP's own key, so the LSP is the payee
// as well as the payer's only channel counterparty. That makes a node-level
// swap impossible: find_linked_asset_channel drops every channel whose
// counterparty is the recipient, leaving no candidates. What remains is what this
// file does — let the two legs of one APay payment carry different assets:
//
//	payer  --(canonical USDT)-->  LSP        inbound leg, quoted by the callback
//	LSP    --(payout asset)-->    receiver   outbound leg, the receiver's own asset
//
// Both legs stay ordinary single-hop payments and atomicity still comes from the
// shared payment hash.
//
// The two assets are independent RGB contracts: the Asset Link proof exists only
// in the wallet that ran link_ifa and never travels in a consignment, so it
// cannot authorize anything here. Convertibility is an operator statement —
// CONVERTIBLE_PAIRS — and the payer trusts the LSP for the outbound leg.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"utexo-lsp/pkg/node_client"
)

var errPairNotConvertible = errors.New("assets are not a convertible pair")

// invoiceAssetPair is the two legs of one APay payment. Outbound equals inbound
// unless the LSP converts, and both are nil for a plain BTC payment.
type invoiceAssetPair struct {
	InboundAssetID      *string
	InboundAssetAmount  *uint64
	OutboundAssetID     *string
	OutboundAssetAmount *uint64
	Converted           bool
}

type assetMetadataCacheEntry struct {
	meta node_client.AssetMetadataResponse
	at   time.Time
}

type assetMetadataCache struct {
	mu      sync.Mutex
	entries map[string]assetMetadataCacheEntry
}

// assetMetadata reads a contract's metadata off the LSP's own node, cached for
// GET_INFO_ASSETS_TTL. The entry expires rather than pins so a contract the node
// has only just learned (canonical USDT arrives by transfer) is picked up.
func (a *API) assetMetadata(ctx context.Context, assetID string) (node_client.AssetMetadataResponse, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return node_client.AssetMetadataResponse{}, errors.New("empty asset_id")
	}
	if a.lspClient == nil {
		return node_client.AssetMetadataResponse{}, errors.New("lsp client is not configured")
	}

	a.assetMeta.mu.Lock()
	entry, ok := a.assetMeta.entries[assetID]
	a.assetMeta.mu.Unlock()
	if ok && time.Since(entry.at) < a.cfg.GetInfoAssetsTTL {
		return entry.meta, nil
	}

	meta, err := a.lspClient.AssetMetadata(ctx, node_client.AssetMetadataRequest{AssetID: assetID})
	if err != nil {
		return node_client.AssetMetadataResponse{}, err
	}

	a.assetMeta.mu.Lock()
	if a.assetMeta.entries == nil {
		a.assetMeta.entries = make(map[string]assetMetadataCacheEntry)
	}
	a.assetMeta.entries[assetID] = assetMetadataCacheEntry{meta: meta, at: time.Now()}
	a.assetMeta.mu.Unlock()
	return meta, nil
}

func (a *API) assetInfo(ctx context.Context, assetID string) (SupportedAsset, error) {
	meta, err := a.assetMetadata(ctx, assetID)
	if err != nil {
		return SupportedAsset{}, err
	}
	return SupportedAsset{
		AssetID:   strings.TrimSpace(assetID),
		Schema:    meta.AssetSchema,
		Ticker:    meta.Ticker,
		Name:      meta.Name,
		Precision: meta.Precision,
	}, nil
}

// resolveInvoiceAssetPair turns the payer's request into the two legs. The outbound
// leg is not negotiable — the receiver is paid in the asset of its own channel — so
// only the inbound leg varies, and the payer's wallet chose it in the callback
// query: the LNURL callback is unauthenticated, so the LSP cannot tell whose
// channels to look at and must not make that choice itself.
func (a *API) resolveInvoiceAssetPair(ctx context.Context, account LightningAddressAccount, assetID *string, assetAmount *uint64) (invoiceAssetPair, error) {
	pair := invoiceAssetPair{
		InboundAssetID:      assetID,
		InboundAssetAmount:  assetAmount,
		OutboundAssetID:     assetID,
		OutboundAssetAmount: assetAmount,
	}
	if assetID == nil {
		// BTC on both legs.
		return pair, nil
	}
	inbound := strings.TrimSpace(*assetID)
	if inbound == "" {
		return invoiceAssetPair{}, errors.New("asset_id is required")
	}

	payout, err := a.payoutAssetID(ctx, account)
	if err != nil {
		return invoiceAssetPair{}, err
	}

	switch {
	case payout == "":
		// Nothing to convert to yet, but the asset still has to be one this LSP
		// delivers: otherwise the payer gets a HODL invoice in an asset the receiver
		// can never be paid in, discovered only once the money is already here.
		if err := a.ensureAssetPayoutEligible(inbound); err != nil {
			return invoiceAssetPair{}, err
		}
	case inbound == payout:
		// The common path: one asset, no conversion.
	default:
		if err := a.ensureConvertiblePair(ctx, inbound, payout); err != nil {
			return invoiceAssetPair{}, fmt.Errorf("cannot quote %s for an address paid out in %s: %w", inbound, payout, err)
		}
		outbound := payout
		pair.OutboundAssetID = &outbound
		pair.OutboundAssetAmount = assetAmount // 1:1, no spread
		pair.Converted = true
	}

	return pair, nil
}

// resolveReceiveAssetPair does for /lightning_receive what resolveInvoiceAssetPair
// does for the LNURL callback, with the roles of the two legs reversed:
//
//	sender --(canonical USDT, on-chain)--> LSP        inbound leg, the RGB invoice
//	LSP    --(LNUSDT, lightning)-------->  receiver   outbound leg, the BOLT11
//
// The outbound leg is fixed by the BOLT11 the receiver already signed, so only
// the inbound one is open. rgb_invoice.asset_id names it; omitting it asks the
// LSP to resolve it, which it can do because CONVERTIBLE_PAIRS is its own
// configuration — clients never need to know the counterpart's contract id.
//
// Resolution never guesses: one declared counterpart is taken, several are a 400
// naming them, none falls back to the outbound asset.
func (a *API) resolveReceiveAssetPair(ctx context.Context, params *RGBInvoiceInput, decodedLN *node_client.DecodeLNInvoiceResponse) (invoiceAssetPair, error) {
	if decodedLN == nil {
		return invoiceAssetPair{}, errors.New("cannot decode ln invoice")
	}
	outbound := strings.TrimSpace(decodedLN.AssetID)
	if outbound == "" {
		// A plain BTC invoice: there is no asset leg to bridge, and asking for one
		// would leave the sender's RGB stranded with nothing to deliver it in.
		if params != nil && optionalAssetID(params.AssetID) != "" {
			return invoiceAssetPair{}, errors.New("ln_invoice carries no asset, so rgb_invoice.asset_id cannot be honoured")
		}
		return invoiceAssetPair{}, errors.New("ln_invoice must carry an rgb asset for a lightning receive")
	}

	inbound := ""
	if params != nil {
		inbound = optionalAssetID(params.AssetID)
	}
	if inbound == "" {
		switch counterparts := a.convertibleCounterparts(outbound); len(counterparts) {
		case 0:
			inbound = outbound
		case 1:
			inbound = counterparts[0]
		default:
			return invoiceAssetPair{}, fmt.Errorf(
				"rgb_invoice.asset_id is ambiguous: %s is convertible with %s — name one",
				outbound, strings.Join(counterparts, ", "))
		}
	}

	pair := invoiceAssetPair{
		InboundAssetID:  &inbound,
		OutboundAssetID: &outbound,
	}
	if decodedLN.AssetAmount > 0 {
		amount := uint64(decodedLN.AssetAmount)
		pair.InboundAssetAmount = &amount
		pair.OutboundAssetAmount = &amount // 1:1, no spread
	}

	if inbound == outbound {
		if err := a.ensureAssetSupported(inbound); err != nil {
			return invoiceAssetPair{}, err
		}
		return pair, nil
	}
	if err := a.ensureConvertiblePair(ctx, inbound, outbound); err != nil {
		return invoiceAssetPair{}, fmt.Errorf("cannot accept %s for a receive delivered in %s: %w", inbound, outbound, err)
	}
	pair.Converted = true
	return pair, nil
}

// receiveAssignmentJSON builds the assignment for the RGB invoice the LSP issues.
//
// Same-asset receives keep whatever applyAndValidateRGBAssignment settled on. A
// converted receive pins the amount instead: the legs are unrelated contracts, so
// Any would let the sender deliver any quantity while the LSP pays the BOLT11 in
// full out of its own inventory.
func receiveAssignmentJSON(assignment *string, legs invoiceAssetPair, decodedLN *node_client.DecodeLNInvoiceResponse) (map[string]any, error) {
	if !legs.Converted {
		return rgbAssignmentJSON(assignment)
	}
	if decodedLN == nil || decodedLN.AssetAmount <= 0 {
		return nil, errors.New("ln_invoice must carry an asset_amount for a converted receive")
	}
	return map[string]any{"type": "Fungible", "value": uint64(decodedLN.AssetAmount)}, nil
}

// payoutAssetID returns the asset this address is paid out in, or "" when it
// cannot be established yet. It pins the value on first sighting so a later
// channel close cannot change what an existing address means.
func (a *API) payoutAssetID(ctx context.Context, account LightningAddressAccount) (string, error) {
	if account.PayoutAssetID != nil {
		if pinned := strings.TrimSpace(*account.PayoutAssetID); pinned != "" {
			return pinned, nil
		}
	}

	assetID, err := a.payoutAssetFromChannels(ctx, account.PeerPubkey)
	if err != nil || assetID == "" {
		return "", err
	}
	if err := a.db.SetLightningAddressPayoutAsset(ctx, account.PeerPubkey, assetID); err != nil {
		// Not fatal: the value is re-derivable from the channel on the next call.
		log.Printf("apay: persist payout asset %s for %s: %v", assetID, account.PeerPubkey, err)
	}
	return assetID, nil
}

// payoutAssetFromChannels derives it from the channels the LSP holds with the peer.
// Ambiguity reports "unknown" rather than guessing: quoting the wrong asset leaves
// the payer's HODL standing against an invoice the receiver cannot sign.
func (a *API) payoutAssetFromChannels(ctx context.Context, peerPubkey string) (string, error) {
	if a.lspClient == nil {
		return "", nil
	}
	peerPubkey = normalizePeerPubkey(peerPubkey)
	if peerPubkey == "" {
		return "", nil
	}

	chans, err := a.lspClient.ListChannels(ctx)
	if err != nil {
		return "", wrapErr("/listchannels", err)
	}
	return a.payoutAssetFromChannelList(peerPubkey, chans.Channels), nil
}

func (a *API) payoutAssetFromChannelList(peerPubkey string, channels []node_client.Channel) string {
	peerPubkey = normalizePeerPubkey(peerPubkey)
	seen := make([]string, 0, 2)
	for _, c := range channels {
		if normalizePeerPubkey(c.PeerPubkey) != peerPubkey || c.AssetID == nil {
			continue
		}
		id := strings.TrimSpace(*c.AssetID)
		// A BTC channel says nothing about the payout asset, and an asset this
		// LSP will not deliver is not a payout asset either. Convertible assets
		// count: the peer funded that channel itself.
		if id == "" || !a.isPayoutEligibleAsset(id) || slices.Contains(seen, id) {
			continue
		}
		seen = append(seen, id)
	}
	switch len(seen) {
	case 1:
		return seen[0]
	case 0:
		return ""
	default:
		// Two candidates is the normal state once a peer funds its own channel
		// in a convertible asset while the cron opens it one in the served
		// asset. PAYOUT_ASSET_PREFERENCE decides; without it, refuse to guess.
		for _, preferred := range a.cfg.PayoutAssetPreference {
			if slices.Contains(seen, preferred) {
				return preferred
			}
		}
		log.Printf("apay: peer %s holds channels in %d payout-eligible assets (%s) and none matches PAYOUT_ASSET_PREFERENCE — payout asset is ambiguous, not pinning one",
			peerPubkey, len(seen), strings.Join(seen, ", "))
		return ""
	}
}

// ensureConvertiblePair is the entire authorization for a 1:1 conversion. The
// payout-eligibility checks are not redundant with the caller: the conversion
// branch of resolveInvoiceAssetPair is the one path that never runs them itself,
// so without them any asset at all could be quoted.
func (a *API) ensureConvertiblePair(ctx context.Context, inbound, outbound string) error {
	if err := a.ensureAssetPayoutEligible(inbound); err != nil {
		return err
	}
	if err := a.ensureAssetPayoutEligible(outbound); err != nil {
		return err
	}
	if !a.isConvertiblePair(inbound, outbound) {
		return fmt.Errorf("%w: %s|%s is not in CONVERTIBLE_PAIRS", errPairNotConvertible, inbound, outbound)
	}

	inMeta, err := a.assetMetadata(ctx, inbound)
	if err != nil {
		return fmt.Errorf("asset metadata for %s: %w", inbound, err)
	}
	outMeta, err := a.assetMetadata(ctx, outbound)
	if err != nil {
		return fmt.Errorf("asset metadata for %s: %w", outbound, err)
	}
	// The rate is 1:1 in base units, the only unit these APIs speak, so differing
	// precisions would silently move the decimal point.
	if inMeta.Precision != outMeta.Precision {
		return fmt.Errorf("%w at 1:1: precision %d vs %d", errPairNotConvertible, inMeta.Precision, outMeta.Precision)
	}
	return nil
}

// isConvertiblePair reports whether the operator declared these two assets
// interchangeable. Order does not matter: the same pair serves a checkout in one
// direction and a refund in the other.
func (a *API) isConvertiblePair(inbound, outbound string) bool {
	inbound, outbound = strings.TrimSpace(inbound), strings.TrimSpace(outbound)
	if inbound == "" || outbound == "" || inbound == outbound {
		return false
	}
	for _, pair := range a.cfg.ConvertiblePairs {
		if (pair[0] == inbound && pair[1] == outbound) || (pair[0] == outbound && pair[1] == inbound) {
			return true
		}
	}
	return false
}

// convertibleCounterparts lists every asset declared convertible with this one.
func (a *API) convertibleCounterparts(assetID string) []string {
	assetID = strings.TrimSpace(assetID)
	out := make([]string, 0, len(a.cfg.ConvertiblePairs))
	for _, pair := range a.cfg.ConvertiblePairs {
		var other string
		switch assetID {
		case pair[0]:
			other = pair[1]
		case pair[1]:
			other = pair[0]
		default:
			continue
		}
		if other != "" && other != assetID && !slices.Contains(out, other) {
			out = append(out, other)
		}
	}
	return out
}

// acceptedAssets is what discovery advertises: the payout asset first, then every
// asset the callback would convert to it. Each candidate runs the same check the
// callback will, so discovery cannot promise a quote the callback then refuses.
func (a *API) acceptedAssets(ctx context.Context, payoutAssetID string) []SupportedAsset {
	payout, err := a.assetInfo(ctx, payoutAssetID)
	if err != nil {
		log.Printf("apay: asset info for payout asset %s: %v", payoutAssetID, err)
		return nil
	}
	accepted := []SupportedAsset{payout}

	for _, id := range a.convertibleCounterparts(payout.AssetID) {
		if err := a.ensureConvertiblePair(ctx, id, payout.AssetID); err != nil {
			log.Printf("apay: not advertising %s alongside %s: %v", id, payout.AssetID, err)
			continue
		}
		info, err := a.assetInfo(ctx, id)
		if err != nil {
			log.Printf("apay: asset info for convertible asset %s: %v", id, err)
			continue
		}
		accepted = append(accepted, info)
	}
	return accepted
}

func optionalAssetID(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}
