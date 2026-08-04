package lspapi

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestBuildGetInfoPublishesPolicy(t *testing.T) {
	a := &API{cfg: Config{
		MinAmtMsat:                      1000,
		DefaultChannelCapacitySat:       200000,
		PeerInFlightPercent:             10,
		DefaultChannelPushMsat:          5_000_000,
		DefaultChannelAssetAmount:       1_000_000,
		DefaultVirtualOpenMode:          "trusted_no_broadcast",
		LightningAddressMinSendableMsat: 1000,
		LightningAddressMaxSendableMsat: 3_000_000,
		LSPNodeHost:                     "lsp-signet.utexo.com",
		LSPNodePort:                     9735,
	}}

	got := a.buildGetInfo(nodeIdentity{pubkey: "0312c36f", network: "signet"}, nil)

	if got.APIVersion != 1 {
		t.Fatalf("api_version = %d, want 1", got.APIVersion)
	}
	if got.Pubkey != "0312c36f" || got.Network != "signet" {
		t.Fatalf("identity = %q/%q", got.Pubkey, got.Network)
	}
	if got.Host != "lsp-signet.utexo.com" || got.Port != 9735 {
		t.Fatalf("address = %q:%d", got.Host, got.Port)
	}
	if got.MinPaymentSizeMsat != "1000" {
		t.Fatalf("min_payment_size_msat = %q, want 1000", got.MinPaymentSizeMsat)
	}
	// 200_000 sat * 1000 * 10%
	if got.MaxPaymentSizeMsat != "20000000" {
		t.Fatalf("max_payment_size_msat = %q, want 20000000", got.MaxPaymentSizeMsat)
	}
	if got.MinChannelBalanceSat != got.MaxChannelBalanceSat || got.MinChannelBalanceSat != "200000" {
		t.Fatalf("channel balance = %q/%q", got.MinChannelBalanceSat, got.MaxChannelBalanceSat)
	}
	if got.MinChannelAssetAmount != "1000000" || got.MaxChannelAssetAmount != "1000000" {
		t.Fatalf("channel asset amount = %q/%q", got.MinChannelAssetAmount, got.MaxChannelAssetAmount)
	}
	if got.VirtualChannelMode != "trusted_no_broadcast" {
		t.Fatalf("virtual_channel_mode = %q", got.VirtualChannelMode)
	}
}

// Every field the old passthrough leaked must be absent from the wire format —
// xpubs above all, which cannot be rotated once published.
func TestGetInfoOmitsNodeState(t *testing.T) {
	a := &API{cfg: Config{}}
	raw, err := json.Marshal(a.buildGetInfo(nodeIdentity{pubkey: "02aa", network: "signet"}, nil))
	if err != nil {
		t.Fatal(err)
	}

	for _, leaked := range []string{
		"account_xpub_vanilla", "account_xpub_colored",
		"local_balance_sat", "pending_outbound_payments_sat", "eventual_close_fees_sat",
		"num_channels", "num_usable_channels", "num_peers",
		"network_nodes", "network_channels", "max_media_upload_size_mb",
		"rgb_htlc_min_msat", "channel_asset_max_amount",
	} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("get_info leaks %q: %s", leaked, raw)
		}
	}
}

// u64 values must be strings: JSON numbers above 2^53 are silently corrupted by
// JS clients, which is how channel_asset_max_amount broke.
func TestGetInfoAmountsAreStrings(t *testing.T) {
	a := &API{cfg: Config{DefaultChannelAssetAmount: math.MaxUint64}}
	raw, err := json.Marshal(a.buildGetInfo(nodeIdentity{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"max_channel_asset_amount":"18446744073709551615"`) {
		t.Fatalf("u64 not round-tripped as a string: %s", raw)
	}
}

func TestGetInfoAddressOmittedWhenUnset(t *testing.T) {
	a := &API{cfg: Config{}}
	raw, err := json.Marshal(a.buildGetInfo(nodeIdentity{pubkey: "02aa"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"host"`) || strings.Contains(string(raw), `"port"`) {
		t.Fatalf("address should be omitted when unconfigured: %s", raw)
	}
}

func TestGetInfoSupportedAssetsSerializeAsArray(t *testing.T) {
	a := &API{cfg: Config{}}
	raw, err := json.Marshal(a.buildGetInfo(nodeIdentity{}, []SupportedAsset{{
		AssetID: "rgb:aaa", Schema: "Ifa", Ticker: "UTIF", Name: "UTEXO Test IFA", Precision: 8,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schema":"Ifa"`) {
		t.Fatalf("asset schema missing: %s", raw)
	}

	empty, err := json.Marshal(a.buildGetInfo(nodeIdentity{}, []SupportedAsset{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"supported_assets":[]`) {
		t.Fatalf("empty asset list must be [] not null: %s", empty)
	}
}

func TestValidateNodeAddress(t *testing.T) {
	valid := []struct {
		host string
		port int
	}{
		{"", 0},
		{"lsp-signet.utexo.com", 9735},
		{"1.2.3.4", 9736},
		{"2001:db8::1", 9735}, // bare IPv6 needs no brackets once the port is its own field
	}
	for _, tc := range valid {
		if err := validateNodeAddress(tc.host, tc.port); err != nil {
			t.Fatalf("validateNodeAddress(%q, %d) = %v, want nil", tc.host, tc.port, err)
		}
	}

	invalid := []struct {
		host string
		port int
		why  string
	}{
		{"lsp-signet.utexo.com", 0, "host without port"},
		{"", 9735, "port without host"},
		{"lsp-signet.utexo.com:9735", 9735, "port in the host variable"},
		{"http://lsp-signet.utexo.com", 9735, "URL instead of host"},
		{"lsp-signet.utexo.com", 70000, "port out of range"},
	}
	for _, tc := range invalid {
		if err := validateNodeAddress(tc.host, tc.port); err == nil {
			t.Fatalf("validateNodeAddress(%q, %d) = nil, want error (%s)", tc.host, tc.port, tc.why)
		}
	}
}
