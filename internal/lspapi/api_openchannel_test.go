package lspapi

import (
	"encoding/json"
	"strings"
	"testing"

	"utexo-lsp/pkg/node_client"
)

func TestOpenChannelPayloadAddsDefaultVirtualModeToRequest(t *testing.T) {
	mode := "outbound"
	a := &API{
		cfg: Config{
			DefaultVirtualOpenMode: mode,
		},
	}

	payload, err := openChannelPayload(a, node_client.Connection{
		PeerPubkeyAndOptAddr: "02abc@127.0.0.1:9735",
		CapacitySat:          200000,
		PushMsat:             0,
		Public:               false,
		WithAnchors:          true,
	})
	if err != nil {
		t.Fatalf("openChannelPayload failed: %v", err)
	}

	req, ok := payload.(node_client.OpenChannelRequest)
	if !ok {
		t.Fatalf("expected node_client.OpenChannelRequest, got %T", payload)
	}
	if req.VirtualOpenMode == nil || *req.VirtualOpenMode != mode {
		t.Fatalf("expected virtual mode %q, got %v", mode, req.VirtualOpenMode)
	}
}

func TestOpenChannelPayloadInjectsDefaultVirtualModeIntoMapPayload(t *testing.T) {
	mode := "outbound"
	a := &API{
		cfg: Config{
			DefaultVirtualOpenMode: mode,
		},
	}

	params := map[string]any{
		"peer_pubkey_and_opt_addr": "02abc@127.0.0.1:9735",
		"capacity_sat":             200000,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	payload, err := openChannelPayload(a, node_client.Connection{OpenChannelParams: raw})
	if err != nil {
		t.Fatalf("openChannelPayload failed: %v", err)
	}

	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %T", payload)
	}
	if got, ok := m["virtual_open_mode"].(string); !ok || got != mode {
		t.Fatalf("expected virtual_open_mode=%q, got %#v", mode, m["virtual_open_mode"])
	}
}

func TestOpenChannelPayloadPreservesExplicitVirtualModeInMapPayload(t *testing.T) {
	a := &API{
		cfg: Config{
			DefaultVirtualOpenMode: "outbound",
		},
	}

	params := map[string]any{
		"peer_pubkey_and_opt_addr": "02abc@127.0.0.1:9735",
		"capacity_sat":             200000,
		"virtual_open_mode":        "inbound",
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	payload, err := openChannelPayload(a, node_client.Connection{OpenChannelParams: raw})
	if err != nil {
		t.Fatalf("openChannelPayload failed: %v", err)
	}

	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %T", payload)
	}
	if got, ok := m["virtual_open_mode"].(string); !ok || got != "inbound" {
		t.Fatalf("expected explicit virtual_open_mode to remain \"inbound\", got %#v", m["virtual_open_mode"])
	}
}

func TestOpenChannelPayloadAddsDefaultAssetAmountForRGBRequest(t *testing.T) {
	a := &API{
		cfg: Config{
			DefaultChannelAssetAmount: 7,
		},
	}

	assetID := "rgb:asset"
	payload, err := openChannelPayload(a, node_client.Connection{
		PeerPubkeyAndOptAddr: "02abc@127.0.0.1:9735",
		CapacitySat:          200000,
		PushMsat:             0,
		AssetID:              &assetID,
		Public:               false,
		WithAnchors:          true,
	})
	if err != nil {
		t.Fatalf("openChannelPayload failed: %v", err)
	}

	req, ok := payload.(node_client.OpenChannelRequest)
	if !ok {
		t.Fatalf("expected node_client.OpenChannelRequest, got %T", payload)
	}
	if req.AssetAmount == nil || *req.AssetAmount != 7 {
		t.Fatalf("expected asset_amount=7, got %#v", req.AssetAmount)
	}
}

func openChannelPayload(a *API, c node_client.Connection) (any, error) {
	req, err := a.openChannelRequest(c)
	if err != nil {
		return nil, err
	}
	if len(c.OpenChannelParams) == 0 {
		return req, nil
	}

	payload := map[string]any{}
	if err := json.Unmarshal(c.OpenChannelParams, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.PeerPubkeyAndOptAddr) != "" {
		payload["peer_pubkey_and_opt_addr"] = req.PeerPubkeyAndOptAddr
	}
	if req.CapacitySat > 0 {
		payload["capacity_sat"] = req.CapacitySat
	}
	if req.PushMsat > 0 {
		payload["push_msat"] = req.PushMsat
	}
	if req.AssetID != nil {
		payload["asset_id"] = *req.AssetID
	}
	if req.AssetAmount != nil {
		payload["asset_amount"] = *req.AssetAmount
	}
	if req.PushAssetAmount != nil {
		payload["push_asset_amount"] = *req.PushAssetAmount
	}
	payload["public"] = req.Public
	payload["with_anchors"] = req.WithAnchors
	if req.VirtualOpenMode != nil {
		payload["virtual_open_mode"] = *req.VirtualOpenMode
	}
	return payload, nil
}
