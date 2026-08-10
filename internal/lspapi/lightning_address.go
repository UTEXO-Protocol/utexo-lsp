package lspapi

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"utexo-lsp/pkg/node_client"
)

func (a *API) lightningAddressDomain() (string, error) {
	parsed, err := parseLightningAddressDomainURL(a.cfg.LightningAddressDomainURL)
	if err != nil {
		return "", err
	}
	return parsed.Host, nil
}

func (a *API) lightningAddressCallbackDomainURL() (string, error) {
	parsed, err := parseLightningAddressDomainURL(a.cfg.LightningAddressDomainURL)
	if err != nil {
		return "", err
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func (a *API) lightningAddressCallbackURL(account LightningAddressAccount) (string, error) {
	baseURL, err := a.lightningAddressCallbackDomainURL()
	if err != nil {
		return "", err
	}

	handle := account.Username
	if handle == "" {
		return "", errors.New("empty lightning address handle")
	}
	return baseURL + "/pay/callback/" + url.PathEscape(handle), nil
}

func (a *API) lightningAddressMetadata(account LightningAddressAccount) (string, string, error) {
	domain, err := a.lightningAddressDomain()
	if err != nil {
		return "", "", err
	}

	handle := account.Username
	if handle == "" {
		return "", "", errors.New("empty lightning address handle")
	}

	address := handle + "@" + domain
	shortDesc := strings.TrimSpace(a.cfg.LightningAddressShortDescription)
	if shortDesc == "" {
		shortDesc = address
	}

	metadataEntries := make([][2]string, 0, 3)
	metadataEntries = append(metadataEntries, [2]string{"text/identifier", address})
	metadataEntries = append(metadataEntries, [2]string{"text/plain", shortDesc})

	metadata, err := json.Marshal(metadataEntries)
	if err != nil {
		return "", "", err
	}

	return address, string(metadata), nil
}

func lightningAddressDescriptionHash(metadata string) string {
	sum := sha256.Sum256([]byte(metadata))
	return hex.EncodeToString(sum[:])
}

func parseLightningAddressRgbAssetQueryParams(r *http.Request) (*string, *uint64, error) {
	query := r.URL.Query()
	hasAssetID := query.Has("asset_id")
	hasAssetAmount := query.Has("asset_amount")
	if !hasAssetID && !hasAssetAmount {
		return nil, nil, nil
	}
	if hasAssetID != hasAssetAmount {
		return nil, nil, errors.New("asset_id and asset_amount must be provided together")
	}

	assetID := strings.TrimSpace(query.Get("asset_id"))
	if assetID == "" {
		return nil, nil, errors.New("asset_id is required")
	}
	assetAmount, err := strconv.ParseUint(strings.TrimSpace(query.Get("asset_amount")), 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot parse asset_amount: %v", err)
	}
	if assetAmount == 0 {
		return nil, nil, errors.New("asset_amount must be greater than zero")
	}

	return &assetID, &assetAmount, nil
}

func (a *API) handleLightningAddressDiscovery(w http.ResponseWriter, r *http.Request) {
	account, ok, err := a.lightningAddressAccount(r.Context(), r.PathValue("username"))
	if !ok {
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"status": "ERROR",
				"reason": fmt.Sprintf("failed to resolve lightning address account: %v", err),
			})
			return
		}
		http.NotFound(w, r)
		return
	}

	_, metadata, err := a.lightningAddressMetadata(account)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "ERROR",
			"reason": fmt.Sprintf("failed to build lightning address metadata: %v", err),
		})
		return
	}
	callbackURL, err := a.lightningAddressCallbackURL(account)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "ERROR",
			"reason": fmt.Sprintf("failed to build lightning address callback url: %v", err),
		})
		return
	}
	addrSig, attErr := a.db.GetApayAddressAttestation(r.Context(), account.PeerPubkey)
	if attErr != nil {
		log.Printf("apay: load address attestation for %s: %v", account.PeerPubkey, attErr)
	}
	writeJSON(w, http.StatusOK, LightningAddressDiscoveryResponse{
		Callback:        callbackURL,
		MaxSendable:     a.cfg.LightningAddressMaxSendableMsat,
		MinSendable:     a.cfg.LightningAddressMinSendableMsat,
		Metadata:        metadata,
		Tag:             "payRequest",
		RecipientPubkey: account.PeerPubkey,
		AddressSig:      addrSig,
	})
}

func (a *API) handleLightningAddressByPubkey(w http.ResponseWriter, r *http.Request) {
	clientPubkey, err := parseClientPubkey(r.PathValue("pubkey"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid client pubkey")
		return
	}

	account, ok, err := a.lightningAddressAccountByPubkey(r.Context(), clientPubkey)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to resolve lightning address account: %v", err))
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "lightning address account not found")
		return
	}

	domain, err := a.lightningAddressDomain()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to resolve lightning address domain: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, LightningAddressByPubkeyResponse{
		Username:         account.Username,
		Domain:           domain,
		LightningAddress: fmt.Sprintf("%s@%s", account.Username, domain),
	})
}

var handleRegex = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)

var reservedHandles = map[string]bool{
	"admin":      true,
	"support":    true,
	"root":       true,
	"utexo":      true,
	"lnurl":      true,
	"well-known": true,
	"system":     true,
	"host":       true,
	"config":     true,
	"api":        true,
	"help":       true,
	"security":   true,
}

type SetLightningAddressRequest struct {
	Pubkey    string `json:"pubkey"`
	Username  string `json:"username"`
	Signature string `json:"signature"`
}

const zbase32Alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"

var zbase32Encoding = base32.NewEncoding(zbase32Alphabet).WithPadding(base32.NoPadding)

func decodeZBase32(s string) ([]byte, error) {
	return zbase32Encoding.DecodeString(strings.TrimSpace(s))
}

func (a *API) handleSetLightningAddress(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)

	var req SetLightningAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	clientPubkey, err := parseClientPubkey(req.Pubkey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid client pubkey")
		return
	}

	username := normalizeLightningAddressHandle(req.Username)
	if reservedHandles[username] {
		writeErr(w, http.StatusForbidden, "this handle is reserved")
		return
	}
	if len(username) < 3 || len(username) > 64 {
		writeErr(w, http.StatusBadRequest, "lightning address must be between 3 and 64 characters")
		return
	}
	if !handleRegex.MatchString(username) {
		writeErr(w, http.StatusBadRequest, "lightning address contains forbidden characters")
		return
	}

	// Cryptographic Proof of Pubkey Ownership Verification
	if req.Signature == "" {
		writeErr(w, http.StatusBadRequest, "signature is required")
		return
	}

	var sigBytes []byte
	var decodeErr error

	// Try hex decoding first
	sigBytes, decodeErr = hex.DecodeString(req.Signature)
	if decodeErr != nil || len(sigBytes) != 65 {
		// Try decoding as zbase32 (standard Lightning signature format)
		sigBytes, decodeErr = decodeZBase32(req.Signature)
		if decodeErr != nil {
			writeErr(w, http.StatusBadRequest, "invalid signature format (hex or zbase32 compact signature required)")
			return
		}
	}

	if len(sigBytes) != 65 {
		writeErr(w, http.StatusBadRequest, "invalid signature length (65 bytes expected)")
		return
	}

	pubKeyBytes, err := hex.DecodeString(clientPubkey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid public key hex")
		return
	}

	pubKey, err := btcec.ParsePubKey(pubKeyBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid public key")
		return
	}

	// Message hash is double SHA256 of "Lightning Signed Message:" + username
	msgBytes := append([]byte("Lightning Signed Message:"), []byte(username)...)
	hash1 := sha256.Sum256(msgBytes)
	hash := sha256.Sum256(hash1[:])

	recoveredPubKey, _, err := ecdsa.RecoverCompact(sigBytes, hash[:])
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "failed to recover public key from signature")
		return
	}

	if !pubKey.IsEqual(recoveredPubKey) {
		writeErr(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	timeout := a.cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	account := LightningAddressAccount{
		PeerPubkey: clientPubkey,
		Username:   username,
	}

	err = a.db.SetLightningAddressAccount(ctx, account)
	if err != nil {
		if errors.Is(err, errLightningAddressTaken) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		log.Printf("ERROR: failed to set lightning address account for %s: %v", clientPubkey, err)
		writeErr(w, http.StatusInternalServerError, "internal server error")
		return
	}

	domain, err := a.lightningAddressDomain()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("failed to resolve lightning address domain: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, LightningAddressByPubkeyResponse{
		Username:         username,
		Domain:           domain,
		LightningAddress: fmt.Sprintf("%s@%s", username, domain),
	})
}

func (a *API) handleLightningAddressCallback(w http.ResponseWriter, r *http.Request) {
	account, ok, err := a.lightningAddressAccount(r.Context(), r.PathValue("username"))
	if !ok {
		if err != nil {
			writeLightningAddressError(w, http.StatusInternalServerError, fmt.Sprintf("failed to resolve lightning address account: %v", err))
			return
		}
		http.NotFound(w, r)
		return
	}

	amountStr := strings.TrimSpace(r.URL.Query().Get("amount"))
	if amountStr == "" {
		writeLightningAddressError(w, http.StatusBadRequest, "missing amount")
		return
	}

	amountMsat, err := strconv.ParseUint(amountStr, 10, 64)
	if err != nil {
		writeLightningAddressError(w, http.StatusBadRequest, fmt.Sprintf("cannot parse amount: %v", err))
		return
	}
	if amountMsat < a.cfg.LightningAddressMinSendableMsat || amountMsat > a.cfg.LightningAddressMaxSendableMsat {
		writeLightningAddressError(w, http.StatusBadRequest, "amount is out of acceptable range")
		return
	}
	assetID, assetAmount, err := parseLightningAddressRgbAssetQueryParams(r)
	if err != nil {
		writeLightningAddressError(w, http.StatusBadRequest, err.Error())
		return
	}

	_, metadata, err := a.lightningAddressMetadata(account)
	if err != nil {
		writeLightningAddressError(w, http.StatusInternalServerError, fmt.Sprintf("failed to build lightning address metadata: %v", err))
		return
	}
	reservation, err := a.db.ReserveLightningAddressInvoiceSlot(r.Context(), account, amountMsat, assetID, assetAmount, a.cfg.APayInboundInvoiceExpiry)
	if err != nil {
		writeLightningAddressError(w, http.StatusInternalServerError, fmt.Sprintf("failed to reserve lightning address invoice slot: %v", err))
		return
	}
	invoice, err := a.requestHodlInvoice(r.Context(), amountMsat, assetID, assetAmount, metadata, reservation.PaymentHash)
	if err != nil {
		if releaseErr := a.db.ReleaseLightningAddressInvoiceSlot(r.Context(), reservation.ID, err.Error()); releaseErr != nil {
			err = fmt.Errorf("%v (and failed to release reservation: %v)", err, releaseErr)
		}
		writeLightningAddressError(w, http.StatusInternalServerError, fmt.Sprintf("error constructing invoice: %v", err))
		return
	}
	if err := a.db.FinalizeLightningAddressInvoiceSlot(r.Context(), reservation.ID, invoice); err != nil {
		writeLightningAddressError(w, http.StatusInternalServerError, fmt.Sprintf("error persisting invoice slot: %v", err))
		return
	}

	proof, proofErr := a.db.BuildApayInvoiceProof(r.Context(), reservation.OrderID, reservation.HashIndex)
	if proofErr != nil {
		log.Printf("apay: build invoice proof (order %d, index %d): %v", reservation.OrderID, reservation.HashIndex, proofErr)
	}

	writeJSON(w, http.StatusOK, LightningAddressCallbackResponse{
		PR:     invoice,
		Routes: []string{},
		Proof:  proof,
	})
}

func (a *API) requestHodlInvoice(ctx context.Context, amountMsat uint64, assetID *string, assetAmount *uint64, metadata, paymentHash string) (string, error) {
	if strings.TrimSpace(paymentHash) == "" {
		return "", errors.New("empty payment hash")
	}
	payload := node_client.LNInvoiceRequest{
		AmtMsat:                 &amountMsat,
		ExpirySec:               uint32(a.cfg.APayInboundInvoiceExpiry.Seconds()),
		PaymentHash:             &paymentHash,
		MinFinalCltvExpiryDelta: &a.cfg.APayInboundMinFinalCltvExpiryDelta,
	}
	if assetID != nil {
		payload.AssetID = assetID
	}
	if assetAmount != nil {
		payload.AssetAmount = assetAmount
	}
	if metadata != "" {
		hash := lightningAddressDescriptionHash(metadata)
		payload.DescriptionHash = &hash
	}

	resp, err := a.lspClient.LNInvoice(ctx, payload)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Invoice) == "" {
		return "", errors.New("empty lsp lightning invoice")
	}
	return resp.Invoice, nil
}

func writeLightningAddressError(w http.ResponseWriter, code int, reason string) {
	writeJSON(w, code, map[string]string{
		"status": "ERROR",
		"reason": reason,
	})
}
