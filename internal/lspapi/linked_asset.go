package lspapi

// Linked-asset conversion on the LSP's books.
//
// APay signs the payer's invoice with the LSP's own key, so the LSP is the payee
// as well as the payer's only channel counterparty. That makes a node-level
// linked-asset swap impossible: find_linked_asset_channel drops every channel
// whose counterparty is the recipient, leaving no candidates. What remains is what
// this file does — let the two legs of one APay payment carry different assets:
//
//	payer  --(child asset)-->  LSP        inbound leg, quoted by the callback
//	LSP    --(parent asset)->  receiver   outbound leg, always the payout asset
//
// Both legs stay ordinary single-hop payments and atomicity still comes from the
// shared payment hash. The Asset Link is not on the payment path at all: it is the
// LSP's authorization to treat the pair as interchangeable at 1:1, so the payer
// trusts the LSP for the amount of the outbound leg.

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

var errAssetsNotLinked = errors.New("assets are not linked")

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
// GET_INFO_ASSETS_TTL: genesis never changes, but a link can be established after
// we first looked, so the entry expires rather than pins.
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
		// serves: otherwise the payer gets a HODL invoice in an asset the receiver
		// can never be paid in, discovered only once the money is already here.
		if err := a.ensureAssetSupported(inbound); err != nil {
			return invoiceAssetPair{}, err
		}
	case inbound == payout:
		// The common path: one asset, no conversion.
	default:
		if err := a.verifyLinkedPair(ctx, inbound, payout); err != nil {
			return invoiceAssetPair{}, fmt.Errorf("cannot quote %s for an address paid out in %s: %w", inbound, payout, err)
		}
		outbound := payout
		pair.OutboundAssetID = &outbound
		pair.OutboundAssetAmount = assetAmount // 1:1, no spread
		pair.Converted = true
	}

	return pair, nil
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
		// LSP does not serve is not one it can deliver either.
		if id == "" || !a.isSupportedAsset(&id) || slices.Contains(seen, id) {
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
		log.Printf("apay: peer %s holds channels in %d supported assets (%s) — payout asset is ambiguous, not pinning one",
			peerPubkey, len(seen), strings.Join(seen, ", "))
		return ""
	}
}

// verifyLinkedPair is the entire authorization for a 1:1 conversion, and is
// deliberately local. The child's linked_from_asset_id rides in its genesis and so
// travels to everyone, but linked_to_asset_id and the parent's Link transfer exist
// only in the wallet that ran /assetlink. Requiring all three means the LSP converts
// only pairs it linked itself, not any pair someone declared.
func (a *API) verifyLinkedPair(ctx context.Context, inbound, outbound string) error {
	inMeta, err := a.assetMetadata(ctx, inbound)
	if err != nil {
		return fmt.Errorf("asset metadata for %s: %w", inbound, err)
	}
	outMeta, err := a.assetMetadata(ctx, outbound)
	if err != nil {
		return fmt.Errorf("asset metadata for %s: %w", outbound, err)
	}

	var parent, child string
	var parentMeta node_client.AssetMetadataResponse
	switch {
	case optionalAssetID(inMeta.LinkedFromAssetID) == outbound:
		// Payer spends the child, receiver is paid the parent.
		parent, child, parentMeta = outbound, inbound, outMeta
	case optionalAssetID(outMeta.LinkedFromAssetID) == inbound:
		// Payer spends the parent, receiver is paid the child.
		parent, child, parentMeta = inbound, outbound, inMeta
	default:
		return fmt.Errorf("%w: neither declares the other as its parent", errAssetsNotLinked)
	}

	if optionalAssetID(parentMeta.LinkedToAssetID) != child {
		return fmt.Errorf("%w: %s does not link to %s in this wallet — the LSP is not the linker of this pair",
			errAssetsNotLinked, parent, child)
	}
	if err := a.requireSettledLinkTransfer(ctx, parent); err != nil {
		return err
	}
	// The rate is 1:1 in base units, the only unit these APIs speak, so differing
	// precisions would silently move the decimal point.
	if inMeta.Precision != outMeta.Precision {
		return fmt.Errorf("%w at 1:1: precision %d vs %d", errAssetsNotLinked, inMeta.Precision, outMeta.Precision)
	}
	return nil
}

func (a *API) requireSettledLinkTransfer(ctx context.Context, parentAssetID string) error {
	if a.lspClient == nil {
		return errors.New("lsp client is not configured")
	}
	transfers, err := a.lspClient.ListTransfers(ctx, node_client.ListTransfersRequest{AssetID: parentAssetID})
	if err != nil {
		return wrapErr("/listtransfers", err)
	}
	for _, t := range transfers.Transfers {
		if t.Kind == node_client.TransferKindLink && t.Status == node_client.TransferStatusSettled {
			return nil
		}
	}
	return fmt.Errorf("%w: %s has no settled Link transfer in this wallet", errAssetsNotLinked, parentAssetID)
}

// acceptedAssets is what discovery advertises: the payout asset first, then every
// asset the callback would convert to it — at most the linked counterpart, since
// conversion is 1:1 between a single linked pair.
func (a *API) acceptedAssets(ctx context.Context, payoutAssetID string) []SupportedAsset {
	payout, err := a.assetInfo(ctx, payoutAssetID)
	if err != nil {
		log.Printf("apay: asset info for payout asset %s: %v", payoutAssetID, err)
		return nil
	}
	accepted := []SupportedAsset{payout}

	meta, err := a.assetMetadata(ctx, payoutAssetID)
	if err != nil {
		return accepted
	}
	for _, candidate := range []*string{meta.LinkedFromAssetID, meta.LinkedToAssetID} {
		id := optionalAssetID(candidate)
		if id == "" || id == payout.AssetID {
			continue
		}
		if err := a.verifyLinkedPair(ctx, id, payout.AssetID); err != nil {
			log.Printf("apay: not advertising %s alongside %s: %v", id, payout.AssetID, err)
			continue
		}
		info, err := a.assetInfo(ctx, id)
		if err != nil {
			log.Printf("apay: asset info for linked asset %s: %v", id, err)
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
