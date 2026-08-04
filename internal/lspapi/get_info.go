package lspapi

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"utexo-lsp/pkg/node_client"
)

const getInfoAPIVersion = 1

type nodeIdentity struct {
	pubkey  string
	network string
}

type getInfoCache struct {
	mu sync.Mutex

	identity   nodeIdentity
	identityOK bool

	assets   []SupportedAsset
	assetsOK bool
	assetsAt time.Time
}

func (a *API) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	identity, err := a.nodeIdentity(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "node identity unavailable: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, a.buildGetInfo(identity, a.supportedAssets(ctx)))
}

func (a *API) buildGetInfo(identity nodeIdentity, assets []SupportedAsset) GetInfoResponse {
	return GetInfoResponse{
		APIVersion:      getInfoAPIVersion,
		Pubkey:          identity.pubkey,
		Network:         identity.network,
		Host:            a.cfg.LSPNodeHost,
		Port:            a.cfg.LSPNodePort,
		SupportedAssets: assets,

		MinPaymentSizeMsat: u64String(a.cfg.MinAmtMsat),
		MaxPaymentSizeMsat: u64String(a.cfg.maxDeliverableMsat()),

		// min == max while the LSP offers no choice; the pair keeps a future
		// negotiable range additive.
		MinChannelBalanceSat:        u64String(a.cfg.DefaultChannelCapacitySat),
		MaxChannelBalanceSat:        u64String(a.cfg.DefaultChannelCapacitySat),
		MinInitialClientBalanceMsat: u64String(a.cfg.DefaultChannelPushMsat),
		MaxInitialClientBalanceMsat: u64String(a.cfg.DefaultChannelPushMsat),
		MinChannelAssetAmount:       u64String(a.cfg.DefaultChannelAssetAmount),
		MaxChannelAssetAmount:       u64String(a.cfg.DefaultChannelAssetAmount),

		VirtualChannelMode: a.cfg.DefaultVirtualOpenMode,

		LightningAddressMinSendableMsat: u64String(a.cfg.LightningAddressMinSendableMsat),
		LightningAddressMaxSendableMsat: u64String(a.cfg.LightningAddressMaxSendableMsat),
	}
}

func u64String(v uint64) string { return strconv.FormatUint(v, 10) }

// warmGetInfoCache primes the caches at startup. Failure is not fatal: the
// node may still be locked.
func (a *API) warmGetInfoCache(ctx context.Context) {
	if _, err := a.nodeIdentity(ctx); err != nil {
		log.Printf("get_info: node identity not cached at startup: %v", err)
		return
	}
	a.supportedAssets(ctx)
}

// nodeIdentity is cached for the process lifetime: both values are fixed for a
// given node, and a different node means a restart.
func (a *API) nodeIdentity(ctx context.Context) (nodeIdentity, error) {
	a.info.mu.Lock()
	cached, ok := a.info.identity, a.info.identityOK
	a.info.mu.Unlock()
	if ok {
		return cached, nil
	}

	info, err := a.lspClient.NodeInfo(ctx)
	if err != nil {
		return nodeIdentity{}, err
	}
	network, err := a.lspClient.NetworkInfo(ctx)
	if err != nil {
		return nodeIdentity{}, err
	}

	identity := nodeIdentity{
		pubkey:  info.Pubkey,
		network: strings.ToLower(network.Network),
	}

	a.info.mu.Lock()
	a.info.identity, a.info.identityOK = identity, true
	a.info.mu.Unlock()
	return identity, nil
}

// supportedAssets resolves SUPPORTED_ASSET_IDS to metadata, cached for
// GET_INFO_ASSETS_TTL. A failed refresh serves the previous list rather than
// dropping assets a client may legitimately use.
func (a *API) supportedAssets(ctx context.Context) []SupportedAsset {
	a.info.mu.Lock()
	cached, fresh := a.info.assets, a.info.assetsOK && time.Since(a.info.assetsAt) < a.cfg.GetInfoAssetsTTL
	a.info.mu.Unlock()
	if fresh {
		return cached
	}

	assets := make([]SupportedAsset, 0, len(a.cfg.SupportedAssetIDs))
	partial := false
	for _, assetID := range a.cfg.SupportedAssetIDs {
		assetID = strings.TrimSpace(assetID)
		if assetID == "" {
			continue
		}
		meta, err := a.lspClient.AssetMetadata(ctx, node_client.AssetMetadataRequest{AssetID: assetID})
		if err != nil {
			log.Printf("get_info: asset metadata for %s failed: %v", assetID, err)
			partial = true
			continue
		}
		assets = append(assets, SupportedAsset{
			AssetID:   assetID,
			Schema:    meta.AssetSchema,
			Ticker:    meta.Ticker,
			Name:      meta.Name,
			Precision: meta.Precision,
		})
	}

	a.info.mu.Lock()
	defer a.info.mu.Unlock()
	if partial {
		if a.info.assetsOK {
			return a.info.assets
		}
		return assets
	}
	a.info.assets, a.info.assetsOK, a.info.assetsAt = assets, true, time.Now()
	return assets
}
