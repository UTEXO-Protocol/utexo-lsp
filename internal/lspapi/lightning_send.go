package lspapi

// POST /lightning_send — pay a third party's BOLT11 out of an asset the caller
// does not hold.
//
//	caller --(what it holds, HODL)--> LSP          inbound leg, quoted here
//	LSP    --(what the invoice asks)-> third party  outbound leg, its own invoice
//
// Atomicity is the shared payment hash: the HODL invoice carries the third
// party's hash, so the only way to claim what the caller paid is a preimage only
// the third party can release, and it releases it only on being paid. The
// caller's exposure is griefing (an HTLC held to expiry), never theft.
//
// The exposure runs the other way too, which is why paying the outbound leg is
// guarded by the claim deadline: once the third party is paid, an inbound HODL
// this LSP can no longer claim is a straight loss.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"utexo-lsp/pkg/node_client"
)

type lightningSendQuote struct {
	decoded  *node_client.DecodeLNInvoiceResponse
	legs     invoiceAssetPair
	inMsat   uint64
	feeMsat  uint64
	expiry   time.Duration
	deadline time.Time
}

func (a *API) handleLightningSend(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.LightningSendEnabled {
		writeErr(w, http.StatusNotFound, "lightning_send is not enabled on this LSP")
		return
	}

	var req LightningSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	invoice := strings.TrimSpace(req.Invoice)
	if invoice == "" {
		writeErr(w, http.StatusBadRequest, "invoice is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	quote, err := a.quoteLightningSend(ctx, invoice, req.PayWithAssetID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Only now is anything created: every rejection above costs nothing.
	inboundInvoice, err := a.requestLightningSendHodlInvoice(ctx, quote)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("error constructing invoice: %v", err))
		return
	}

	rec := LightningSendRecord{
		PaymentHash:         quote.decoded.PaymentHash,
		OutboundInvoice:     invoice,
		OutboundAssetID:     quote.legs.OutboundAssetID,
		OutboundAssetAmount: quote.legs.OutboundAssetAmount,
		OutboundAmountMsat:  uint64(quote.decoded.AmtMsat),
		PayeePubkey:         quote.decoded.PayeePubkey,
		InboundInvoice:      inboundInvoice,
		InboundAssetID:      quote.legs.InboundAssetID,
		InboundAssetAmount:  quote.legs.InboundAssetAmount,
		InboundAmountMsat:   quote.inMsat,
		Converted:           quote.legs.Converted,
		Status:              lightningSendStateQuoted,
		ExpiresAt:           quote.deadline,
	}
	if _, err := a.db.InsertLightningSend(ctx, rec); err != nil {
		// Nothing will ever claim this invoice, so fail it back rather than leave a
		// claimable HODL with no record behind it.
		a.cancelLightningSendHodl(ctx, quote.decoded.PaymentHash)
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("error persisting lightning send: %v", err))
		return
	}

	if quote.legs.Converted {
		log.Printf("lightning_send: quoting %s and delivering %s to %s (1:1, hash %s)",
			optionalAssetID(quote.legs.InboundAssetID), optionalAssetID(quote.legs.OutboundAssetID),
			quote.decoded.PayeePubkey, quote.decoded.PaymentHash)
	}

	writeJSON(w, http.StatusOK, LightningSendResponse{
		LNInvoice:   inboundInvoice,
		PaymentHash: quote.decoded.PaymentHash,
		Inbound: LightningSendLeg{
			AssetID:     optionalAssetID(quote.legs.InboundAssetID),
			AssetAmount: derefUint64(quote.legs.InboundAssetAmount),
			AmountMsat:  quote.inMsat,
		},
		Outbound: LightningSendLeg{
			AssetID:     optionalAssetID(quote.legs.OutboundAssetID),
			AssetAmount: derefUint64(quote.legs.OutboundAssetAmount),
			AmountMsat:  uint64(quote.decoded.AmtMsat),
			PayeePubkey: quote.decoded.PayeePubkey,
		},
		Converted: quote.legs.Converted,
		FeeMsat:   quote.feeMsat,
		ExpiresAt: quote.deadline.Unix(),
	})
}

func (a *API) handleLightningSendStatus(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.LightningSendEnabled {
		writeErr(w, http.StatusNotFound, "lightning_send is not enabled on this LSP")
		return
	}
	paymentHash := strings.ToLower(strings.TrimSpace(r.PathValue("payment_hash")))
	if !isValidPaymentHash(paymentHash) {
		writeErr(w, http.StatusBadRequest, "invalid payment_hash")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()

	rec, err := a.db.LoadLightningSendByPaymentHash(ctx, paymentHash)
	if err != nil {
		if errors.Is(err, errLightningSendNotFound) {
			writeErr(w, http.StatusNotFound, "lightning send not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := LightningSendStatusResponse{PaymentHash: rec.PaymentHash, Status: rec.Status}
	if rec.LastError != nil {
		resp.Reason = *rec.LastError
	}
	writeJSON(w, http.StatusOK, resp)
}

// quoteLightningSend runs every rejection before anything is created: a request
// that cannot be served must cost neither a HODL invoice nor a row.
func (a *API) quoteLightningSend(ctx context.Context, invoice, payWithAssetID string) (lightningSendQuote, error) {
	decoded, err := a.validateLNInvoice(ctx, invoice)
	if err != nil {
		return lightningSendQuote{}, err
	}

	identity, err := a.nodeIdentity(ctx)
	if err != nil {
		return lightningSendQuote{}, fmt.Errorf("cannot resolve node identity: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(decoded.Network), identity.network) {
		return lightningSendQuote{}, fmt.Errorf("invoice is for %s, this LSP is on %s", decoded.Network, identity.network)
	}
	// Paying ourselves would have the node hold and settle the same hash on both
	// sides, which no preimage unlocks.
	if normalizePeerPubkey(decoded.PayeePubkey) == normalizePeerPubkey(identity.pubkey) {
		return lightningSendQuote{}, errors.New("invoice is payable to this LSP itself")
	}

	// An amountless invoice would leave the LSP choosing how much to pay.
	if decoded.AmtMsat <= 0 {
		return lightningSendQuote{}, errors.New("invoice must carry an amount")
	}
	if decoded.AssetID == "" || decoded.AssetAmount <= 0 {
		return lightningSendQuote{}, errors.New("invoice must carry an rgb asset and asset_amount")
	}
	if a.cfg.LightningSendMaxAssetAmount > 0 && uint64(decoded.AssetAmount) > a.cfg.LightningSendMaxAssetAmount {
		return lightningSendQuote{}, fmt.Errorf("asset amount %d exceeds the per-payment limit of %d",
			decoded.AssetAmount, a.cfg.LightningSendMaxAssetAmount)
	}

	legs, err := a.resolveLightningSendAssetPair(ctx, decoded, payWithAssetID)
	if err != nil {
		return lightningSendQuote{}, err
	}

	// canDeliverNow admits only a direct channel to the payee, which is also what
	// makes the CLTV budget below a one-hop calculation.
	if reason, ok := a.canDeliverNow(ctx, decoded); !ok {
		return lightningSendQuote{}, fmt.Errorf("cannot deliver this payment: %s", reason)
	}

	// The claim deadline LDK gives us is already ldkHtlcFailBackBuffer short of the
	// inbound invoice's final delta, and create_bolt11_invoice added
	// ldkMinFinalCltvBuffer to what we asked for. What is left has to cover the
	// outbound leg's own final delta plus a margin for the outbox.
	budget := int64(a.cfg.APayInboundMinFinalCltvExpiryDelta) - ldkHtlcFailBackBuffer - ldkMinFinalCltvBuffer
	need := int64(decoded.MinFinalCltvExpiryDelta) + int64(a.cfg.claimMarginBlocks())
	if need > budget {
		return lightningSendQuote{}, fmt.Errorf(
			"invoice needs %d blocks of cltv (final delta %d + margin %d) but only %d are available; "+
				"raise APAY_INBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA or ask for an invoice with a shorter final delta",
			need, decoded.MinFinalCltvExpiryDelta, a.cfg.claimMarginBlocks(), budget)
	}

	// The node rejects a duplicate hash itself, but a 400 naming the reason beats a
	// 500 out of /lninvoice.
	paymentHash := strings.ToLower(strings.TrimSpace(decoded.PaymentHash))
	if !isValidPaymentHash(paymentHash) {
		return lightningSendQuote{}, errors.New("invoice carries an invalid payment_hash")
	}
	if err := a.assertLightningSendHashFree(ctx, paymentHash); err != nil {
		return lightningSendQuote{}, err
	}
	decoded.PaymentHash = paymentHash

	// Expiring after the invoice it funds would leave the caller able to pay for a
	// delivery that can no longer happen.
	expiry := a.cfg.APayInboundInvoiceExpiry
	outboundExpiresAt := time.Unix(decoded.Timestamp+decoded.ExpirySec, 0).UTC()
	if until := time.Until(outboundExpiresAt); until < expiry {
		expiry = until
	}
	if expiry <= 0 {
		return lightningSendQuote{}, errors.New("invoice expires too soon to be relayed")
	}

	return lightningSendQuote{
		decoded:  decoded,
		legs:     legs,
		inMsat:   uint64(decoded.AmtMsat) + a.cfg.LightningSendFeeMsat,
		feeMsat:  a.cfg.LightningSendFeeMsat,
		expiry:   expiry,
		deadline: time.Now().UTC().Add(expiry),
	}, nil
}

// resolveLightningSendAssetPair fixes the two legs. The outbound one is what the
// third party signed, so only the inbound one is open and it is the caller's to
// choose — it alone knows which of its channels has the liquidity.
//
// Omitting it asks the LSP to resolve it from CONVERTIBLE_PAIRS, and only
// unambiguously: one counterpart is taken, several are a 400, none stays put.
func (a *API) resolveLightningSendAssetPair(ctx context.Context, decoded *node_client.DecodeLNInvoiceResponse, payWithAssetID string) (invoiceAssetPair, error) {
	outbound := strings.TrimSpace(decoded.AssetID)
	assetAmount := uint64(decoded.AssetAmount)

	inbound := strings.TrimSpace(payWithAssetID)
	if inbound == "" {
		switch counterparts := a.convertibleCounterparts(outbound); len(counterparts) {
		case 0:
			inbound = outbound
		case 1:
			inbound = counterparts[0]
		default:
			return invoiceAssetPair{}, fmt.Errorf(
				"pay_with_asset_id is ambiguous: %s is convertible with %s — name one",
				outbound, strings.Join(counterparts, ", "))
		}
	}

	pair := invoiceAssetPair{
		InboundAssetID:      &inbound,
		InboundAssetAmount:  &assetAmount,
		OutboundAssetID:     &outbound,
		OutboundAssetAmount: &assetAmount, // 1:1, no spread — a spread is LIGHTNING_SEND_FEE_MSAT
	}

	if inbound == outbound {
		// No conversion, the LSP only fronts the payment — but still only in an
		// asset it delivers.
		if err := a.ensureAssetPayoutEligible(inbound); err != nil {
			return invoiceAssetPair{}, err
		}
		return pair, nil
	}
	if err := a.ensureConvertiblePair(ctx, inbound, outbound); err != nil {
		return invoiceAssetPair{}, fmt.Errorf("cannot quote %s for a payment delivered in %s: %w", inbound, outbound, err)
	}
	pair.Converted = true
	return pair, nil
}

// assertLightningSendHashFree refuses a hash already held in either flow: the
// first preimage would otherwise settle both inbound HTLCs.
func (a *API) assertLightningSendHashFree(ctx context.Context, paymentHash string) error {
	if _, err := a.db.LoadLightningSendByPaymentHash(ctx, paymentHash); err == nil {
		return errors.New("this invoice is already being relayed")
	} else if !errors.Is(err, errLightningSendNotFound) {
		return err
	}
	if _, err := a.db.LoadAsyncRotatingInvoiceByPaymentHash(ctx, paymentHash); err == nil {
		return errors.New("payment_hash is already reserved for an APay invoice")
	} else if !errors.Is(err, errAsyncInvoiceNotFound) {
		return err
	}
	return nil
}

func (a *API) requestLightningSendHodlInvoice(ctx context.Context, quote lightningSendQuote) (string, error) {
	payload := node_client.LNInvoiceRequest{
		AmtMsat:                 &quote.inMsat,
		ExpirySec:               uint32(quote.expiry.Seconds()),
		PaymentHash:             &quote.decoded.PaymentHash,
		MinFinalCltvExpiryDelta: &a.cfg.APayInboundMinFinalCltvExpiryDelta,
		AssetID:                 quote.legs.InboundAssetID,
		AssetAmount:             quote.legs.InboundAssetAmount,
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

// lightningSendOwnsHash routes the node's webhooks: both fire regardless of
// flow, so the hash is what decides. Disabled owns nothing — a hash can only
// have been recorded while the feature was on.
func (a *API) lightningSendOwnsHash(ctx context.Context, paymentHash string) (bool, error) {
	if !a.cfg.LightningSendEnabled {
		return false, nil
	}
	if _, err := a.db.LoadLightningSendByPaymentHash(ctx, paymentHash); err != nil {
		if errors.Is(err, errLightningSendNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// markLightningSendClaimable handles /internal/async_order/claimable: the
// caller's HTLC is held, so the delivery leg can be paid. The claim deadline is
// recorded here because this is the only moment LDK reports it, and every later
// decision to pay or refuse is made against it.
func (a *API) markLightningSendClaimable(ctx context.Context, paymentHash string, claimDeadlineHeight *uint32) error {
	rec, err := a.db.LoadLightningSendByPaymentHash(ctx, paymentHash)
	if err != nil {
		return err
	}
	if lightningSendStateAtOrBeyond(rec.Status, lightningSendStateClaimable) {
		return nil
	}

	changed, err := a.db.AdvanceLightningSend(ctx, paymentHash,
		[]LightningSendState{lightningSendStateQuoted},
		lightningSendStateClaimable,
		LightningSendUpdate{
			ClaimDeadlineHeight: claimDeadlineHeight,
			Enqueue:             lightningSendActionPayOutbound,
		})
	if err != nil {
		return err
	}
	if !changed {
		current, reloadErr := a.db.LoadLightningSendByPaymentHash(ctx, paymentHash)
		if reloadErr != nil {
			return reloadErr
		}
		if lightningSendStateAtOrBeyond(current.Status, lightningSendStateClaimable) {
			return nil
		}
		return fmt.Errorf("lightning send %s in unexpected status %q before claimable", paymentHash, current.Status)
	}
	return nil
}

// markLightningSendPreimage handles /internal/async_order/payment_sent. The
// handler already proved sha256(preimage) == payment_hash, so reaching here means
// the third party was paid and the inbound HODL is collectable.
func (a *API) markLightningSendPreimage(ctx context.Context, paymentHash, preimage string) error {
	changed, err := a.db.AdvanceLightningSend(ctx, paymentHash,
		[]LightningSendState{lightningSendStateOutboundPending, lightningSendStateOutboundPaid},
		lightningSendStateOutboundClaimed,
		LightningSendUpdate{
			PaymentPreimage: &preimage,
			Enqueue:         lightningSendActionClaimInbound,
		})
	if err != nil {
		return err
	}
	if changed {
		return nil
	}
	current, err := a.db.LoadLightningSendByPaymentHash(ctx, paymentHash)
	if err != nil {
		return err
	}
	if lightningSendStateAtOrBeyond(current.Status, lightningSendStateOutboundClaimed) {
		return nil
	}
	return fmt.Errorf("lightning send %s in unexpected status %q before outbound claim", paymentHash, current.Status)
}

// lightningSendPayOutboundJob pays the delivery leg — the one irreversible step,
// and the only one that can cost this LSP money. It refuses rather than retries
// near the claim deadline: paying a leg we can no longer collect against is worse
// than refunding the caller.
func (a *API) lightningSendPayOutboundJob(ctx context.Context, paymentHash string) error {
	jobCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
	defer cancel()

	rec, err := a.db.LoadLightningSendByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("load lightning send: %w", err)
	}
	if lightningSendStateAtOrBeyond(rec.Status, lightningSendStateOutboundPaid) {
		return nil
	}
	if lightningSendTerminal(rec.Status) {
		return nil
	}

	if err := a.lightningSendCanPay(jobCtx, rec); err != nil {
		a.failLightningSend(jobCtx, rec, err.Error())
		return nil
	}

	// Take ownership before touching the node: a concurrent job must not pay the
	// same invoice twice.
	changed, err := a.db.AdvanceLightningSend(jobCtx, paymentHash,
		[]LightningSendState{lightningSendStateClaimable},
		lightningSendStateOutboundPending, LightningSendUpdate{})
	if err != nil {
		return fmt.Errorf("claim outbound leg: %w", err)
	}
	if !changed {
		current, reloadErr := a.db.LoadLightningSendByPaymentHash(jobCtx, paymentHash)
		if reloadErr != nil {
			return reloadErr
		}
		if lightningSendStateAtOrBeyond(current.Status, lightningSendStateOutboundPending) {
			return nil
		}
		return fmt.Errorf("lightning send %s in unexpected status %q before outbound payment", paymentHash, current.Status)
	}

	if _, err := a.sendLNByInvoice(jobCtx, rec.OutboundInvoice); err != nil {
		if !errors.Is(err, errPaymentReportedFailed) {
			// Transport error: the node may or may not have started the payment, so
			// stay in outbound_pending and retry. markLightningSendPreimage accepts
			// that state precisely so a late PaymentSent still lands.
			return err
		}
		// Reported failed, so nothing was delivered: fail the HODL back rather than
		// hold the caller's money to expiry.
		a.refundLightningSend(jobCtx, rec, err.Error())
		return nil
	}

	if _, err := a.db.AdvanceLightningSend(jobCtx, paymentHash,
		[]LightningSendState{lightningSendStateOutboundPending},
		lightningSendStateOutboundPaid, LightningSendUpdate{}); err != nil {
		return fmt.Errorf("persist outbound_paid: %w", err)
	}
	return nil
}

// lightningSendClaimInboundJob collects the caller's HTLC with the preimage the
// third party released.
func (a *API) lightningSendClaimInboundJob(ctx context.Context, paymentHash string) error {
	jobCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
	defer cancel()

	rec, err := a.db.LoadLightningSendByPaymentHash(jobCtx, paymentHash)
	if err != nil {
		return fmt.Errorf("load lightning send: %w", err)
	}
	if lightningSendStateAtOrBeyond(rec.Status, lightningSendStateSettled) {
		return nil
	}
	if rec.Status != lightningSendStateOutboundClaimed {
		return fmt.Errorf("lightning send %s in unexpected status %q before inbound claim", paymentHash, rec.Status)
	}
	if rec.PaymentPreimage == nil || strings.TrimSpace(*rec.PaymentPreimage) == "" {
		return errors.New("payment_preimage is missing")
	}

	if err := a.aPayClaimInboundInvoice(jobCtx, paymentHash, *rec.PaymentPreimage); err != nil {
		return err
	}

	if _, err := a.db.AdvanceLightningSend(jobCtx, paymentHash,
		[]LightningSendState{lightningSendStateOutboundClaimed},
		lightningSendStateSettled, LightningSendUpdate{}); err != nil {
		return fmt.Errorf("persist settled: %w", err)
	}
	return nil
}

// lightningSendCanPay is the last gate before the irreversible step: both halves
// of the answer are refusals to pay, not warnings.
func (a *API) lightningSendCanPay(ctx context.Context, rec LightningSendRecord) error {
	if rec.ClaimDeadlineHeight == nil || *rec.ClaimDeadlineHeight == 0 {
		return errors.New("no claim deadline recorded for the held htlc")
	}

	// A decode failure is not advisory — an expired delivery invoice is the likely
	// one, and paying it spends against something the payee will reject while the
	// caller's HTLC is already held.
	decoded, err := a.validateLNInvoice(ctx, rec.OutboundInvoice)
	if err != nil {
		return fmt.Errorf("delivery invoice is no longer payable: %w", err)
	}

	// One hop, so the delivery leg costs its own final delta plus the outbox margin.
	required := uint64(a.cfg.claimMarginBlocks()) + decoded.MinFinalCltvExpiryDelta
	return a.validateAsyncOrderClaimDeadlineWithinPolicy(ctx, *rec.ClaimDeadlineHeight, required)
}

// refundLightningSend fails the held HTLC back, so the caller gets its money now
// instead of at CLTV expiry. Terminal, so the outbox stops.
func (a *API) refundLightningSend(ctx context.Context, rec LightningSendRecord, reason string) {
	a.cancelLightningSendHodl(ctx, rec.PaymentHash)
	if _, err := a.db.AdvanceLightningSend(ctx, rec.PaymentHash,
		[]LightningSendState{
			lightningSendStateQuoted,
			lightningSendStateClaimable,
			lightningSendStateOutboundPending,
		},
		lightningSendStateCancelled,
		LightningSendUpdate{LastError: &reason}); err != nil {
		log.Printf("lightning_send: persist cancelled for %s: %v", rec.PaymentHash, err)
	}
}

// failLightningSend is refundLightningSend for a state we must not retry out of.
// It still cancels the HODL: the caller is not at fault.
func (a *API) failLightningSend(ctx context.Context, rec LightningSendRecord, reason string) {
	a.cancelLightningSendHodl(ctx, rec.PaymentHash)
	if _, err := a.db.AdvanceLightningSend(ctx, rec.PaymentHash,
		[]LightningSendState{
			lightningSendStateQuoted,
			lightningSendStateClaimable,
			lightningSendStateOutboundPending,
		},
		lightningSendStateFailed,
		LightningSendUpdate{LastError: &reason}); err != nil {
		log.Printf("lightning_send: persist failed for %s: %v", rec.PaymentHash, err)
	}
	log.Printf("lightning_send: %s refused: %s", rec.PaymentHash, reason)
}

func (a *API) cancelLightningSendHodl(ctx context.Context, paymentHash string) {
	if a.lspClient == nil {
		return
	}
	if err := a.lspClient.CancelHodlInvoice(ctx, node_client.CancelHodlInvoiceRequest{PaymentHash: paymentHash}); err != nil {
		// Not fatal: an unpaid HODL has nothing to fail back, and the node says so.
		log.Printf("lightning_send: cancel hodl invoice %s: %v", paymentHash, err)
	}
}

func derefUint64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}
