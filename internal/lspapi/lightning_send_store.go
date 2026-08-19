package lspapi

// Persistence for POST /lightning_send.
//
// Three methods rather than the ten Mark* transitions async_rotating_invoices
// grew: this flow has no reservation and no rotation, so every state change is
// the same compare-and-set.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var errLightningSendNotFound = errors.New("lightning send not found")

func (s *SQLStore) InsertLightningSend(ctx context.Context, rec LightningSendRecord) (int64, error) {
	if s.driver == "postgres" {
		return 0, errors.New("lightning_send is not supported on postgres")
	}
	paymentHash := strings.ToLower(strings.TrimSpace(rec.PaymentHash))
	if !isValidPaymentHash(paymentHash) {
		return 0, errors.New("invalid payment_hash")
	}
	if rec.Status == "" {
		rec.Status = lightningSendStateQuoted
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO lightning_send_mappings (
			payment_hash, outbound_invoice, outbound_asset_id, outbound_asset_amount,
			outbound_amount_msat, payee_pubkey, inbound_invoice, inbound_asset_id,
			inbound_asset_amount, inbound_amount_msat, converted, status, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		paymentHash, rec.OutboundInvoice, rec.OutboundAssetID, rec.OutboundAssetAmount,
		rec.OutboundAmountMsat, rec.PayeePubkey, rec.InboundInvoice, rec.InboundAssetID,
		rec.InboundAssetAmount, rec.InboundAmountMsat, rec.Converted, rec.Status, rec.ExpiresAt.UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *SQLStore) LoadLightningSendByPaymentHash(ctx context.Context, paymentHash string) (LightningSendRecord, error) {
	if s.driver == "postgres" {
		return LightningSendRecord{}, errors.New("lightning_send is not supported on postgres")
	}
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return LightningSendRecord{}, errors.New("invalid payment_hash")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, payment_hash, outbound_invoice, outbound_asset_id, outbound_asset_amount,
		       outbound_amount_msat, payee_pubkey, inbound_invoice, inbound_asset_id,
		       inbound_asset_amount, inbound_amount_msat, converted, status,
		       claim_deadline_height, payment_preimage, last_error, expires_at, created_at, updated_at
		FROM lightning_send_mappings
		WHERE payment_hash = ?
	`, paymentHash)
	return scanLightningSend(row)
}

func scanLightningSend(row rowScanner) (LightningSendRecord, error) {
	var (
		rec                 LightningSendRecord
		outAssetID          sql.NullString
		outAssetAmount      sql.NullInt64
		inAssetID           sql.NullString
		inAssetAmount       sql.NullInt64
		claimDeadlineHeight sql.NullInt64
		preimage            sql.NullString
		lastError           sql.NullString
	)
	err := row.Scan(
		&rec.ID, &rec.PaymentHash, &rec.OutboundInvoice, &outAssetID, &outAssetAmount,
		&rec.OutboundAmountMsat, &rec.PayeePubkey, &rec.InboundInvoice, &inAssetID,
		&inAssetAmount, &rec.InboundAmountMsat, &rec.Converted, &rec.Status,
		&claimDeadlineHeight, &preimage, &lastError, &rec.ExpiresAt, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LightningSendRecord{}, errLightningSendNotFound
		}
		return LightningSendRecord{}, err
	}
	if outAssetID.Valid {
		v := outAssetID.String
		rec.OutboundAssetID = &v
	}
	if outAssetAmount.Valid {
		v := uint64(outAssetAmount.Int64)
		rec.OutboundAssetAmount = &v
	}
	if inAssetID.Valid {
		v := inAssetID.String
		rec.InboundAssetID = &v
	}
	if inAssetAmount.Valid {
		v := uint64(inAssetAmount.Int64)
		rec.InboundAssetAmount = &v
	}
	if claimDeadlineHeight.Valid {
		v := uint32(claimDeadlineHeight.Int64)
		rec.ClaimDeadlineHeight = &v
	}
	if preimage.Valid {
		v := preimage.String
		rec.PaymentPreimage = &v
	}
	if lastError.Valid {
		v := lastError.String
		rec.LastError = &v
	}
	return rec, nil
}

// AdvanceLightningSend moves the record to `to` only if its state is one of
// `from`, and reports whether it did. A false return is not an error — it is the
// normal answer to a replayed webhook or a retried job, and the caller reloads to
// tell "already past this" from "never got there".
//
// The outbox job is written in the same transaction as the state, so a job can
// never exist for a state that rolled back.
func (s *SQLStore) AdvanceLightningSend(ctx context.Context, paymentHash string, from []LightningSendState, to LightningSendState, upd LightningSendUpdate) (bool, error) {
	if s.driver == "postgres" {
		return false, errors.New("lightning_send is not supported on postgres")
	}
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}
	if len(from) == 0 {
		return false, errors.New("no source state given")
	}

	set := []string{"status = ?", "updated_at = CURRENT_TIMESTAMP"}
	args := []any{to}
	if upd.ClaimDeadlineHeight != nil {
		set = append(set, "claim_deadline_height = ?")
		args = append(args, *upd.ClaimDeadlineHeight)
	}
	if upd.PaymentPreimage != nil {
		set = append(set, "payment_preimage = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(*upd.PaymentPreimage)))
	}
	if upd.LastError != nil {
		set = append(set, "last_error = ?")
		args = append(args, *upd.LastError)
	}

	placeholders := make([]string, 0, len(from))
	for _, st := range from {
		placeholders = append(placeholders, "?")
		args = append(args, st)
	}
	args = append(args, paymentHash)

	query := fmt.Sprintf(`
		UPDATE lightning_send_mappings
		SET %s
		WHERE status IN (%s) AND payment_hash = ?
	`, strings.Join(set, ", "), strings.Join(placeholders, ", "))

	changed := false
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return nil
		}
		changed = true
		if upd.Enqueue != "" {
			return s.enqueueAsyncRotatingInvoiceOutboxTx(ctx, tx, paymentHash, upd.Enqueue)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// lightningSendTerminal reports whether nothing further will happen to a record.
func lightningSendTerminal(status LightningSendState) bool {
	return slices.Contains([]LightningSendState{
		lightningSendStateSettled,
		lightningSendStateCancelled,
		lightningSendStateFailed,
	}, status)
}
