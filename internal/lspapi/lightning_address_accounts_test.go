package lspapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
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

func TestSetLightningAddress(t *testing.T) {
	api, _ := newLightningAddressTestAPI(t, "https://example.com", "Payment to example", nil)

	privKeyA, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	pubkeyA := hex.EncodeToString(privKeyA.PubKey().SerializeCompressed())

	privKeyB, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate key B: %v", err)
	}
	pubkeyB := hex.EncodeToString(privKeyB.PubKey().SerializeCompressed())

	signMessage := func(priv *btcec.PrivateKey, username string) string {
		msgBytes := append([]byte("Lightning Signed Message:"), []byte(username)...)
		hash1 := sha256.Sum256(msgBytes)
		hash := sha256.Sum256(hash1[:])
		sigBytes := ecdsa.SignCompact(priv, hash[:], true)
		return hex.EncodeToString(sigBytes)
	}

	type setReq struct {
		Pubkey    string `json:"pubkey"`
		Username  string `json:"username"`
		Signature string `json:"signature,omitempty"`
	}

	type setResp struct {
		Username         string `json:"username"`
		Domain           string `json:"domain"`
		LightningAddress string `json:"lightning_address,omitempty"`
		Error            string `json:"error,omitempty"`
	}

	executePost := func(payload setReq) (int, setResp) {
		if payload.Signature == "" {
			var priv *btcec.PrivateKey
			if payload.Pubkey == pubkeyA {
				priv = privKeyA
			} else if payload.Pubkey == pubkeyB {
				priv = privKeyB
			}
			if priv != nil {
				username := normalizeLightningAddressHandle(payload.Username)
				payload.Signature = signMessage(priv, username)
			}
		}
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/lightning_address", strings.NewReader(string(bodyBytes)))
		rr := httptest.NewRecorder()
		api.routes().ServeHTTP(rr, req)

		var resp setResp
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		return rr.Code, resp
	}

	// 1. Valid registration of arbitrary handle
	t.Run("valid registration", func(t *testing.T) {
		code, resp := executePost(setReq{Pubkey: pubkeyA, Username: "alice"})
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %+v", code, resp)
		}
		if resp.Username != "alice" || resp.Domain != "example.com" || resp.LightningAddress != "alice@example.com" {
			t.Fatalf("unexpected response: %+v", resp)
		}
	})

	// 2. Rejection of invalid handles (too short, too long)
	t.Run("too short handle", func(t *testing.T) {
		code, resp := executePost(setReq{Pubkey: pubkeyA, Username: "ab"})
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %+v", code, resp)
		}
	})

	t.Run("too long handle", func(t *testing.T) {
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: strings.Repeat("a", 65)})
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", code)
		}
	})

	// 3. Rejection of forbidden characters
	t.Run("forbidden characters", func(t *testing.T) {
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: "alice!"})
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", code)
		}
		code, _ = executePost(setReq{Pubkey: pubkeyA, Username: "alice space"})
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", code)
		}
	})

	// 4. Auto-convert uppercase and normalize
	t.Run("auto-convert uppercase and whitespace", func(t *testing.T) {
		code, resp := executePost(setReq{Pubkey: pubkeyA, Username: " \tALICE_123\n "})
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %+v", code, resp)
		}
		if resp.Username != "alice_123" {
			t.Fatalf("expected auto-converted lowercase 'alice_123', got '%s'", resp.Username)
		}
	})

	// 5. Rejection of malformed pubkey
	t.Run("malformed pubkey", func(t *testing.T) {
		code, _ := executePost(setReq{Pubkey: "invalidpubkey", Username: "charlie"})
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", code)
		}
	})

	// 6. Conflict error (409) when handle is already taken by User B
	t.Run("taken handle conflict", func(t *testing.T) {
		// User A claims "bob" first
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: "bob"})
		if code != http.StatusOK {
			t.Fatalf("expected 200")
		}

		// User B tries to claim "bob"
		code, resp := executePost(setReq{Pubkey: pubkeyB, Username: "bob"})
		if code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict, got %d: %+v", code, resp)
		}
	})

	// 7. Idempotent success when re-registered by the same User A
	t.Run("idempotent re-registration", func(t *testing.T) {
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: "charlie"})
		if code != http.StatusOK {
			t.Fatalf("expected 200")
		}
		code, resp := executePost(setReq{Pubkey: pubkeyA, Username: "charlie"})
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %+v", code, resp)
		}
		if resp.Username != "charlie" {
			t.Fatalf("expected charlie, got %s", resp.Username)
		}
	})

	// 8. Overwriting owned address is allowed, old handle is freed
	t.Run("overwrite address and free old handle", func(t *testing.T) {
		// User A currently owns "charlie". Let's change it to "charlie2"
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: "charlie2"})
		if code != http.StatusOK {
			t.Fatalf("expected 200")
		}

		// Now "charlie" should be freed, let's verify User B can claim "charlie"
		code, resp := executePost(setReq{Pubkey: pubkeyB, Username: "charlie"})
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %+v", code, resp)
		}
		if resp.Username != "charlie" {
			t.Fatalf("expected charlie, got %s", resp.Username)
		}
	})

	// 9. Concurrent registration race safety
	t.Run("concurrent registration race safety", func(t *testing.T) {
		const numGoroutines = 10
		var (
			successCount int64
			conflictCount int64
			otherCount   int64
			startBarrier = make(chan struct{})
			doneBarrier  = make(chan struct{})
		)

		for i := 0; i < numGoroutines; i++ {
			priv, err := btcec.NewPrivateKey()
			if err != nil {
				t.Fatalf("failed to generate private key: %v", err)
			}
			pkStr := hex.EncodeToString(priv.PubKey().SerializeCompressed())

			go func(privKey *btcec.PrivateKey, pk string) {
				<-startBarrier
				sig := signMessage(privKey, "speedy")
				code, resp := executePost(setReq{Pubkey: pk, Username: "speedy", Signature: sig})
				switch code {
				case http.StatusOK:
					atomic.AddInt64(&successCount, 1)
				case http.StatusConflict:
					atomic.AddInt64(&conflictCount, 1)
				default:
					t.Logf("unexpected other error: %d -> %+v", code, resp)
					atomic.AddInt64(&otherCount, 1)
				}
				doneBarrier <- struct{}{}
			}(priv, pkStr)
		}

		close(startBarrier)
		for i := 0; i < numGoroutines; i++ {
			<-doneBarrier
		}

		if successCount != 1 {
			t.Errorf("expected exactly 1 success, got %d", successCount)
		}
		if conflictCount != numGoroutines-1 {
			t.Errorf("expected %d conflicts, got %d", numGoroutines-1, conflictCount)
		}
		if otherCount != 0 {
			t.Errorf("expected 0 other errors, got %d", otherCount)
		}
	})

	// 10. Reserved/blacklist handles and strict regex checks
	t.Run("reserved handles are blacklisted", func(t *testing.T) {
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: "admin"})
		if code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for reserved 'admin', got %d", code)
		}
		code, _ = executePost(setReq{Pubkey: pubkeyA, Username: "support"})
		if code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for reserved 'support', got %d", code)
		}
	})

	t.Run("strict regex validation", func(t *testing.T) {
		// Consecutive dots/hyphens should fail
		code, _ := executePost(setReq{Pubkey: pubkeyA, Username: "alice--bob"})
		if code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for 'alice--bob', got %d", code)
		}
		// Leading/trailing dot/hyphen should fail
		code, _ = executePost(setReq{Pubkey: pubkeyA, Username: "-alice"})
		if code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for '-alice', got %d", code)
		}
		code, _ = executePost(setReq{Pubkey: pubkeyA, Username: "alice."})
		if code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for 'alice.', got %d", code)
		}
	})

	// 11. Cryptographic signature validation test
	t.Run("rejects request with missing signature", func(t *testing.T) {
		// We explicitly pass "Signature" as something non-empty but invalid, or empty with custom sign suppression.
		// Since executePost automatically signs if Signature == "", let's bypass it by passing a single space or custom signature:
		payload := setReq{
			Pubkey:    pubkeyA,
			Username:  "malicious",
			Signature: " ", // invalid sig
		}
		code, _ := executePost(payload)
		if code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for malformed signature, got %d", code)
		}
	})

	t.Run("rejects request with invalid signature of another pubkey", func(t *testing.T) {
		// Sign "malicious" with privKeyB but submit with pubkeyA
		sig := signMessage(privKeyB, "malicious")
		payload := setReq{
			Pubkey:    pubkeyA,
			Username:  "malicious",
			Signature: sig,
		}
		code, _ := executePost(payload)
		if code != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for mismatched signature, got %d", code)
		}
	})

	// 12. Oversized payload test
	t.Run("oversized payload", func(t *testing.T) {
		largeBody := strings.Repeat("A", 5000)
		req := httptest.NewRequest(http.MethodPost, "/lightning_address", strings.NewReader(largeBody))
		rr := httptest.NewRecorder()
		api.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for oversized payload, got %d", rr.Code)
		}
	})
}

func TestBtcecSig(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}
	pubKey := priv.PubKey()

	message := "alice"
	hash := sha256.Sum256([]byte(message))

	sig := ecdsa.Sign(priv, hash[:])
	sigBytes := sig.Serialize()

	parsedSig, err := ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		t.Fatalf("failed to parse DER signature: %v", err)
	}

	if !parsedSig.Verify(hash[:], pubKey) {
		t.Fatalf("failed to verify signature")
	}
}

func TestCompactSig(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}
	pubKey := priv.PubKey()

	message := "alice"
	msgBytes := append([]byte("Lightning Signed Message:"), []byte(message)...)
	hash1 := sha256.Sum256(msgBytes)
	hash := sha256.Sum256(hash1[:])

	sigBytes := ecdsa.SignCompact(priv, hash[:], true)

	recoveredPubKey, compressed, err := ecdsa.RecoverCompact(sigBytes, hash[:])
	if err != nil {
		t.Fatalf("failed to recover pubkey: %v", err)
	}

	if !compressed {
		t.Fatalf("expected compressed pubkey")
	}

	if !pubKey.IsEqual(recoveredPubKey) {
		t.Fatalf("recovered pubkey does not match original")
	}
}

func encodeZBase32(b []byte) string {
	var bits uint64
	var bitCount uint
	var out strings.Builder
	for i := 0; i < len(b); i++ {
		bits = (bits << 8) | uint64(b[i])
		bitCount += 8
		for bitCount >= 5 {
			bitCount -= 5
			out.WriteByte(zbase32Alphabet[(bits>>bitCount)&0x1F])
		}
	}
	if bitCount > 0 {
		out.WriteByte(zbase32Alphabet[(bits<<(5-bitCount))&0x1F])
	}
	return out.String()
}

func TestZBase32(t *testing.T) {
	testBytes := []byte("hello")
	encoded := encodeZBase32(testBytes)

	// Test valid decoding
	decoded, err := decodeZBase32(encoded)
	if err != nil {
		t.Fatalf("failed to decode zbase32: %v", err)
	}
	if string(decoded[:len(testBytes)]) != string(testBytes) {
		t.Fatalf("decoded bytes do not match original: got %q, want %q", string(decoded), string(testBytes))
	}

	// Test whitespace stripping
	decodedWithSpaces, err := decodeZBase32(" \n\t" + encoded + " \r\n")
	if err != nil {
		t.Fatalf("failed to decode zbase32 with spaces: %v", err)
	}
	if string(decodedWithSpaces[:len(testBytes)]) != string(testBytes) {
		t.Fatalf("decoded bytes with spaces do not match original")
	}

	// Test invalid characters
	_, err = decodeZBase32(encoded + "IL1") // I, L, 1 (except 1 is in zbase32, but let's use invalid Base32 chars like '7', Wait: '7' is in zbase32 alphabet: "ybndrfg8ejkmcpqxot1uwisza345h769". Let's use characters not in zbase32, such as 'I', 'O', 'V', 'Z' (uppercase not matching lowercase encoding, wait, standard library encoding handles uppercase if we configured it, but we configured lowercase; let's use special characters like '#', '@', '!')
	if err == nil {
		t.Fatalf("expected error decoding invalid characters, got nil")
	}
}

func TestStaticAdminHandles(t *testing.T) {
	// Configure static admin handles
	const adminPubkey = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	cfg := Config{
		LightningAddressDomainURL: "https://users.utexo.com",
		AdminHandles: map[string]string{
			"admin": adminPubkey,
		},
	}
	api := &API{
		cfg: cfg,
	}

	// 1. Verify we can resolve admin by username
	acc, ok, err := api.lightningAddressAccount(context.Background(), "admin")
	if err != nil || !ok {
		t.Fatalf("expected admin to resolve, ok: %v, err: %v", ok, err)
	}
	if acc.Username != "admin" || acc.PeerPubkey != adminPubkey {
		t.Fatalf("unexpected acc returned: %+v", acc)
	}

	// 2. Verify we can resolve admin by pubkey
	acc, ok, err = api.lightningAddressAccountByPubkey(context.Background(), adminPubkey)
	if err != nil || !ok {
		t.Fatalf("expected admin to resolve by pubkey, ok: %v, err: %v", ok, err)
	}
	if acc.Username != "admin" || acc.PeerPubkey != adminPubkey {
		t.Fatalf("unexpected acc returned: %+v", acc)
	}
}
