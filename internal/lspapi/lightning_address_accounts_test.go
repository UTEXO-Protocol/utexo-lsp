package lspapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const lightningAddressValidTestPeerPubkey = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func TestParseClientPubkey(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "valid compressed key",
			raw:  lightningAddressValidTestPeerPubkey,
			want: lightningAddressValidTestPeerPubkey,
		},
		{
			name: "valid compressed key with odd parity",
			raw:  "03" + lightningAddressValidTestPeerPubkey[2:],
			want: "03" + lightningAddressValidTestPeerPubkey[2:],
		},
		{
			name: "canonicalizes uppercase and whitespace",
			raw:  " \t" + strings.ToUpper(lightningAddressValidTestPeerPubkey) + "\n",
			want: lightningAddressValidTestPeerPubkey,
		},
		{
			name:    "rejects invalid hex",
			raw:     "02" + strings.Repeat("zz", 32),
			wantErr: true,
		},
		{
			name:    "rejects uncompressed key",
			raw:     "04" + strings.Repeat("00", 64),
			wantErr: true,
		},
		{
			name:    "rejects invalid compressed prefix",
			raw:     "04" + strings.Repeat("00", 32),
			wantErr: true,
		},
		{
			name:    "rejects invalid curve point",
			raw:     "02" + strings.Repeat("ff", 32),
			wantErr: true,
		},
		{
			name:    "rejects peer address suffix",
			raw:     lightningAddressValidTestPeerPubkey + "@127.0.0.1:9735",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseClientPubkey(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse client pubkey: %v", err)
			}
			if got != test.want {
				t.Fatalf("unexpected parsed pubkey: got %q want %q", got, test.want)
			}
		})
	}
}

func TestLightningAddressAccountMintedOnceAndPersisted(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	api := &API{db: store}

	peerPubkey := "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	account1, err := api.ensureLightningAddressAccount(context.Background(), peerPubkey)
	if err != nil {
		t.Fatalf("ensure first account: %v", err)
	}
	account2, err := api.ensureLightningAddressAccount(context.Background(), peerPubkey)
	if err != nil {
		t.Fatalf("ensure second account: %v", err)
	}

	if account1.Username == "" {
		t.Fatalf("expected minted account handle, got %+v", account1)
	}
	if account1.Username != account2.Username {
		t.Fatalf("expected persisted handle after first insert, got %+v and %+v", account1, account2)
	}

	gotByPeer, err := store.GetLightningAddressAccountByPeerPubkey(context.Background(), strings.ToLower(peerPubkey))
	if err != nil {
		t.Fatalf("lookup by peer pubkey: %v", err)
	}
	if gotByPeer.Username != account1.Username {
		t.Fatalf("unexpected stored account: %+v vs %+v", gotByPeer, account1)
	}
}

func TestLightningAddressDiscoveryUsesDbBackedAccount(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	api := &API{
		cfg: Config{
			LightningAddressDomainURL:        "https://example.com",
			LightningAddressShortDescription: "Payment to example",
			LightningAddressMinSendableMsat:  1_000,
			LightningAddressMaxSendableMsat:  5_000,
		},
		db: store,
	}

	peerPubkey := "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	account, err := api.ensureLightningAddressAccount(context.Background(), peerPubkey)
	if err != nil {
		t.Fatalf("ensure account: %v", err)
	}

	gotByPeer, err := store.GetLightningAddressAccountByPeerPubkey(context.Background(), strings.ToLower(peerPubkey))
	if err != nil {
		t.Fatalf("lookup by peer pubkey: %v", err)
	}
	if gotByPeer.Username != account.Username {
		t.Fatalf("unexpected stored account: %+v vs %+v", gotByPeer, account)
	}

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

	expectedCallback := "https://example.com/pay/callback/" + url.PathEscape(account.Username)
	if resp.Callback != expectedCallback {
		t.Fatalf("unexpected callback: %s", resp.Callback)
	}

	expectedMetadata := `[["text/identifier","` + account.Username + `@example.com"],["text/plain","Payment to example"]]`
	if resp.Metadata != expectedMetadata {
		t.Fatalf("unexpected metadata: %s", resp.Metadata)
	}
}

func TestLightningAddressDiscoveryRejectsSuffix(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username+"+tips", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}
