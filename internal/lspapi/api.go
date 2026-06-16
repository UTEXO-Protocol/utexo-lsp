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
	"strconv"
	"strings"
	"time"
	"utexo-lsp/pkg/node_client"
)

func NewAPI(cfg Config, db Store, lspClient, rgbClient *node_client.Client) *API {
	return &API{
		cfg:       cfg,
		db:        db,
		lspClient: lspClient,
		rgbClient: rgbClient,
	}
}

func (a *API) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("GET /get_info", a.handleGetInfo)
	mux.HandleFunc("GET /.well-known/lnurlp/{username}", a.handleLightningAddressDiscovery)
	mux.HandleFunc("GET /pay/callback/{username}", a.handleLightningAddressCallback)
	mux.HandleFunc("GET /lightning_address/by_pubkey/{pubkey}", a.handleLightningAddressByPubkey)
	mux.HandleFunc("POST /onchain_send", a.handleOnchainSend)
	mux.HandleFunc("POST /lightning_receive", a.handleLightningReceive)
	mux.HandleFunc("POST /internal/async_order/claimable", a.handleInternalInboundInvoiceClaimable)
	mux.HandleFunc("POST /internal/async_order/payment_sent", a.handleInternalAsyncOrderPaymentSent)
	mux.HandleFunc("POST /internal/async_order/new", a.handleInternalAsyncOrderNew)
	return mux
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeOKJSON(w, map[string]any{"ok": true})
}

func (a *API) handleInternalInboundInvoiceClaimable(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(a.cfg.APayBearerToken)
	if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req AsyncOrderClaimableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	if req.ClaimDeadlineHeight == nil || *req.ClaimDeadlineHeight == 0 {
		writeErr(w, http.StatusBadRequest, "claim_deadline_height is required")
		return
	}
	if err := a.validateAsyncOrderClaimDeadlineWithinPolicy(
		ctx,
		*req.ClaimDeadlineHeight,
		uint64(a.cfg.APayInboundMinFinalCltvExpiryDelta)-ldkHtlcFailBackBuffer-ldkMinFinalCltvBuffer,
	); err != nil {
		if errors.Is(err, errAsyncClaimDeadlineDependency) {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := a.db.MarkAsyncRotatingInvoiceClaimable(ctx, req.PaymentHash, req.AmountMsat, req.ClaimDeadlineHeight); err != nil {
		if errors.Is(err, errAsyncInvoiceNotFound) {
			writeErr(w, http.StatusNotFound, "claimable invoice not found")
			return
		}
		if errors.Is(err, errAsyncRotatingInvoiceAmountMsatMismatch) || errors.Is(err, errAsyncRotatingInvoiceInvalidAmountMsat) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeOKJSON(w, map[string]any{"ok": true})
}

func (a *API) handleInternalAsyncOrderPaymentSent(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(a.cfg.APayBearerToken)
	if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req AsyncOrderPaymentSentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	paymentHash := strings.ToLower(strings.TrimSpace(req.PaymentHash))
	if !isValidPaymentHash(paymentHash) {
		writeErr(w, http.StatusBadRequest, "invalid payment_hash")
		return
	}

	paymentPreimage := strings.ToLower(strings.TrimSpace(req.PaymentPreimage))
	if !isValidPaymentHash(paymentPreimage) {
		writeErr(w, http.StatusBadRequest, "invalid payment_preimage")
		return
	}

	preimageBytes, err := hex.DecodeString(paymentPreimage)
	if err != nil || len(preimageBytes) != sha256.Size {
		writeErr(w, http.StatusBadRequest, "invalid payment_preimage")
		return
	}
	sum := sha256.Sum256(preimageBytes)
	if hex.EncodeToString(sum[:]) != paymentHash {
		writeErr(w, http.StatusBadRequest, "payment_preimage does not match payment_hash")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	if _, err := a.db.MarkAsyncRotatingInvoiceOutboundClaimed(ctx, paymentHash, paymentPreimage); err != nil {
		if errors.Is(err, errAsyncInvoiceNotFound) {
			current, loadErr := a.db.LoadAsyncRotatingInvoiceByPaymentHash(ctx, paymentHash)
			if loadErr != nil {
				writeErr(w, http.StatusInternalServerError, loadErr.Error())
				return
			}
			switch {
			case asyncRotatingInvoiceStatusAtOrBeyond(current.Status, asyncInvoiceStatusOutboundClaimed):
				writeOKJSON(w, map[string]any{"ok": true})
				return
			case !asyncRotatingInvoiceStatusAtOrBeyond(current.Status, asyncInvoiceStatusOutboundPaid):
				writeErr(w, http.StatusServiceUnavailable, "payment_sent received before outbound payment was confirmed locally")
				return
			default:
				writeErr(w, http.StatusInternalServerError, "async invoice in unexpected status before outbound claim")
				return
			}
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOKJSON(w, map[string]any{"ok": true})
}

func (a *API) handleInternalAsyncOrderNew(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(a.cfg.APayBearerToken)
	if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req AsyncOrderNewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, AsyncOrderJSONRPCResponseEnvelope{
			JSONRPC: asyncOrderJSONRPCVersion,
			ID:      nil,
			Result:  nil,
			Error: &AsyncOrderError{
				Code:    asyncOrderJSONRPCParseError,
				Message: "parse error",
			},
		})
		return
	}
	req.PeerPubkey = normalizePeerPubkey(req.PeerPubkey)
	if req.PeerPubkey == "" {
		writeJSON(w, http.StatusBadRequest, AsyncOrderJSONRPCResponseEnvelope{
			JSONRPC: asyncOrderJSONRPCVersion,
			ID:      req.ID,
			Result:  nil,
			Error: &AsyncOrderError{
				Code:    asyncOrderJSONRPCInvalidRequest,
				Message: "invalid request",
			},
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	if _, err := a.ensureLightningAddressAccount(ctx, req.PeerPubkey); err != nil {
		writeJSON(w, http.StatusInternalServerError, AsyncOrderJSONRPCResponseEnvelope{
			JSONRPC: asyncOrderJSONRPCVersion,
			ID:      req.ID,
			Result:  nil,
			Error: &AsyncOrderError{
				Code:    asyncOrderJSONRPCInternalError,
				Message: err.Error(),
			},
		})
		return
	}

	resp, rpcErr, err := a.db.ApplyAsyncOrderNew(ctx, req)
	if rpcErr != nil {
		writeJSON(w, asyncOrderHTTPStatusFromErrorCode(rpcErr.Code), AsyncOrderJSONRPCResponseEnvelope{
			JSONRPC: asyncOrderJSONRPCVersion,
			ID:      req.ID,
			Result:  nil,
			Error:   rpcErr,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, AsyncOrderJSONRPCResponseEnvelope{
			JSONRPC: asyncOrderJSONRPCVersion,
			ID:      req.ID,
			Result:  nil,
			Error: &AsyncOrderError{
				Code:    asyncOrderJSONRPCInternalError,
				Message: err.Error(),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, AsyncOrderJSONRPCResponseEnvelope{
		JSONRPC: asyncOrderJSONRPCVersion,
		ID:      req.ID,
		Result:  resp,
		Error:   nil,
	})
}

func (a *API) validateAsyncOrderClaimDeadlineWithinPolicy(ctx context.Context, claimDeadlineHeight uint32, requiredBlocks uint64) error {
	if a.rgbClient == nil {
		return fmt.Errorf("%w: rgb client is not configured", errAsyncClaimDeadlineDependency)
	}

	info, err := a.rgbClient.NetworkInfo(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", errAsyncClaimDeadlineDependency, err)
	}

	if int64(claimDeadlineHeight) <= info.Height {
		return fmt.Errorf(
			"claim_deadline_height %d already passed; current height %d",
			claimDeadlineHeight,
			info.Height,
		)
	}

	blocksLeft := uint64(claimDeadlineHeight) - uint64(info.Height)
	if blocksLeft <= requiredBlocks {
		return fmt.Errorf(
			"claim_deadline_height %d is too close to current height %d (have %d blocks, need more than %d)",
			claimDeadlineHeight,
			info.Height,
			blocksLeft,
			requiredBlocks,
		)
	}

	return nil
}

func (a *API) runAsyncOrderOutbox(ctx context.Context) {
	for range 10 {
		job, ok, err := a.db.ClaimAsyncRotatingInvoiceOutboxJob(ctx)
		if err != nil {
			log.Printf("cron async_order_outbox claim: %v", err)
			return
		}
		if !ok {
			return
		}

		err = a.processAsyncOrderOutboxJob(ctx, job)
		if err != nil {
			log.Printf("cron async_order_outbox job %d %s/%s failed: %v", job.ID, job.Action, job.PaymentHash, err)
			if retryErr := a.db.MarkAsyncRotatingInvoiceOutboxRetry(ctx, job.ID, err.Error()); retryErr != nil {
				log.Printf("cron async_order_outbox retry job %d failed: %v", job.ID, retryErr)
			}
			continue
		}

		if err := a.db.MarkAsyncRotatingInvoiceOutboxDone(ctx, job.ID); err != nil {
			log.Printf("cron async_order_outbox complete job %d failed: %v", job.ID, err)
		}
	}
}

func (a *API) processAsyncOrderOutboxJob(ctx context.Context, job AsyncRotatingInvoiceOutboxJob) error {
	switch job.Action {
	case asyncOutboxActionRequestOutboundInvoice:
		return a.aPayRequestOutboundInvoiceJob(ctx, job.PaymentHash)
	case asyncOutboxActionSendOutboundPayment:
		return a.aPaySendOutboundPaymentJob(ctx, job.PaymentHash)
	case asyncOutboxActionClaimInboundInvoice:
		return a.aPayClaimInboundInvoiceJob(ctx, job.PaymentHash)
	default:
		return fmt.Errorf("unknown async outbox action %q", job.Action)
	}
}

func (a *API) aPayRequestOutboundInvoiceJob(ctx context.Context, paymentHash string) error {
	if a.rgbClient == nil {
		return fmt.Errorf("rgb client is not configured")
	}

	jobCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
	defer cancel()

	invoice, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("load invoice: %w", err)
	}
	switch invoice.Status {
	case asyncInvoiceStatusClaimable, asyncInvoiceStatusOutboundRequested:
	case asyncInvoiceStatusOutboundPending, asyncInvoiceStatusOutboundPaid, asyncInvoiceStatusOutboundClaimed, asyncInvoiceStatusInboundClaimed, asyncInvoiceStatusInboundCancelled, asyncInvoiceStatusOutboundCancelled, asyncInvoiceStatusFailed:
		return nil
	default:
		return fmt.Errorf("async invoice %s in unexpected status %q before outbound request", paymentHash, invoice.Status)
	}

	if invoice.ClaimDeadlineHeight == nil {
		return errors.New("claim_deadline_height is missing")
	}
	if err := a.validateAsyncOrderClaimDeadlineWithinPolicy(
		jobCtx,
		*invoice.ClaimDeadlineHeight,
		uint64(a.cfg.APayOutboundMinFinalCltvExpiryDelta)+uint64(a.cfg.claimMarginBlocks()),
	); err != nil {
		if errors.Is(err, errAsyncClaimDeadlineDependency) {
			return err
		}
		transitioned, markErr := a.db.MarkAsyncRotatingInvoiceFailed(jobCtx, paymentHash)
		if markErr != nil {
			return fmt.Errorf("persist failed: %w", markErr)
		}
		if !transitioned {
			current, reloadErr := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
			if reloadErr != nil {
				return fmt.Errorf("reload invoice after failed: %w", reloadErr)
			}
			if asyncRotatingInvoiceStatusAtOrBeyond(current.Status, asyncInvoiceStatusOutboundPending) {
				return nil
			}
			return fmt.Errorf("async invoice %s in unexpected status %q after deadline validation failure", paymentHash, current.Status)
		}
		return nil
	}

	peerPubkey, err := a.db.GetAsyncOrderPeerPubkeyByOrderID(jobCtx, invoice.OrderID)
	if err != nil {
		return fmt.Errorf("load peer: %w", err)
	}

	account, err := a.db.GetLightningAddressAccountByPeerPubkey(jobCtx, peerPubkey)
	if err != nil {
		return fmt.Errorf("load lightning address account: %w", err)
	}
	_, metadata, err := a.lightningAddressMetadata(account)
	if err != nil {
		return fmt.Errorf("build lightning address metadata: %w", err)
	}
	descriptionHash := lightningAddressDescriptionHash(metadata)

	if invoice.Status == asyncInvoiceStatusClaimable {
		transitioned, err := a.db.MarkAsyncRotatingInvoiceOutboundRequested(jobCtx, paymentHash)
		if err != nil {
			return fmt.Errorf("persist outbound_requested: %w", err)
		}
		if transitioned {
			invoice.Status = asyncInvoiceStatusOutboundRequested
		}
	}

	refreshed, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("reload invoice: %w", err)
	}
	if asyncRotatingInvoiceStatusAtOrBeyond(refreshed.Status, asyncInvoiceStatusOutboundPending) {
		return nil
	}
	if refreshed.Status != asyncInvoiceStatusOutboundRequested {
		return fmt.Errorf("async invoice %s in unexpected status %q before outbound request", paymentHash, refreshed.Status)
	}

	req := node_client.AsyncOrderOutboundInvoiceRequest{
		ClientNodeID: peerPubkey,
		Params: node_client.AsyncOrderRequestOutboundInvoiceParams{
			AmountMsat:              invoice.AmountMsat,
			AssetAmount:             invoice.AssetAmount,
			AssetID:                 invoice.AssetID,
			DescriptionHash:         descriptionHash,
			InvoiceExpirySec:        uint32(a.cfg.APayOutboundInvoiceExpiry.Seconds()),
			MinFinalCltvExpiryDelta: a.cfg.APayOutboundMinFinalCltvExpiryDelta,
			HashIndex:               strconv.FormatInt(invoice.HashIndex, 10),
			PaymentHash:             invoice.PaymentHash,
		},
	}

	resp, err := a.rgbClient.AsyncOrderOutboundInvoice(jobCtx, req)
	if err != nil {
		return fmt.Errorf("could not get async outbound invoice: %w", err)
	}

	if err := a.validateAsyncOrderRequestInvoiceResponse(jobCtx, invoice, peerPubkey, req.Params, resp); err != nil {
		return err
	}

	transitioned, err := a.db.MarkAsyncRotatingInvoiceOutboundPending(jobCtx, paymentHash, resp.Bolt11)
	if err != nil {
		return fmt.Errorf("persist outbound_pending: %w", err)
	}
	if !transitioned {
		current, reloadErr := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
		if reloadErr != nil {
			return fmt.Errorf("reload invoice after outbound_pending: %w", reloadErr)
		}
		if asyncRotatingInvoiceStatusAtOrBeyond(current.Status, asyncInvoiceStatusOutboundPending) {
			return nil
		}
		return fmt.Errorf("async invoice %s in unexpected status %q after outbound invoice request", paymentHash, current.Status)
	}
	return nil
}

func (a *API) aPaySendOutboundPaymentJob(ctx context.Context, paymentHash string) error {
	if a.lspClient == nil {
		return fmt.Errorf("lsp client is not configured")
	}

	jobCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
	defer cancel()

	invoice, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("load invoice: %w", err)
	}
	if asyncRotatingInvoiceStatusAtOrBeyond(invoice.Status, asyncInvoiceStatusOutboundPaid) {
		return nil
	}
	if invoice.Status != asyncInvoiceStatusOutboundPending {
		return fmt.Errorf("async invoice %s in unexpected status %q before outbound payment", paymentHash, invoice.Status)
	}

	refreshed, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("reload invoice: %w", err)
	}
	if asyncRotatingInvoiceStatusAtOrBeyond(refreshed.Status, asyncInvoiceStatusOutboundPaid) {
		return nil
	}
	if refreshed.Status != asyncInvoiceStatusOutboundPending {
		return fmt.Errorf("async invoice %s in unexpected status %q before outbound payment", paymentHash, refreshed.Status)
	}
	invoice = refreshed

	if invoice.OutboundInvoice == nil || strings.TrimSpace(*invoice.OutboundInvoice) == "" {
		return errors.New("outbound invoice is missing")
	}

	if err := a.sendLNByInvoice(jobCtx, *invoice.OutboundInvoice); err != nil {
		return err
	}

	transitioned, err := a.db.MarkAsyncRotatingInvoiceOutboundPaid(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("persist outbound_paid: %w", err)
	}
	if !transitioned {
		current, reloadErr := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
		if reloadErr != nil {
			return fmt.Errorf("reload invoice after outbound_paid: %w", reloadErr)
		}
		if asyncRotatingInvoiceStatusAtOrBeyond(current.Status, asyncInvoiceStatusOutboundPaid) {
			return nil
		}
		return fmt.Errorf("async invoice %s in unexpected status %q after outbound payment", paymentHash, current.Status)
	}
	return nil
}

func (a *API) aPayClaimInboundInvoiceJob(ctx context.Context, paymentHash string) error {
	jobCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
	defer cancel()

	invoice, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("load invoice: %w", err)
	}
	if asyncRotatingInvoiceStatusAtOrBeyond(invoice.Status, asyncInvoiceStatusInboundClaimed) {
		return nil
	}
	if invoice.Status != asyncInvoiceStatusOutboundClaimed {
		return fmt.Errorf("async invoice %s in unexpected status %q before inbound claim", paymentHash, invoice.Status)
	}

	refreshed, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("reload invoice: %w", err)
	}
	if asyncRotatingInvoiceStatusAtOrBeyond(refreshed.Status, asyncInvoiceStatusInboundClaimed) {
		return nil
	}
	if refreshed.Status != asyncInvoiceStatusOutboundClaimed {
		return fmt.Errorf("async invoice %s in unexpected status %q before inbound claim", paymentHash, refreshed.Status)
	}
	invoice = refreshed

	if invoice.PaymentPreimage == nil || strings.TrimSpace(*invoice.PaymentPreimage) == "" {
		return errors.New("payment_preimage is missing")
	}

	if err := a.aPayClaimInboundInvoice(jobCtx, paymentHash, *invoice.PaymentPreimage); err != nil {
		return err
	}

	transitioned, err := a.db.MarkAsyncRotatingInvoiceInboundClaimed(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("persist inbound_claimed: %w", err)
	}
	if !transitioned {
		current, reloadErr := a.db.LoadAsyncRotatingInvoiceByPaymentHash(jobCtx, paymentHash)
		if reloadErr != nil {
			return fmt.Errorf("reload invoice after inbound_claimed: %w", reloadErr)
		}
		if asyncRotatingInvoiceStatusAtOrBeyond(current.Status, asyncInvoiceStatusInboundClaimed) {
			return nil
		}
		return fmt.Errorf("async invoice %s in unexpected status %q after inbound claim", paymentHash, current.Status)
	}
	return nil
}

func (a *API) validateAsyncOrderRequestInvoiceResponse(ctx context.Context, reserved AsyncRotatingInvoice, peerPubkey string, params node_client.AsyncOrderRequestOutboundInvoiceParams, resp node_client.AsyncOrderOutboundInvoiceResponse) error {
	responsePaymentHash := strings.ToLower(strings.TrimSpace(resp.PaymentHash))
	if !isValidPaymentHash(responsePaymentHash) {
		return fmt.Errorf("invalid outbound invoice payment_hash %q", resp.PaymentHash)
	}
	expectedPaymentHash := strings.ToLower(strings.TrimSpace(reserved.PaymentHash))
	if responsePaymentHash != expectedPaymentHash {
		return fmt.Errorf("invalid outbound invoice - response payment_hash mismatch: got %s want %s", responsePaymentHash, expectedPaymentHash)
	}
	if strings.TrimSpace(resp.Bolt11) == "" {
		return errors.New("invalid outbound invoice - empty response bolt11")
	}

	decoded, err := a.validateLNInvoice(ctx, resp.Bolt11)
	if err != nil {
		return err
	}
	decodedPaymentHash := strings.ToLower(strings.TrimSpace(decoded.PaymentHash))
	if !isValidPaymentHash(decodedPaymentHash) {
		return fmt.Errorf("invalid outbound invoice - decoded invoice has invalid payment_hash %q", decoded.PaymentHash)
	}
	if decodedPaymentHash != responsePaymentHash {
		return fmt.Errorf("invalid outbound invoice - decoded invoice payment_hash mismatch: got %s want %s", decodedPaymentHash, responsePaymentHash)
	}
	if strings.TrimSpace(decoded.DescriptionHash) == "" {
		return errors.New("decoded invoice missing description_hash")
	}
	decodedDescriptionHash := strings.ToLower(strings.TrimSpace(decoded.DescriptionHash))
	expectedDescriptionHash := strings.ToLower(strings.TrimSpace(params.DescriptionHash))
	if decodedDescriptionHash != expectedDescriptionHash {
		return fmt.Errorf(
			"invalid outbound invoice - decoded invoice description_hash mismatch: got %s want %s",
			decodedDescriptionHash,
			expectedDescriptionHash,
		)
	}
	if params.AmountMsat != reserved.AmountMsat {
		return fmt.Errorf("invalid outbound invoice - rotating invoice amount_msat mismatch with request params: got %d want %d", params.AmountMsat, reserved.AmountMsat)
	}
	if err := validateOptionalStringMatch(reserved.AssetID, params.AssetID, "asset_id"); err != nil {
		return fmt.Errorf("invalid outbound invoice - rotating invoice mismatch with request params: %w", err)
	}
	if err := validateOptionalUint64Match(reserved.AssetAmount, params.AssetAmount, "asset_amount"); err != nil {
		return fmt.Errorf("invalid outbound invoice - rotating invoice mismatch with request params: %w", err)
	}
	if decoded.AmtMsat <= 0 || uint64(decoded.AmtMsat) != reserved.AmountMsat {
		return fmt.Errorf("invalid outbound invoice - decoded invoice amount_msat mismatch: got %d want %d", decoded.AmtMsat, reserved.AmountMsat)
	}
	if err := validateOptionalStringValueMatch(reserved.AssetID, decoded.AssetID, "asset_id"); err != nil {
		return err
	}
	if err := validateOptionalInt64AsUint64Match(reserved.AssetAmount, decoded.AssetAmount, "asset_amount"); err != nil {
		return err
	}

	decodedPayee := normalizePeerPubkey(decoded.PayeePubkey)
	expectedPayee := normalizePeerPubkey(peerPubkey)
	if decodedPayee == "" {
		return errors.New("decoded invoice missing payee_pubkey")
	}
	if decodedPayee != expectedPayee {
		return fmt.Errorf("decoded invoice payee_pubkey mismatch: got %s want %s", decodedPayee, expectedPayee)
	}
	if decoded.ExpirySec != int64(params.InvoiceExpirySec) {
		return fmt.Errorf("invalid outbound invoice - decoded invoice expiry_sec %d does not match requested %d", decoded.ExpirySec, params.InvoiceExpirySec)
	}

	minCltv := uint64(params.MinFinalCltvExpiryDelta) + ldkMinFinalCltvBuffer
	if decoded.MinFinalCltvExpiryDelta != minCltv {
		return fmt.Errorf(
			"decoded invoice min_final_cltv_expiry_delta %d does not match requested %d",
			decoded.MinFinalCltvExpiryDelta,
			minCltv,
		)
	}
	if reserved.ClaimDeadlineHeight == nil {
		return errors.New("claim_deadline_height is missing")
	}
	if err := a.validateAsyncOrderClaimDeadlineWithinPolicy(ctx, *reserved.ClaimDeadlineHeight, decoded.MinFinalCltvExpiryDelta+uint64(a.cfg.claimMarginBlocks())); err != nil {
		return err
	}

	return nil
}

func validateOptionalStringMatch(expected, got *string, field string) error {
	expectedValue := ""
	if expected != nil {
		expectedValue = strings.TrimSpace(*expected)
	}
	gotValue := ""
	if got != nil {
		gotValue = strings.TrimSpace(*got)
	}
	if expectedValue != gotValue {
		return fmt.Errorf("decoded invoice %s mismatch: got %q want %q", field, gotValue, expectedValue)
	}
	return nil
}

func validateOptionalStringValueMatch(expected *string, got string, field string) error {
	expectedValue := ""
	if expected != nil {
		expectedValue = strings.TrimSpace(*expected)
	}
	gotValue := strings.TrimSpace(got)
	if expectedValue != gotValue {
		return fmt.Errorf("decoded invoice %s mismatch: got %q want %q", field, gotValue, expectedValue)
	}
	return nil
}

func validateOptionalUint64Match(expected, got *uint64, field string) error {
	if expected == nil && got == nil {
		return nil
	}
	if expected == nil || got == nil || *expected != *got {
		return fmt.Errorf("decoded invoice %s mismatch: got %s want %s", field, formatOptionalUint64(got), formatOptionalUint64(expected))
	}
	return nil
}

func validateOptionalInt64AsUint64Match(expected *uint64, got int64, field string) error {
	var gotPtr *uint64
	if got > 0 {
		gotValue := uint64(got)
		gotPtr = &gotValue
	}
	return validateOptionalUint64Match(expected, gotPtr, field)
}

func formatOptionalUint64(v *uint64) string {
	if v == nil {
		return "<nil>"
	}
	return strconv.FormatUint(*v, 10)
}

func asyncOrderHTTPStatusFromErrorCode(code int64) int {
	switch code {
	case asyncOrderErrorDuplicateIndexConflict, asyncOrderErrorDuplicateHashConflict:
		return http.StatusConflict
	case asyncOrderJSONRPCInternalError:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

func (a *API) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	info, err := a.lspClient.NodeInfo(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

func (a *API) handleOnchainSend(w http.ResponseWriter, r *http.Request) {
	var req OnchainSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.RGBInvoice) == "" {
		writeErr(w, http.StatusBadRequest, "rgb_invoice is required")
		return
	}
	if err := ensureLNInvoiceInputMinAmount(&req.LNInvoice, a.cfg.MinAmtMsat); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	decodedRGB, err := a.validateRGBInvoice(ctx, req.RGBInvoice)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := applyAndValidateOnchainAssetParams(&req.LNInvoice, decodedRGB); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := alignAndValidateLNExpiryWithRGB(&req.LNInvoice, decodedRGB, time.Now().UTC(), a.cfg.ExpiryMatchToleranceSec); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.LNInvoice.AssetID == nil || strings.TrimSpace(*req.LNInvoice.AssetID) == "" {
		writeErr(w, http.StatusBadRequest, "rgb_invoice must contain asset_id for onchain_send")
		return
	}
	if err := a.ensureAssetSupported(strings.TrimSpace(*req.LNInvoice.AssetID)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	invoiceResp, err := a.lspClient.LNInvoice(ctx, node_client.LNInvoiceRequest{
		AmtMsat:                 req.LNInvoice.AmtMsat,
		ExpirySec:               req.LNInvoice.ExpirySec,
		AssetID:                 req.LNInvoice.AssetID,
		AssetAmount:             req.LNInvoice.AssetAmount,
		DescriptionHash:         req.LNInvoice.DescriptionHash,
		PaymentHash:             req.LNInvoice.PaymentHash,
		MinFinalCltvExpiryDelta: req.LNInvoice.MinFinalCltvExpiryDelta,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, wrapErr("could not get ln invoice", err).Error())
		return
	}

	if strings.TrimSpace(invoiceResp.Invoice) == "" {
		writeErr(w, http.StatusBadGateway, "empty lsp lightning invoice")
		return
	}

	lnDecoded, err := a.validateLNInvoice(ctx, invoiceResp.Invoice)
	if err != nil {
		writeErr(w, http.StatusBadGateway, wrapErr("created ln invoice failed validation", err).Error())
		return
	}

	lnExp := unixFromTimestampAndExpiry(lnDecoded.Timestamp, lnDecoded.ExpirySec)
	id, err := a.db.InsertOnchainSend(ctx, req.RGBInvoice, invoiceResp.Invoice, &lnExp)
	if err != nil {
		writeErr(w, http.StatusConflict, wrapErr("cannot persist mapping", err).Error())
		return
	}

	writeJSON(w, http.StatusOK, OnchainSendResponse{
		RGBInvoice: req.RGBInvoice,
		LNInvoice:  invoiceResp.Invoice,
		MappingID:  id,
	})
}

func (a *API) handleLightningReceive(w http.ResponseWriter, r *http.Request) {
	var req LightningReceiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.LNInvoice) == "" {
		writeErr(w, http.StatusBadRequest, "ln_invoice is required")
		return
	}
	if req.RGBParams.AssetID == nil || strings.TrimSpace(*req.RGBParams.AssetID) == "" {
		writeErr(w, http.StatusBadRequest, "rgb_invoice.asset_id is required for transfer monitoring")
		return
	}
	if err := applyAndValidateRGBAssignment(&req.RGBParams, a.cfg.DefaultRGBAssignment); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.ensureAssetSupported(strings.TrimSpace(*req.RGBParams.AssetID)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	applyBackendMinConfirmations(&req.RGBParams, a.cfg.MinConfirmations)

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	decodedLN, err := a.validateLNInvoice(ctx, req.LNInvoice)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ensureDecodedLNMinAmount(decodedLN, a.cfg.MinAmtMsat); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	if err := alignAndValidateRGBDurationWithLN(&req.RGBParams, decodedLN, now, a.cfg.ExpiryMatchToleranceSec); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	assignmentJSON, err := rgbAssignmentJSON(req.RGBParams.Assignment)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var rgbExpiry *int64
	if req.RGBParams.DurationSeconds != nil {
		exp := now.Unix() + int64(*req.RGBParams.DurationSeconds)
		rgbExpiry = &exp
	}

	rgbResp, err := a.rgbClient.RGBInvoice(ctx, node_client.RGBInvoiceRequest{
		AssetID:             req.RGBParams.AssetID,
		Assignment:          assignmentJSON,
		ExpirationTimestamp: rgbExpiry,
		MinConfirmations:    req.RGBParams.MinConfirmations,
		Witness:             req.RGBParams.Witness,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, wrapErr("failed /rgbinvoice", err).Error())
		return
	}
	if strings.TrimSpace(rgbResp.Invoice) == "" {
		writeErr(w, http.StatusBadGateway, "empty lsp rgb invoice")
		return
	}
	if rgbResp.BatchTransferIdx == 0 {
		writeErr(w, http.StatusBadGateway, "empty batch_transfer_idx from /rgbinvoice")
		return
	}

	decodedRGB, err := a.validateRGBInvoice(ctx, rgbResp.Invoice)
	if err != nil {
		writeErr(w, http.StatusBadGateway, wrapErr("created rgb invoice failed validation", err).Error())
		return
	}

	var rgbExp *time.Time
	if decodedRGB.ExpirationTimestamp != nil {
		t := time.Unix(*decodedRGB.ExpirationTimestamp, 0).UTC()
		rgbExp = &t
	}

	id, err := a.db.InsertLightningReceive(ctx, req.LNInvoice, rgbResp.Invoice, strings.TrimSpace(*req.RGBParams.AssetID), rgbResp.BatchTransferIdx, rgbExp)
	if err != nil {
		writeErr(w, http.StatusConflict, wrapErr("cannot persist mapping", err).Error())
		return
	}

	writeJSON(w, http.StatusOK, LightningReceiveResponse{
		LNInvoice:  req.LNInvoice,
		RGBInvoice: rgbResp.Invoice,
		MappingID:  id,
	})
}

func (a *API) validateLNInvoice(ctx context.Context, invoice string) (*node_client.DecodeLNInvoiceResponse, error) {
	resp, err := a.rgbClient.DecodeLNInvoice(ctx, node_client.DecodeLNInvoiceRequest{Invoice: invoice})
	if err != nil {
		return nil, wrapErr("/decodelninvoice", err)
	}
	expiresAt := resp.Timestamp + resp.ExpirySec
	if time.Now().UTC().Unix() >= expiresAt {
		return nil, errors.New("ln invoice already expired")
	}
	return &resp, nil
}

func (a *API) validateRGBInvoice(ctx context.Context, invoice string) (*node_client.DecodeRGBInvoiceResponse, error) {
	resp, err := a.rgbClient.DecodeRGBInvoice(ctx, node_client.DecodeRGBInvoiceRequest{Invoice: invoice})
	if err != nil {
		return nil, wrapErr("/decodergbinvoice", err)
	}
	if resp.ExpirationTimestamp != nil && time.Now().UTC().Unix() >= *resp.ExpirationTimestamp {
		return nil, errors.New("rgb invoice already expired")
	}
	return &resp, nil
}

func (a *API) runCron(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.CronEvery)
	defer ticker.Stop()
	a.runCronTick(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runCronTick(ctx)
		}
	}
}

func (a *API) runCronTick(ctx context.Context) {
	if err := a.reconcileChannels(ctx); err != nil {
		log.Printf("cron reconcileChannels: %v", err)
	}
	if err := a.maintainUtxos(ctx); err != nil {
		log.Printf("cron maintainUtxos: %v", err)
	}
	if err := a.monitorOnchainSend(ctx); err != nil {
		log.Printf("cron monitorOnchainSend: %v", err)
	}
	if err := a.monitorLightningReceive(ctx); err != nil {
		log.Printf("cron monitorLightningReceive: %v", err)
	}
	a.runAsyncOrderOutbox(ctx)
}

func (a *API) reconcileChannels(ctx context.Context) error {
	conns, err := a.getConnections(ctx)
	if err != nil {
		return wrapErr("/listconnections", err)
	}

	chans, err := a.lspClient.ListChannels(ctx)
	if err != nil {
		return wrapErr("/listchannels", err)
	}

	existing := make(map[string]struct{}, len(chans.Channels))
	for _, c := range chans.Channels {
		existing[channelKey(c.PeerPubkey, c.AssetID)] = struct{}{}
	}

	for _, c := range conns {
		peerKey := peerOnly(c.PeerPubkeyAndOptAddr)
		if peerKey != "" {
			if _, err := a.ensureLightningAddressAccount(ctx, peerKey); err != nil {
				log.Printf("ensure lightning address account for %s: %v", peerKey, err)
			}
		}

		if !a.isSupportedAsset(c.AssetID) {
			log.Printf("skip openchannel for unsupported asset_id: %v", c.AssetID)
			continue
		}

		if _, ok := existing[channelKey(peerKey, c.AssetID)]; ok {
			continue
		}

		req, err := a.openChannelRequest(c)
		if err != nil {
			log.Printf("skip openchannel payload: %v", err)
			continue
		}
		if _, err := a.lspClient.OpenChannel(ctx, req); err != nil {
			log.Printf("openchannel failed for %s: %v", c.PeerPubkeyAndOptAddr, err)
		}
	}
	return nil
}

func (a *API) isSupportedAsset(assetID *string) bool {
	if assetID == nil {
		// Nil asset means BTC channel; allow it.
		return true
	}
	id := strings.TrimSpace(*assetID)
	if id == "" {
		// Empty asset means BTC channel; allow it.
		return true
	}
	for _, supported := range a.cfg.SupportedAssetIDs {
		if id == supported {
			return true
		}
	}
	return false
}

func (a *API) ensureAssetSupported(assetID string) error {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return errors.New("asset_id is required")
	}
	if len(a.cfg.SupportedAssetIDs) == 0 {
		return errors.New("SUPPORTED_ASSET_IDS is not configured on server")
	}
	for _, supported := range a.cfg.SupportedAssetIDs {
		if assetID == supported {
			return nil
		}
	}
	return fmt.Errorf("asset_id is not supported: %s", assetID)
}

func (a *API) monitorOnchainSend(ctx context.Context) error {
	recs, err := a.db.ListOnchainPending(ctx, 200)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.LNExpiresAt != nil && time.Now().UTC().After(*r.LNExpiresAt) {
			a.cancelLNInvoice(ctx, r.LspLNInvoice)
			_ = a.db.UpdateOnchainStatus(ctx, r.ID, statusExpired, "ln invoice expired")
			continue
		}

		status, err := a.lnInvoiceStatus(ctx, r.LspLNInvoice)
		if err != nil {
			log.Printf("invoicestatus(%d): %v", r.ID, err)
			continue
		}

		switch normalizeStatus(status) {
		case "succeeded":
			if err := a.sendRGBByInvoice(ctx, r.UserRGBInvoice); err != nil {
				_ = a.db.UpdateOnchainStatus(ctx, r.ID, statusFailed, err.Error())
			} else {
				_ = a.db.UpdateOnchainStatus(ctx, r.ID, statusCompleted, "")
			}
		case "failed":
			_ = a.db.UpdateOnchainStatus(ctx, r.ID, statusFailed, "lsp ln invoice failed")
		case "expired":
			a.cancelLNInvoice(ctx, r.LspLNInvoice)
			_ = a.db.UpdateOnchainStatus(ctx, r.ID, statusExpired, "lsp ln invoice expired")
		}
	}
	return nil
}

func (a *API) maintainUtxos(ctx context.Context) error {
	shouldCreate, createNum, err := utxoMaintenanceDecision(a.cfg.UtxoMinCount, a.cfg.UtxoTargetCount)
	if err != nil {
		return err
	}
	if !shouldCreate {
		return nil
	}

	unspents, err := a.rgbClient.ListUnspents(ctx, node_client.ListUnspentsRequest{SkipSync: a.cfg.UtxoSkipSync})
	if err != nil {
		return wrapErr("/listunspents", err)
	}

	if uint32(len(unspents.Unspents)) >= a.cfg.UtxoMinCount {
		return nil
	}

	size := a.cfg.UtxoSizeSat
	num := createNum
	req := node_client.CreateUtxosRequest{
		UpTo:     false,
		Num:      &num,
		Size:     &size,
		FeeRate:  a.cfg.UtxoFeeRate,
		SkipSync: a.cfg.UtxoSkipSync,
	}
	if err := a.lspClient.CreateUtxos(ctx, req); err != nil {
		return wrapErr("/createutxos", err)
	}
	return nil
}

func (a *API) monitorLightningReceive(ctx context.Context) error {
	recs, err := a.db.ListLightningPending(ctx, 200)
	if err != nil {
		return err
	}
	if err := a.refreshTransfers(ctx); err != nil {
		return wrapErr("/refreshtransfers", err)
	}
	for _, r := range recs {
		if r.RGBExpiresAt != nil && time.Now().UTC().After(*r.RGBExpiresAt) {
			_ = a.db.UpdateLightningStatus(ctx, r.ID, statusExpired, "rgb invoice expired")
			continue
		}

		status, err := a.transferStatusByIdx(ctx, r.RGBAssetID, r.BatchTransferIdx)
		if err != nil {
			log.Printf("transfer status (%d): %v", r.ID, err)
			continue
		}

		switch normalizeStatus(status) {
		case "succeeded":
			if err := a.sendLNByInvoice(ctx, r.UserLNInvoice); err != nil {
				_ = a.db.UpdateLightningStatus(ctx, r.ID, statusFailed, err.Error())
			} else {
				_ = a.db.UpdateLightningStatus(ctx, r.ID, statusCompleted, "")
			}
		case "settled":
			if err := a.sendLNByInvoice(ctx, r.UserLNInvoice); err != nil {
				_ = a.db.UpdateLightningStatus(ctx, r.ID, statusFailed, err.Error())
			} else {
				_ = a.db.UpdateLightningStatus(ctx, r.ID, statusCompleted, "")
			}
		case "failed":
			_ = a.db.UpdateLightningStatus(ctx, r.ID, statusFailed, "rgb invoice failed")
		case "expired":
			_ = a.db.UpdateLightningStatus(ctx, r.ID, statusExpired, "rgb invoice expired")
		}
	}
	return nil
}

func (a *API) lnInvoiceStatus(ctx context.Context, invoice string) (string, error) {
	out, err := a.lspClient.InvoiceStatus(ctx, node_client.InvoiceStatusRequest{Invoice: invoice})
	if err != nil {
		return "", err
	}
	return out.Status, nil
}

func (a *API) sendRGBByInvoice(ctx context.Context, rgbInvoice string) error {
	decoded, err := a.validateRGBInvoice(ctx, rgbInvoice)
	if err != nil {
		return err
	}
	if decoded.AssetID == nil || *decoded.AssetID == "" {
		return errors.New("rgb invoice has no asset_id")
	}

	_, err = a.lspClient.SendRGB(ctx, node_client.SendRGBRequest{
		Donation:         false,
		FeeRate:          a.cfg.SendRGBFeeRate,
		MinConfirmations: a.cfg.MinConfirmations,
		SkipSync:         false,
		RecipientMap: map[string][]node_client.SendRGBRecipient{
			*decoded.AssetID: {
				{
					RecipientID:        decoded.RecipientID,
					Assignment:         decoded.Assignment,
					TransportEndpoints: decoded.TransportEndpoints,
				},
			},
		},
	})
	return err
}

func (a *API) sendLNByInvoice(ctx context.Context, lnInvoice string) error {
	if a.lspClient == nil {
		return errors.New("lsp client is not configured")
	}
	_, err := a.lspClient.SendPayment(ctx, node_client.SendPaymentRequest{Invoice: lnInvoice})
	return err
}

func (a *API) aPayClaimInboundInvoice(ctx context.Context, paymentHash, paymentPreimage string) error {
	if a.lspClient == nil {
		return errors.New("lsp client is not configured")
	}
	_, err := a.lspClient.ClaimHodlInvoice(ctx, node_client.ClaimHodlInvoiceRequest{
		PaymentHash:     paymentHash,
		PaymentPreimage: paymentPreimage,
	})
	return err
}

func (a *API) refreshTransfers(ctx context.Context) error {
	return a.rgbClient.RefreshTransfers(ctx, node_client.RefreshTransfersRequest{SkipSync: false})
}

func (a *API) transferStatusByIdx(ctx context.Context, assetID string, batchTransferIdx int64) (string, error) {
	out, err := a.rgbClient.ListTransfers(ctx, node_client.ListTransfersRequest{AssetID: assetID})
	if err != nil {
		return "", err
	}
	for _, t := range out.Transfers {
		if t.Idx == batchTransferIdx {
			return string(t.Status), nil
		}
	}
	return "", fmt.Errorf("transfer idx %d not found for asset %s", batchTransferIdx, assetID)
}

func (a *API) getConnections(ctx context.Context) ([]node_client.Connection, error) {
	connectionsResp, err := a.lspClient.ListConnections(ctx)
	if err == nil {
		return connectionsResp.Connections, nil
	}

	var apiErr *node_client.APIError
	if !errors.As(err, &apiErr) || (apiErr.Code != http.StatusMethodNotAllowed && apiErr.Code != http.StatusNotFound) {
		return nil, err
	}

	// When listconnections is unavailable and we fall back to listpeers,
	// synthesize RGB channel intents from server allowlist so we don't
	// accidentally open BTC-only channels for RGB flows.
	resp, err := a.lspClient.ListPeers(ctx)
	if err != nil {
		return nil, err
	}

	publicByDefault := a.cfg.DefaultVirtualOpenMode == ""
	conns := make([]node_client.Connection, 0, len(resp.Peers))
	for _, p := range resp.Peers {
		if len(a.cfg.SupportedAssetIDs) > 0 {
			for _, assetID := range a.cfg.SupportedAssetIDs {
				assetIDCopy := assetID
				assetAmount := a.cfg.DefaultChannelAssetAmount
				conns = append(conns, node_client.Connection{
					PeerPubkeyAndOptAddr: p.Pubkey,
					CapacitySat:          a.cfg.DefaultChannelCapacitySat,
					PushMsat:             a.cfg.DefaultChannelPushMsat,
					AssetID:              &assetIDCopy,
					AssetAmount:          &assetAmount,
					Public:               publicByDefault,
					WithAnchors:          true,
				})
			}
			continue
		}

		conns = append(conns, node_client.Connection{
			PeerPubkeyAndOptAddr: p.Pubkey,
			CapacitySat:          a.cfg.DefaultChannelCapacitySat,
			PushMsat:             a.cfg.DefaultChannelPushMsat,
			Public:               publicByDefault,
			WithAnchors:          true,
		})
	}
	return conns, nil
}

func (a *API) cancelLNInvoice(ctx context.Context, lnInvoice string) {
	decoded, err := a.validateLNInvoice(ctx, lnInvoice)
	if err != nil || strings.TrimSpace(decoded.PaymentHash) == "" {
		return
	}
	_ = a.lspClient.CancelInvoice(ctx, node_client.CancelInvoiceRequest{PaymentHash: decoded.PaymentHash})
}

func (a *API) openChannelRequest(c node_client.Connection) (node_client.OpenChannelRequest, error) {
	inbound := uint64(0)
	if c.AssetDecimals != nil {
		mul := uint64(1)
		for i := 0; i < int(*c.AssetDecimals); i++ {
			mul *= 10
		}
		if mul > 0 {
			inbound = 1_000_000 * mul
		}
	}

	req := node_client.OpenChannelRequest{
		PeerPubkeyAndOptAddr: c.PeerPubkeyAndOptAddr,
		CapacitySat:          c.CapacitySat,
		PushMsat:             c.PushMsat,
		AssetID:              c.AssetID,
		Public:               c.Public,
		WithAnchors:          c.WithAnchors,
	}
	if len(c.OpenChannelParams) > 0 {
		if err := json.Unmarshal(c.OpenChannelParams, &req); err != nil {
			return node_client.OpenChannelRequest{}, err
		}
		if strings.TrimSpace(req.PeerPubkeyAndOptAddr) == "" {
			req.PeerPubkeyAndOptAddr = c.PeerPubkeyAndOptAddr
		}
		if req.CapacitySat == 0 {
			req.CapacitySat = c.CapacitySat
		}
		if req.PushMsat == 0 {
			req.PushMsat = c.PushMsat
		}
		if c.AssetID != nil {
			if req.AssetID == nil || strings.TrimSpace(*req.AssetID) == "" {
				req.AssetID = c.AssetID
			}
			if req.AssetAmount == nil {
				assetAmount := c.AssetAmount
				if assetAmount == nil && a.cfg.DefaultChannelAssetAmount > 0 {
					v := a.cfg.DefaultChannelAssetAmount
					assetAmount = &v
				}
				req.AssetAmount = assetAmount
			}
		}
		if inbound > 0 && req.PushAssetAmount == nil {
			req.PushAssetAmount = &inbound
		}
		if a.cfg.DefaultVirtualOpenMode != "" {
			if req.VirtualOpenMode == nil {
				mode := a.cfg.DefaultVirtualOpenMode
				req.VirtualOpenMode = &mode
			}
			req.Public = false
		}
		return req, nil
	}
	if c.AssetID != nil {
		assetAmount := c.AssetAmount
		if assetAmount == nil && a.cfg.DefaultChannelAssetAmount > 0 {
			v := a.cfg.DefaultChannelAssetAmount
			assetAmount = &v
		}
		req.AssetAmount = assetAmount
	}
	if inbound > 0 {
		req.PushAssetAmount = &inbound
	}
	if a.cfg.DefaultVirtualOpenMode != "" {
		mode := a.cfg.DefaultVirtualOpenMode
		req.VirtualOpenMode = &mode
		// RLN requires virtual channels to be private.
		req.Public = false
	}
	return req, nil
}

func channelKey(peer string, assetID *string) string {
	asset := ""
	if assetID != nil {
		asset = *assetID
	}
	return peer + "|" + asset
}

func peerOnly(peerPubkeyAndOptAddr string) string {
	parts := strings.SplitN(peerPubkeyAndOptAddr, "@", 2)
	return parts[0]
}

func normalizeStatus(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func applyAndValidateOnchainAssetParams(ln *LNInvoiceInput, decoded *node_client.DecodeRGBInvoiceResponse) error {
	if ln == nil || decoded == nil {
		return nil
	}

	decodedAssetID := ""
	if decoded.AssetID != nil {
		decodedAssetID = strings.TrimSpace(*decoded.AssetID)
	}
	if ln.AssetID != nil {
		reqAssetID := strings.TrimSpace(*ln.AssetID)
		if decodedAssetID == "" || reqAssetID != decodedAssetID {
			return errors.New("lninvoice.asset_id must match rgb_invoice asset_id")
		}
	} else if decodedAssetID != "" {
		assetIDCopy := decodedAssetID
		ln.AssetID = &assetIDCopy
	}

	decodedAmount, hasDecodedAmount := extractFungibleAssignmentAmount(decoded.Assignment)
	if ln.AssetAmount != nil {
		if !hasDecodedAmount || *ln.AssetAmount != decodedAmount {
			return errors.New("lninvoice.asset_amount must match rgb_invoice assignment amount")
		}
	} else if hasDecodedAmount {
		amountCopy := decodedAmount
		ln.AssetAmount = &amountCopy
	}

	return nil
}

func extractFungibleAssignmentAmount(assignment any) (uint64, bool) {
	m, ok := assignment.(map[string]any)
	if !ok {
		return 0, false
	}

	rawType, ok := m["type"].(string)
	if !ok || !strings.EqualFold(strings.TrimSpace(rawType), "fungible") {
		return 0, false
	}

	rawValue, ok := m["value"]
	if !ok {
		return 0, false
	}
	return parseUint64(rawValue)
}

func parseUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case uint32:
		return uint64(n), true
	case uint16:
		return uint64(n), true
	case uint8:
		return uint64(n), true
	case int64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case int32:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case float64:
		if n < 0 || n != float64(uint64(n)) {
			return 0, false
		}
		return uint64(n), true
	case json.Number:
		u, err := strconv.ParseUint(n.String(), 10, 64)
		return u, err == nil
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
		return u, err == nil
	default:
		return 0, false
	}
}

func ensureLNInvoiceInputMinAmount(ln *LNInvoiceInput, minAmtMsat uint64) error {
	if ln == nil || minAmtMsat == 0 {
		return nil
	}
	if ln.AmtMsat == nil {
		minCopy := minAmtMsat
		ln.AmtMsat = &minCopy
		return nil
	}
	if *ln.AmtMsat < minAmtMsat {
		return fmt.Errorf("lninvoice.amt_msat must be >= %d", minAmtMsat)
	}
	return nil
}

func ensureDecodedLNMinAmount(decoded *node_client.DecodeLNInvoiceResponse, minAmtMsat uint64) error {
	if decoded == nil || minAmtMsat == 0 {
		return nil
	}
	if decoded.AmtMsat <= 0 {
		return errors.New("ln_invoice must have fixed amount")
	}
	if uint64(decoded.AmtMsat) < minAmtMsat {
		return fmt.Errorf("ln_invoice amount must be >= %d msat", minAmtMsat)
	}
	return nil
}

func utxoMaintenanceDecision(minCount, targetCount uint32) (bool, uint8, error) {
	if minCount == 0 || targetCount == 0 {
		return false, 0, nil
	}
	if targetCount <= minCount {
		return false, 0, errors.New("UTXO_TARGET_COUNT must be greater than UTXO_MIN_COUNT")
	}
	createNum := targetCount - minCount
	if createNum > 255 {
		return false, 0, errors.New("UTXO_TARGET_COUNT-UTXO_MIN_COUNT must fit in uint8")
	}
	return true, uint8(createNum), nil
}

func alignAndValidateRGBDurationWithLN(params *RGBInvoiceInput, decodedLN *node_client.DecodeLNInvoiceResponse, now time.Time, toleranceSec uint32) error {
	if params == nil || decodedLN == nil {
		return nil
	}
	expiresAt := decodedLN.Timestamp + decodedLN.ExpirySec
	remaining := expiresAt - now.Unix()
	if remaining <= 0 {
		return errors.New("ln_invoice already expired")
	}
	if remaining > int64(^uint32(0)) {
		return errors.New("ln_invoice expiration is too far in the future")
	}
	expected := uint32(remaining)

	if params.DurationSeconds == nil || *params.DurationSeconds == 0 {
		v := expected
		params.DurationSeconds = &v
		return nil
	}

	var diff uint32
	if *params.DurationSeconds >= expected {
		diff = *params.DurationSeconds - expected
	} else {
		diff = expected - *params.DurationSeconds
	}
	if diff > toleranceSec {
		return fmt.Errorf("rgb_invoice.duration_seconds must match ln_invoice remaining lifetime (expected ~%d, got %d, tolerance %d)", expected, *params.DurationSeconds, toleranceSec)
	}
	return nil
}

func applyAndValidateRGBAssignment(params *RGBInvoiceInput, defaultAssignment string) error {
	if params == nil {
		return nil
	}
	defaultAssignment = strings.TrimSpace(defaultAssignment)
	if defaultAssignment == "" {
		defaultAssignment = "Value"
	}
	if params.Assignment == nil {
		assignment := defaultAssignment
		params.Assignment = &assignment
		return nil
	}

	incoming := strings.TrimSpace(*params.Assignment)
	if incoming == "" {
		assignment := defaultAssignment
		params.Assignment = &assignment
		return nil
	}
	if !strings.EqualFold(incoming, "Any") && !strings.EqualFold(incoming, "Value") {
		return errors.New("rgb_invoice.assignment must be \"Any\" or \"Value\"")
	}
	assignment := "Any"
	params.Assignment = &assignment
	return nil
}

func rgbAssignmentJSON(assignment *string) (map[string]any, error) {
	if assignment == nil {
		return map[string]any{"type": "Any"}, nil
	}
	v := strings.TrimSpace(*assignment)
	if v == "" || strings.EqualFold(v, "Any") || strings.EqualFold(v, "Value") {
		return map[string]any{"type": "Any"}, nil
	}
	return nil, errors.New("unsupported rgb assignment")
}

func applyBackendMinConfirmations(params *RGBInvoiceInput, backendMin uint8) {
	if params == nil {
		return
	}
	params.MinConfirmations = backendMin
}

func alignAndValidateLNExpiryWithRGB(ln *LNInvoiceInput, decoded *node_client.DecodeRGBInvoiceResponse, now time.Time, toleranceSec uint32) error {
	if ln == nil || decoded == nil {
		return nil
	}
	if decoded.ExpirationTimestamp == nil {
		return errors.New("rgb_invoice must contain expiration_timestamp")
	}

	remaining := *decoded.ExpirationTimestamp - now.Unix()
	if remaining <= 0 {
		return errors.New("rgb invoice already expired")
	}
	if remaining > int64(^uint32(0)) {
		return errors.New("rgb invoice expiration is too far in the future")
	}

	expected := uint32(remaining)
	if ln.ExpirySec == 0 {
		ln.ExpirySec = expected
		return nil
	}

	var diff uint32
	if ln.ExpirySec >= expected {
		diff = ln.ExpirySec - expected
	} else {
		diff = expected - ln.ExpirySec
	}
	if diff > toleranceSec {
		return fmt.Errorf("lninvoice.expiry_sec must match rgb invoice remaining lifetime (expected ~%d, got %d, tolerance %d)", expected, ln.ExpirySec, toleranceSec)
	}
	return nil
}

func unixFromTimestampAndExpiry(ts, exp int64) time.Time {
	return time.Unix(ts+exp, 0).UTC()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOKJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeRawJSON(w http.ResponseWriter, code int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
