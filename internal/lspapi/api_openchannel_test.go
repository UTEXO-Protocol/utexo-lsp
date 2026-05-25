package lspapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenChannelPayloadAddsDefaultVirtualModeToRequest(t *testing.T) {
	mode := "outbound"
	a := &API{
		cfg: Config{
			DefaultVirtualOpenMode: mode,
		},
	}

	payload, err := a.openChannelPayload(Connection{
		PeerPubkeyAndOptAddr: "02abc@127.0.0.1:9735",
		CapacitySat:          200000,
		PushMsat:             0,
		Public:               false,
		WithAnchors:          true,
	})
	require.NoError(t, err)

	req, ok := payload.(OpenChannelRequest)
	require.True(t, ok)
	require.NotNil(t, req.VirtualOpenMode)
	require.Equal(t, mode, *req.VirtualOpenMode)
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
	require.NoError(t, err)

	payload, err := a.openChannelPayload(Connection{OpenChannelParams: raw})
	require.NoError(t, err)

	m, ok := payload.(map[string]any)
	require.True(t, ok)
	got, ok := m["virtual_open_mode"].(string)
	require.True(t, ok)
	require.Equal(t, mode, got)
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
	require.NoError(t, err)

	payload, err := a.openChannelPayload(Connection{OpenChannelParams: raw})
	require.NoError(t, err)

	m, ok := payload.(map[string]any)
	require.True(t, ok)
	got, ok := m["virtual_open_mode"].(string)
	require.True(t, ok)
	require.Equal(t, "inbound", got)
}

func TestOpenChannelPayloadAddsDefaultAssetAmountForRGBRequest(t *testing.T) {
	a := &API{
		cfg: Config{
			DefaultChannelAssetAmount: 7,
		},
	}

	assetID := "rgb:asset"
	payload, err := a.openChannelPayload(Connection{
		PeerPubkeyAndOptAddr: "02abc@127.0.0.1:9735",
		CapacitySat:          200000,
		PushMsat:             0,
		AssetID:              &assetID,
		Public:               false,
		WithAnchors:          true,
	})
	require.NoError(t, err)

	req, ok := payload.(OpenChannelRequest)
	require.True(t, ok)
	require.NotNil(t, req.AssetAmount)
	require.EqualValues(t, 7, *req.AssetAmount)
}
