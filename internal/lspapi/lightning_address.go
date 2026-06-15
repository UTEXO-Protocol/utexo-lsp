package lspapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
		Username: account.Username,
		Domain:   domain,
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
