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

	"github.com/stretchr/testify/require"
)

func TestLightningAddressAccountMintedOnceAndPersisted(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})

	api := &API{db: store}

	peerPubkey := "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	account1, err := api.ensureLightningAddressAccount(context.Background(), peerPubkey)
	require.NoError(t, err)
	account2, err := api.ensureLightningAddressAccount(context.Background(), peerPubkey)
	require.NoError(t, err)

	require.NotEmpty(t, account1.Username)
	require.Equal(t, account1.Username, account2.Username)

	gotByPeer, err := store.GetLightningAddressAccountByPeerPubkey(context.Background(), strings.ToLower(peerPubkey))
	require.NoError(t, err)
	require.Equal(t, account1.Username, gotByPeer.Username)
}

func TestLightningAddressDiscoveryUsesDbBackedAccount(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	require.NoError(t, err)
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
	require.NoError(t, err)

	gotByPeer, err := store.GetLightningAddressAccountByPeerPubkey(context.Background(), strings.ToLower(peerPubkey))
	require.NoError(t, err)
	require.Equal(t, account.Username, gotByPeer.Username)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username, nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp LightningAddressDiscoveryResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	expectedCallback := "https://example.com/pay/callback/" + url.PathEscape(account.Username)
	require.Equal(t, expectedCallback, resp.Callback)

	expectedMetadata := `[["text/identifier","` + account.Username + `@example.com"],["text/plain","Payment to example"]]`
	require.Equal(t, expectedMetadata, resp.Metadata)
}

func TestLightningAddressDiscoveryRejectsSuffix(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/"+account.Username+"+tips", nil)
	rr := httptest.NewRecorder()

	api.routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestEnsureLightningAddressAccountNormalizesPeerPubkey(t *testing.T) {
	store, err := NewStore(Config{
		DatabaseDriver: "sqlite",
		DatabaseURL:    filepath.Join(t.TempDir(), "lnaddr.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})

	api := &API{db: store}

	rawPeer := " 02AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA@127.0.0.1:9735 "
	account, err := api.ensureLightningAddressAccount(context.Background(), rawPeer)
	require.NoError(t, err)

	require.Equal(t, "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", account.PeerPubkey)

	accountFromDB, err := store.GetLightningAddressAccountByPeerPubkey(context.Background(), "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	require.Equal(t, account.Username, accountFromDB.Username)
}

func TestLightningAddressAccountLookupNormalizesHandleCase(t *testing.T) {
	api, account := newLightningAddressTestAPI(t, "https://example.com", "Payment to txalkan", nil)

	got, ok, err := api.lightningAddressAccount(context.Background(), "  "+strings.ToUpper(account.Username)+" ")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, account.Username, got.Username)
}

func TestEnsureLightningAddressAccountRejectsMissingDependencies(t *testing.T) {
	api := &API{}

	_, err := api.ensureLightningAddressAccount(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty peer_pubkey")

	_, err = api.ensureLightningAddressAccount(context.Background(), "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.Error(t, err)
	require.Contains(t, err.Error(), "database is not configured")
}

func TestLightningAddressAccountLookupRejectsMissingDatabase(t *testing.T) {
	api := &API{}

	_, ok, err := api.lightningAddressAccount(context.Background(), "alice")
	require.Error(t, err)
	require.False(t, ok)
	require.Contains(t, err.Error(), "database is not configured")
}
