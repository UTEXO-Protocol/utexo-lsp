package lspapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var errAsyncOrderNotFound = errors.New("async order not found")
var errAsyncHashPoolEmpty = errors.New("async hash pool is empty")
var errAsyncInvoiceNotFound = errors.New("async rotating invoice not found")
var errAsyncRotatingInvoiceAmountMsatMismatch = errors.New("async rotating invoice amount_msat mismatch")
var errAsyncRotatingInvoiceInvalidAmountMsat = errors.New("async rotating invoice invalid amount_msat")
var errAsyncClaimDeadlineDependency = errors.New("claim deadline validation dependency unavailable")

func (s AsyncInvoiceStatus) Rank() int {
	switch s {
	case asyncInvoiceStatusReserved:
		return 10
	case asyncInvoiceStatusActive:
		return 20
	case asyncInvoiceStatusClaimable:
		return 30
	case asyncInvoiceStatusOutboundRequested:
		return 40
	case asyncInvoiceStatusOutboundPending:
		return 50
	case asyncInvoiceStatusOutboundPaid:
		return 60
	case asyncInvoiceStatusOutboundClaimed:
		return 70
	case asyncInvoiceStatusInboundClaimed:
		return 80
	case asyncInvoiceStatusInboundCancelled, asyncInvoiceStatusOutboundCancelled:
		return 90
	case asyncInvoiceStatusFailed:
		return 100
	default:
		return 0
	}
}

func (s AsyncInvoiceStatus) AtOrBeyond(expected AsyncInvoiceStatus) bool {
	return s.Rank() >= expected.Rank()
}

func (s AsyncInvoiceStatus) IsTerminal() bool {
	switch s {
	case asyncInvoiceStatusInboundClaimed, asyncInvoiceStatusInboundCancelled, asyncInvoiceStatusOutboundCancelled, asyncInvoiceStatusFailed:
		return true
	default:
		return false
	}
}

func asyncRotatingInvoiceStatusAtOrBeyond(actual, expected AsyncInvoiceStatus) bool {
	return actual.AtOrBeyond(expected)
}

type asyncOrderRow struct {
	OrderID              int64
	PeerPubkey           string
	Status               AsyncOrderStatus
	AcceptedThroughIndex sql.NullInt64
	CurrentInvoiceSlot   sql.NullInt64
	CurrentHashIndex     sql.NullInt64
	CurrentPaymentHash   sql.NullString
	CurrentInvoiceID     sql.NullInt64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type asyncHashPoolRow struct {
	ID          int64
	OrderID     int64
	HashIndex   int64
	PaymentHash string
	Status      AsyncPoolStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type asyncRotatingInvoiceRow struct {
	AmountMsat          uint64
	AssetAmount         sql.NullInt64
	AssetID             sql.NullString
	ClaimDeadlineHeight sql.NullInt64
	CreatedAt           time.Time
	ExpiresAt           time.Time
	HashIndex           int64
	InvoiceSlot         int64
	ID                  int64
	InboundInvoice      sql.NullString
	OutboundPendingAt   sql.NullTime
	OutboundPaidAt      sql.NullTime
	OrderID             int64
	PaymentHash         string
	PaymentPreimage     sql.NullString
	RequestInvoiceAt    sql.NullTime
	OutboundInvoice     sql.NullString
	Status              AsyncInvoiceStatus
	UpdatedAt           time.Time
}

type parsedAsyncOrderHash struct {
	HashIndex   int64
	PaymentHash string
}

func (s *SQLStore) asyncRotatingInvoiceTransitionResult(ctx context.Context, paymentHash string, expectedStatus AsyncInvoiceStatus, rowsAffected int64) (bool, error) {
	if rowsAffected > 0 {
		return true, nil
	}

	rec, err := s.LoadAsyncRotatingInvoiceByPaymentHash(ctx, paymentHash)
	if err != nil {
		if errors.Is(err, errAsyncInvoiceNotFound) {
			return false, errAsyncInvoiceNotFound
		}
		return false, err
	}
	if asyncRotatingInvoiceStatusAtOrBeyond(rec.Status, expectedStatus) {
		return false, nil
	}
	return false, errAsyncInvoiceNotFound
}

func (s *SQLStore) enqueueAsyncRotatingInvoiceOutboxTx(ctx context.Context, tx *sql.Tx, paymentHash string, action AsyncOutboxAction) error {
	if s.driver == "postgres" {
		return errors.New("async outbox is not supported on postgres")
	}
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return errors.New("invalid payment_hash")
	}
	action = AsyncOutboxAction(strings.TrimSpace(string(action)))
	if action == "" {
		return errors.New("empty outbox action")
	}

	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO async_rotating_invoice_outbox (payment_hash, action, status, available_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, paymentHash, action, asyncOutboxStatusPending)
	return err
}

func (s *SQLStore) ClaimAsyncRotatingInvoiceOutboxJob(ctx context.Context) (AsyncRotatingInvoiceOutboxJob, bool, error) {
	if s.driver == "postgres" {
		return AsyncRotatingInvoiceOutboxJob{}, false, errors.New("async outbox is not supported on postgres")
	}
	var job AsyncRotatingInvoiceOutboxJob
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 3; i++ {
			row := tx.QueryRowContext(ctx, `
				SELECT id, payment_hash, action, status, attempts, available_at, locked_until, last_error, created_at, updated_at
				FROM async_rotating_invoice_outbox
				WHERE (status = ? AND available_at <= CURRENT_TIMESTAMP)
				   OR (status = ? AND locked_until IS NOT NULL AND locked_until <= CURRENT_TIMESTAMP)
				ORDER BY id ASC
				LIMIT 1
			`, asyncOutboxStatusPending, asyncOutboxStatusProcessing)

			var lockedUntil sql.NullTime
			var lastError sql.NullString
			if err := row.Scan(&job.ID, &job.PaymentHash, &job.Action, &job.Status, &job.Attempts, &job.AvailableAt, &lockedUntil, &lastError, &job.CreatedAt, &job.UpdatedAt); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}

			res, err := tx.ExecContext(ctx, `
				UPDATE async_rotating_invoice_outbox
				SET status = ?,
				    attempts = attempts + 1,
				    locked_until = datetime(CURRENT_TIMESTAMP, '+5 minutes'),
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
				  AND (
				    (status = ? AND available_at <= CURRENT_TIMESTAMP)
				    OR (status = ? AND locked_until IS NOT NULL AND locked_until <= CURRENT_TIMESTAMP)
				  )
			`, asyncOutboxStatusProcessing, job.ID, asyncOutboxStatusPending, asyncOutboxStatusProcessing)
			if err != nil {
				return err
			}
			rows, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if rows == 0 {
				job = AsyncRotatingInvoiceOutboxJob{}
				continue
			}

			job.Status = asyncOutboxStatusProcessing
			job.Attempts++
			if lockedUntil.Valid {
				lockedUntilCopy := lockedUntil.Time.UTC()
				job.LockedUntil = &lockedUntilCopy
			} else {
				now := time.Now().UTC().Add(5 * time.Minute)
				job.LockedUntil = &now
			}
			if lastError.Valid {
				lastErrorCopy := lastError.String
				job.LastError = &lastErrorCopy
			} else {
				job.LastError = nil
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return AsyncRotatingInvoiceOutboxJob{}, false, err
	}
	if job.ID == 0 {
		return AsyncRotatingInvoiceOutboxJob{}, false, nil
	}
	return job, true, nil
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboxDone(ctx context.Context, jobID int64) error {
	if s.driver == "postgres" {
		return errors.New("async outbox is not supported on postgres")
	}
	if jobID <= 0 {
		return errors.New("invalid outbox job id")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoice_outbox
		SET status = ?,
		    locked_until = NULL,
		    last_error = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, asyncOutboxStatusDone, jobID)
	return err
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboxRetry(ctx context.Context, jobID int64, lastErr string) error {
	if s.driver == "postgres" {
		return errors.New("async outbox is not supported on postgres")
	}
	if jobID <= 0 {
		return errors.New("invalid outbox job id")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoice_outbox
		SET status = ?,
		    available_at = datetime(CURRENT_TIMESTAMP, '+15 seconds'),
		    locked_until = NULL,
		    last_error = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, asyncOutboxStatusPending, nullIfEmpty(lastErr), jobID)
	return err
}

func (s *SQLStore) inDBTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return err
	}
	return nil
}

func (s *SQLStore) ReserveLightningAddressInvoiceSlot(ctx context.Context, account LightningAddressAccount, amountMsat uint64, assetID *string, assetAmount *uint64, expiry time.Duration) (AsyncRotatingInvoice, error) {
	account.PeerPubkey = normalizePeerPubkey(account.PeerPubkey)
	if account.PeerPubkey == "" {
		return AsyncRotatingInvoice{}, errors.New("empty peer_pubkey")
	}
	if expiry <= 0 {
		expiry = time.Hour
	}

	var reserved AsyncRotatingInvoice
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		order, err := s.bootstrapAsyncOrderTx(ctx, tx, account.PeerPubkey)
		if err != nil {
			return err
		}
		invoiceSlot := nextInt64FromCurrent(order.CurrentInvoiceSlot)

		available, err := s.countAvailableAsyncHashPoolTx(ctx, tx, order.OrderID)
		if err != nil {
			return err
		}
		if available == 0 {
			return errAsyncHashPoolEmpty
		}

		poolEntry, err := s.reserveAsyncHashPoolEntryTx(ctx, tx, order.OrderID)
		if err != nil {
			return err
		}

		reserved = AsyncRotatingInvoice{
			AmountMsat:  amountMsat,
			AssetAmount: assetAmount,
			AssetID:     assetID,
			ExpiresAt:   time.Now().UTC().Add(expiry),
			HashIndex:   poolEntry.HashIndex,
			InvoiceSlot: invoiceSlot,
			OrderID:     order.OrderID,
			PaymentHash: poolEntry.PaymentHash,
			Status:      asyncInvoiceStatusReserved,
		}

		id, err := s.insertAsyncRotatingInvoiceTx(ctx, tx, reserved)
		if err != nil {
			return err
		}
		reserved.ID = id

		if _, err := s.refreshAsyncOrderStatusTx(ctx, tx, order.OrderID); err != nil {
			return err
		}

		return nil
	})
	return reserved, err
}

func (s *SQLStore) FinalizeLightningAddressInvoiceSlot(ctx context.Context, reservationID int64, invoice string) error {
	invoice = strings.TrimSpace(invoice)
	if invoice == "" {
		return errors.New("empty invoice")
	}

	return s.inDBTx(ctx, func(tx *sql.Tx) error {
		rec, err := s.loadAsyncRotatingInvoiceTx(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if rec.Status == asyncInvoiceStatusFailed {
			return fmt.Errorf("async rotating invoice %d already failed", reservationID)
		}
		if rec.InboundInvoice.Valid && rec.InboundInvoice.String != "" && rec.InboundInvoice.String != invoice && rec.Status == asyncInvoiceStatusActive {
			return fmt.Errorf("async rotating invoice %d already finalized with a different invoice", reservationID)
		}

		if err := s.finalizeAsyncRotatingInvoiceTx(ctx, tx, reservationID, invoice); err != nil {
			return err
		}
		if err := s.consumeAsyncHashPoolEntryTx(ctx, tx, rec.OrderID, rec.HashIndex); err != nil {
			return err
		}
		if err := s.updateAsyncOrderCurrentInvoiceTx(ctx, tx, rec.OrderID, reservationID, rec.InvoiceSlot, rec.HashIndex, rec.PaymentHash); err != nil {
			return err
		}
		_, err = s.refreshAsyncOrderStatusTx(ctx, tx, rec.OrderID)
		return err
	})
}

func (s *SQLStore) ReleaseLightningAddressInvoiceSlot(ctx context.Context, reservationID int64, lastErr string) error {
	_ = lastErr
	return s.inDBTx(ctx, func(tx *sql.Tx) error {
		rec, err := s.loadAsyncRotatingInvoiceTx(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if rec.Status == asyncInvoiceStatusActive {
			return fmt.Errorf("async rotating invoice %d is already active", reservationID)
		}
		if rec.Status == asyncInvoiceStatusFailed {
			return nil
		}

		if err := s.markAsyncRotatingInvoiceFailedTx(ctx, tx, reservationID); err != nil {
			return err
		}
		if err := s.releaseAsyncHashPoolEntryTx(ctx, tx, rec.OrderID, rec.HashIndex); err != nil {
			return err
		}
		_, err = s.refreshAsyncOrderStatusTx(ctx, tx, rec.OrderID)
		return err
	})
}

func (s *SQLStore) ApplyAsyncOrderNew(ctx context.Context, req AsyncOrderNewRequest) (AsyncOrderNewResponse, *AsyncOrderError, error) {
	req.PeerPubkey = normalizePeerPubkey(req.PeerPubkey)
	if req.PeerPubkey == "" {
		return AsyncOrderNewResponse{}, asyncOrderInvalidHashBatch(), nil
	}
	if req.ProtocolVersion != asyncOrderProtocolVersion {
		return AsyncOrderNewResponse{}, &AsyncOrderError{
			Code:    asyncOrderErrorUnsupportedProtocolVersion,
			Message: "unsupported_protocol_version",
		}, nil
	}
	if len(req.Hashes) == 0 || len(req.Hashes) > asyncHashPoolMaxSize {
		return AsyncOrderNewResponse{}, asyncOrderInvalidHashBatch(), nil
	}

	hashes, rpcErr := parseAsyncOrderHashBatch(req.Hashes)
	if rpcErr != nil {
		return AsyncOrderNewResponse{}, rpcErr, nil
	}

	var resp AsyncOrderNewResponse
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		order, err := s.bootstrapAsyncOrderTx(ctx, tx, req.PeerPubkey)
		if err != nil {
			return err
		}

		batchID := ""
		if req.Batch != nil {
			if rpcErr := validateApayBatchCommitment(req.PeerPubkey, req.Batch, hashes); rpcErr != nil {
				return rpcErr
			}
			if err := s.persistApayBatchTx(ctx, tx, order.OrderID, req.PeerPubkey, req.Batch); err != nil {
				return err
			}
			batchID = req.Batch.BatchID
		}

		if rpcErr := s.mergeAsyncHashPoolTx(ctx, tx, order, hashes, batchID); rpcErr != nil {
			return rpcErr
		}

		if err := s.storeApayAddressAttestationTx(ctx, tx, req.PeerPubkey, req.AddressSig); err != nil {
			return err
		}

		snapshot, err := s.asyncOrderSnapshotTx(ctx, tx, order.OrderID)
		if err != nil {
			return err
		}
		resp = snapshot
		return nil
	})
	if err != nil {
		var rpcErr *AsyncOrderError
		if errors.As(err, &rpcErr) {
			return AsyncOrderNewResponse{}, rpcErr, nil
		}
		return AsyncOrderNewResponse{}, nil, err
	}
	return resp, nil, nil
}

func parseAsyncOrderHashBatch(hashes []AsyncOrderNewHashInput) ([]parsedAsyncOrderHash, *AsyncOrderError) {
	parsed := make([]parsedAsyncOrderHash, 0, len(hashes))
	var previous *int64

	for _, entry := range hashes {
		index, err := strconv.ParseInt(strings.TrimSpace(entry.HashIndex), 10, 64)
		if err != nil || index <= 0 {
			return nil, asyncOrderInvalidHashBatch()
		}
		paymentHash := strings.ToLower(strings.TrimSpace(entry.PaymentHash))
		if !isValidPaymentHash(paymentHash) {
			return nil, asyncOrderInvalidHashBatch()
		}
		if previous != nil && index != *previous+1 {
			return nil, asyncOrderInvalidHashBatch()
		}
		previousIndex := index
		previous = &previousIndex
		parsed = append(parsed, parsedAsyncOrderHash{
			HashIndex:   index,
			PaymentHash: paymentHash,
		})
	}

	return parsed, nil
}

func isValidPaymentHash(paymentHash string) bool {
	if len(paymentHash) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(paymentHash)
	return err == nil && len(decoded) == 32
}

func (s *SQLStore) mergeAsyncHashPoolTx(ctx context.Context, tx *sql.Tx, order asyncOrderRow, hashes []parsedAsyncOrderHash, batchID string) *AsyncOrderError {
	existing, err := s.loadAsyncHashPoolTx(ctx, tx, order.OrderID)
	if err != nil {
		return asyncOrderInternalError(err)
	}

	highestIndex := int64(0)
	hashToIndex := make(map[string]int64, len(existing))
	for index, paymentHash := range existing {
		if index > highestIndex {
			highestIndex = index
		}
		hashToIndex[paymentHash] = index
	}

	currentAcceptedThroughIndex := int64(0)
	if order.AcceptedThroughIndex.Valid {
		currentAcceptedThroughIndex = order.AcceptedThroughIndex.Int64
	}
	if currentAcceptedThroughIndex > highestIndex {
		return asyncOrderInternalError(fmt.Errorf("async order %d accepted_through_index %d exceeds hash pool watermark %d", order.OrderID, currentAcceptedThroughIndex, highestIndex))
	}

	expectedStart := highestIndex + 1
	sawExisting := false
	sawMissing := false
	seenBatchHashes := make(map[string]struct{}, len(hashes))

	for _, entry := range hashes {
		if _, ok := seenBatchHashes[entry.PaymentHash]; ok {
			return asyncOrderDuplicateHashConflict()
		}
		seenBatchHashes[entry.PaymentHash] = struct{}{}

		if existingHash, ok := existing[entry.HashIndex]; ok {
			if existingHash != entry.PaymentHash {
				return asyncOrderDuplicateIndexConflict()
			}
			sawExisting = true
		} else {
			sawMissing = true
		}

		if existingIndex, ok := hashToIndex[entry.PaymentHash]; ok && existingIndex != entry.HashIndex {
			return asyncOrderDuplicateHashConflict()
		}
	}

	missingCount := 0
	for _, entry := range hashes {
		if _, ok := existing[entry.HashIndex]; !ok {
			missingCount++
		}
	}
	if len(existing)+missingCount > asyncHashPoolMaxSize {
		return asyncOrderInvalidHashBatch()
	}

	if sawExisting && sawMissing {
		return asyncOrderInvalidHashBatch()
	}
	if sawExisting {
		if err := s.updateAsyncOrderAcceptedThroughIndexTx(ctx, tx, order.OrderID, highestIndex); err != nil {
			return asyncOrderInternalError(err)
		}
		return nil
	}
	if hashes[0].HashIndex != expectedStart {
		return asyncOrderInvalidHashBatch()
	}

	stampBatchID := nullIfEmpty(strings.ToLower(strings.TrimSpace(batchID)))
	for _, entry := range hashes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO async_hash_pool (order_id, hash_index, payment_hash, status, batch_id)
			VALUES (?, ?, ?, ?, ?)
		`, order.OrderID, entry.HashIndex, entry.PaymentHash, asyncPoolStatusAvailable, stampBatchID); err != nil {
			return asyncOrderInternalError(err)
		}
	}

	acceptedThroughIndex := hashes[len(hashes)-1].HashIndex
	if currentAcceptedThroughIndex > acceptedThroughIndex {
		acceptedThroughIndex = currentAcceptedThroughIndex
	}
	if err := s.updateAsyncOrderAcceptedThroughIndexTx(ctx, tx, order.OrderID, acceptedThroughIndex); err != nil {
		return asyncOrderInternalError(err)
	}
	return nil
}

func (s *SQLStore) loadAsyncHashPoolTx(ctx context.Context, tx *sql.Tx, orderID int64) (map[int64]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT hash_index, payment_hash FROM async_hash_pool WHERE order_id = ?`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make(map[int64]string)
	for rows.Next() {
		var hashIndex int64
		var paymentHash string
		if err := rows.Scan(&hashIndex, &paymentHash); err != nil {
			return nil, err
		}
		entries[hashIndex] = strings.ToLower(paymentHash)
	}
	return entries, rows.Err()
}

func (s *SQLStore) asyncOrderSnapshotTx(ctx context.Context, tx *sql.Tx, orderID int64) (AsyncOrderNewResponse, error) {
	acceptedThroughIndex, err := s.loadAsyncOrderAcceptedThroughIndexTx(ctx, tx, orderID)
	if err != nil {
		return AsyncOrderNewResponse{}, err
	}

	var unusedHashes int64
	if !acceptedThroughIndex.Valid {
		query := `
			SELECT
				COALESCE(MAX(hash_index), 0),
				COUNT(CASE WHEN status = ? THEN 1 END)
			FROM async_hash_pool
			WHERE order_id = ?
		`
		if err := tx.QueryRowContext(ctx, query, asyncPoolStatusAvailable, orderID).Scan(&acceptedThroughIndex.Int64, &unusedHashes); err != nil {
			return AsyncOrderNewResponse{}, err
		}
		acceptedThroughIndex.Valid = true
		if err := s.updateAsyncOrderAcceptedThroughIndexTx(ctx, tx, orderID, acceptedThroughIndex.Int64); err != nil {
			return AsyncOrderNewResponse{}, err
		}
	} else {
		query := `SELECT COUNT(CASE WHEN status = ? THEN 1 END) FROM async_hash_pool WHERE order_id = ?`
		if err := tx.QueryRowContext(ctx, query, asyncPoolStatusAvailable, orderID).Scan(&unusedHashes); err != nil {
			return AsyncOrderNewResponse{}, err
		}
	}

	status, err := s.refreshAsyncOrderStatusTx(ctx, tx, orderID)
	if err != nil {
		return AsyncOrderNewResponse{}, err
	}

	return AsyncOrderNewResponse{
		ProtocolVersion:      asyncOrderProtocolVersion,
		OrderID:              strconv.FormatInt(orderID, 10),
		Status:               status,
		AcceptedThroughIndex: uint64(acceptedThroughIndex.Int64),
		NextIndexExpected:    uint64(acceptedThroughIndex.Int64 + 1),
		UnusedHashes:         uint64(unusedHashes),
		RefillBatchSize:      uint64(asyncHashPoolMaxSize),
	}, nil
}

func asyncOrderInvalidHashBatch() *AsyncOrderError {
	return &AsyncOrderError{Code: asyncOrderErrorInvalidHashBatch, Message: "invalid_hash_batch"}
}

func asyncOrderDuplicateIndexConflict() *AsyncOrderError {
	return &AsyncOrderError{Code: asyncOrderErrorDuplicateIndexConflict, Message: "duplicate_index_conflict"}
}

func asyncOrderDuplicateHashConflict() *AsyncOrderError {
	return &AsyncOrderError{Code: asyncOrderErrorDuplicateHashConflict, Message: "duplicate_hash_conflict"}
}

func asyncOrderInternalError(err error) *AsyncOrderError {
	return &AsyncOrderError{Code: asyncOrderJSONRPCInternalError, Message: err.Error()}
}

func (s *SQLStore) bootstrapAsyncOrderTx(ctx context.Context, tx *sql.Tx, peerPubkey string) (asyncOrderRow, error) {
	peerPubkey = normalizePeerPubkey(peerPubkey)
	if peerPubkey == "" {
		return asyncOrderRow{}, errors.New("empty peer_pubkey")
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO async_orders (peer_pubkey, status)
		VALUES (?, ?)
	`, peerPubkey, asyncOrderStatusActive); err != nil {
		return asyncOrderRow{}, err
	}

	query := `SELECT order_id, peer_pubkey, status, accepted_through_index, current_invoice_slot, current_hash_index, current_payment_hash, current_invoice_id, created_at, updated_at FROM async_orders WHERE peer_pubkey = ? LIMIT 1`
	row := tx.QueryRowContext(ctx, query, peerPubkey)
	return scanAsyncOrderRow(row)
}

func scanAsyncOrderRow(row rowScanner) (asyncOrderRow, error) {
	var order asyncOrderRow
	if err := row.Scan(&order.OrderID, &order.PeerPubkey, &order.Status, &order.AcceptedThroughIndex, &order.CurrentInvoiceSlot, &order.CurrentHashIndex, &order.CurrentPaymentHash, &order.CurrentInvoiceID, &order.CreatedAt, &order.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return asyncOrderRow{}, errAsyncOrderNotFound
		}
		return asyncOrderRow{}, err
	}
	return order, nil
}

func (s *SQLStore) countAvailableAsyncHashPoolTx(ctx context.Context, tx *sql.Tx, orderID int64) (int64, error) {
	query := `SELECT COUNT(*) FROM async_hash_pool WHERE order_id = ? AND status = ?`
	var count int64
	if err := tx.QueryRowContext(ctx, query, orderID, asyncPoolStatusAvailable).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *SQLStore) reserveAsyncHashPoolEntryTx(ctx context.Context, tx *sql.Tx, orderID int64) (asyncHashPoolRow, error) {
	query := `SELECT id, order_id, hash_index, payment_hash, status, created_at, updated_at FROM async_hash_pool WHERE order_id = ? AND status = ? ORDER BY hash_index ASC LIMIT 1`
	row := tx.QueryRowContext(ctx, query, orderID, asyncPoolStatusAvailable)
	var entry asyncHashPoolRow
	if err := row.Scan(&entry.ID, &entry.OrderID, &entry.HashIndex, &entry.PaymentHash, &entry.Status, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return asyncHashPoolRow{}, errAsyncHashPoolEmpty
		}
		return asyncHashPoolRow{}, err
	}

	update := `UPDATE async_hash_pool SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = ?`
	res, err := tx.ExecContext(ctx, update, asyncPoolStatusReserved, entry.ID, asyncPoolStatusAvailable)
	if err != nil {
		return asyncHashPoolRow{}, err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return asyncHashPoolRow{}, errAsyncHashPoolEmpty
	}
	entry.Status = asyncPoolStatusReserved
	return entry, nil
}

func (s *SQLStore) insertAsyncRotatingInvoiceTx(ctx context.Context, tx *sql.Tx, invoice AsyncRotatingInvoice) (int64, error) {
	var assetIDValue any
	if invoice.AssetID != nil {
		assetIDValue = strings.TrimSpace(*invoice.AssetID)
	}
	var assetAmountValue any
	if invoice.AssetAmount != nil {
		assetAmountValue = *invoice.AssetAmount
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO async_rotating_invoices (order_id, invoice_slot, hash_index, payment_hash, asset_amount, asset_id, amount_msat, expires_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, invoice.OrderID, invoice.InvoiceSlot, invoice.HashIndex, invoice.PaymentHash, assetAmountValue, assetIDValue, invoice.AmountMsat, invoice.ExpiresAt, invoice.Status)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

func asyncRotatingInvoiceFromRow(rec asyncRotatingInvoiceRow) AsyncRotatingInvoice {
	return AsyncRotatingInvoice{
		AmountMsat:          rec.AmountMsat,
		AssetAmount:         nullInt64ToUint64(rec.AssetAmount),
		AssetID:             nullStringToPtr(rec.AssetID),
		ClaimDeadlineHeight: nullInt64ToUint32(rec.ClaimDeadlineHeight),
		CreatedAt:           rec.CreatedAt,
		ExpiresAt:           rec.ExpiresAt,
		HashIndex:           rec.HashIndex,
		InvoiceSlot:         rec.InvoiceSlot,
		ID:                  rec.ID,
		InboundInvoice:      nullStringToPtr(rec.InboundInvoice),
		OutboundPendingAt:   nullTimeToPtr(rec.OutboundPendingAt),
		OutboundPaidAt:      nullTimeToPtr(rec.OutboundPaidAt),
		OrderID:             rec.OrderID,
		PaymentHash:         rec.PaymentHash,
		PaymentPreimage:     nullStringToPtr(rec.PaymentPreimage),
		RequestInvoiceAt:    nullTimeToPtr(rec.RequestInvoiceAt),
		OutboundInvoice:     nullStringToPtr(rec.OutboundInvoice),
		Status:              rec.Status,
		UpdatedAt:           rec.UpdatedAt,
	}
}

func (s *SQLStore) loadAsyncRotatingInvoiceTx(ctx context.Context, tx *sql.Tx, reservationID int64) (asyncRotatingInvoiceRow, error) {
	query := `SELECT id, order_id, invoice_slot, hash_index, payment_hash, asset_amount, asset_id, invoice_string, amount_msat, claim_deadline_height, payment_preimage, request_invoice_at, request_invoice_bolt11, outbound_pending_at, outbound_paid_at, expires_at, status, created_at, updated_at FROM async_rotating_invoices WHERE id = ? LIMIT 1`
	row := tx.QueryRowContext(ctx, query, reservationID)
	var rec asyncRotatingInvoiceRow
	if err := row.Scan(&rec.ID, &rec.OrderID, &rec.InvoiceSlot, &rec.HashIndex, &rec.PaymentHash, &rec.AssetAmount, &rec.AssetID, &rec.InboundInvoice, &rec.AmountMsat, &rec.ClaimDeadlineHeight, &rec.PaymentPreimage, &rec.RequestInvoiceAt, &rec.OutboundInvoice, &rec.OutboundPendingAt, &rec.OutboundPaidAt, &rec.ExpiresAt, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return asyncRotatingInvoiceRow{}, errAsyncInvoiceNotFound
		}
		return asyncRotatingInvoiceRow{}, err
	}
	return rec, nil
}

func (s *SQLStore) loadAsyncRotatingInvoiceByPaymentHashTx(ctx context.Context, tx *sql.Tx, paymentHash string) (asyncRotatingInvoiceRow, error) {
	query := `SELECT id, order_id, invoice_slot, hash_index, payment_hash, asset_amount, asset_id, invoice_string, amount_msat, claim_deadline_height, payment_preimage, request_invoice_at, request_invoice_bolt11, outbound_pending_at, outbound_paid_at, expires_at, status, created_at, updated_at FROM async_rotating_invoices WHERE payment_hash = ? LIMIT 1`
	row := tx.QueryRowContext(ctx, query, paymentHash)
	var rec asyncRotatingInvoiceRow
	if err := row.Scan(&rec.ID, &rec.OrderID, &rec.InvoiceSlot, &rec.HashIndex, &rec.PaymentHash, &rec.AssetAmount, &rec.AssetID, &rec.InboundInvoice, &rec.AmountMsat, &rec.ClaimDeadlineHeight, &rec.PaymentPreimage, &rec.RequestInvoiceAt, &rec.OutboundInvoice, &rec.OutboundPendingAt, &rec.OutboundPaidAt, &rec.ExpiresAt, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return asyncRotatingInvoiceRow{}, errAsyncInvoiceNotFound
		}
		return asyncRotatingInvoiceRow{}, err
	}
	return rec, nil
}

func (s *SQLStore) LoadAsyncRotatingInvoiceByPaymentHash(ctx context.Context, paymentHash string) (AsyncRotatingInvoice, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return AsyncRotatingInvoice{}, errors.New("invalid payment_hash")
	}

	query := `SELECT id, order_id, invoice_slot, hash_index, payment_hash, asset_amount, asset_id, invoice_string, amount_msat, claim_deadline_height, payment_preimage, request_invoice_at, request_invoice_bolt11, outbound_pending_at, outbound_paid_at, expires_at, status, created_at, updated_at FROM async_rotating_invoices WHERE payment_hash = ? LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, paymentHash)
	var rec asyncRotatingInvoiceRow
	if err := row.Scan(&rec.ID, &rec.OrderID, &rec.InvoiceSlot, &rec.HashIndex, &rec.PaymentHash, &rec.AssetAmount, &rec.AssetID, &rec.InboundInvoice, &rec.AmountMsat, &rec.ClaimDeadlineHeight, &rec.PaymentPreimage, &rec.RequestInvoiceAt, &rec.OutboundInvoice, &rec.OutboundPendingAt, &rec.OutboundPaidAt, &rec.ExpiresAt, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AsyncRotatingInvoice{}, errAsyncInvoiceNotFound
		}
		return AsyncRotatingInvoice{}, err
	}
	return asyncRotatingInvoiceFromRow(rec), nil
}

func (s *SQLStore) GetAsyncOrderPeerPubkeyByOrderID(ctx context.Context, orderID int64) (string, error) {
	query := `SELECT peer_pubkey FROM async_orders WHERE order_id = ? LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, orderID)
	var peerPubkey string
	if err := row.Scan(&peerPubkey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errAsyncOrderNotFound
		}
		return "", err
	}
	return peerPubkey, nil
}

func (s *SQLStore) finalizeAsyncRotatingInvoiceTx(ctx context.Context, tx *sql.Tx, reservationID int64, invoice string) error {
	query := `UPDATE async_rotating_invoices SET invoice_string = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := tx.ExecContext(ctx, query, invoice, asyncInvoiceStatusActive, reservationID)
	return err
}

func (s *SQLStore) MarkAsyncRotatingInvoiceClaimable(ctx context.Context, paymentHash string, amountMsat uint64, claimDeadlineHeight *uint32) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}
	if amountMsat == 0 {
		return false, errAsyncRotatingInvoiceInvalidAmountMsat
	}
	if claimDeadlineHeight != nil && *claimDeadlineHeight == 0 {
		return false, errors.New("invalid claim_deadline_height")
	}

	var transitioned bool
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		rec, err := s.loadAsyncRotatingInvoiceByPaymentHashTx(ctx, tx, paymentHash)
		if err != nil {
			return err
		}

		if rec.AmountMsat != amountMsat {
			return errAsyncRotatingInvoiceAmountMsatMismatch
		}

		var claimDeadlineHeightValue any
		if claimDeadlineHeight != nil {
			claimDeadlineHeightValue = *claimDeadlineHeight
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE async_rotating_invoices
			SET status = ?,
			    claimable_at = COALESCE(claimable_at, CURRENT_TIMESTAMP),
			    claim_deadline_height = COALESCE(claim_deadline_height, ?),
			    updated_at = CURRENT_TIMESTAMP
			WHERE payment_hash = ?
			  AND status = ?
		`, asyncInvoiceStatusClaimable, claimDeadlineHeightValue, paymentHash, asyncInvoiceStatusActive)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		transitioned, err = s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusActive, rows)
		if err != nil {
			return err
		}
		if transitioned {
			if err := s.enqueueAsyncRotatingInvoiceOutboxTx(ctx, tx, paymentHash, asyncOutboxActionRequestOutboundInvoice); err != nil {
				return err
			}
		}
		return err
	})
	return transitioned, err
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboundRequested(ctx context.Context, paymentHash string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoices
		SET status = ?,
		    request_invoice_at = COALESCE(request_invoice_at, CURRENT_TIMESTAMP),
		    updated_at = CURRENT_TIMESTAMP
		WHERE payment_hash = ?
		  AND status = ?
	`, asyncInvoiceStatusOutboundRequested, paymentHash, asyncInvoiceStatusClaimable)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusClaimable, rows)
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboundPending(ctx context.Context, paymentHash, invoice string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}
	invoice = strings.TrimSpace(invoice)
	if invoice == "" {
		return false, errors.New("empty request invoice")
	}

	var transitioned bool
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE async_rotating_invoices
			SET status = ?,
			    request_invoice_at = COALESCE(request_invoice_at, CURRENT_TIMESTAMP),
			    request_invoice_bolt11 = COALESCE(request_invoice_bolt11, ?),
			    outbound_pending_at = COALESCE(outbound_pending_at, CURRENT_TIMESTAMP),
			    updated_at = CURRENT_TIMESTAMP
			WHERE payment_hash = ?
			  AND status = ?
		`, asyncInvoiceStatusOutboundPending, invoice, paymentHash, asyncInvoiceStatusOutboundRequested)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		transitioned, err = s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusOutboundRequested, rows)
		if err != nil {
			return err
		}
		if transitioned {
			if err := s.enqueueAsyncRotatingInvoiceOutboxTx(ctx, tx, paymentHash, asyncOutboxActionSendOutboundPayment); err != nil {
				return err
			}
		}
		return nil
	})
	return transitioned, err
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboundPaid(ctx context.Context, paymentHash string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoices
		SET status = ?,
		    outbound_paid_at = COALESCE(outbound_paid_at, CURRENT_TIMESTAMP),
		    updated_at = CURRENT_TIMESTAMP
		WHERE payment_hash = ?
		  AND status = ?
	`, asyncInvoiceStatusOutboundPaid, paymentHash, asyncInvoiceStatusOutboundPending)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusOutboundPending, rows)
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboundClaimed(ctx context.Context, paymentHash, paymentPreimage string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}
	paymentPreimage = strings.ToLower(strings.TrimSpace(paymentPreimage))
	if !isValidPaymentHash(paymentPreimage) {
		return false, errors.New("invalid payment_preimage")
	}
	preimageBytes, err := hex.DecodeString(paymentPreimage)
	if err != nil || len(preimageBytes) != sha256.Size {
		return false, errors.New("invalid payment_preimage")
	}
	sum := sha256.Sum256(preimageBytes)
	if hex.EncodeToString(sum[:]) != paymentHash {
		return false, errors.New("payment_preimage does not match payment_hash")
	}

	var transitioned bool
	err = s.inDBTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE async_rotating_invoices
			SET status = ?,
			    payment_preimage = COALESCE(payment_preimage, ?),
			    updated_at = CURRENT_TIMESTAMP
			WHERE payment_hash = ?
			  AND status = ?
		`, asyncInvoiceStatusOutboundClaimed, paymentPreimage, paymentHash, asyncInvoiceStatusOutboundPaid)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		transitioned, err = s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusOutboundPaid, rows)
		if err != nil {
			return err
		}
		if transitioned {
			if err := s.enqueueAsyncRotatingInvoiceOutboxTx(ctx, tx, paymentHash, asyncOutboxActionClaimInboundInvoice); err != nil {
				return err
			}
		}
		return nil
	})
	return transitioned, err
}

func (s *SQLStore) MarkAsyncRotatingInvoiceInboundClaimed(ctx context.Context, paymentHash string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoices
		SET status = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE payment_hash = ?
		  AND status = ?
	`, asyncInvoiceStatusInboundClaimed, paymentHash, asyncInvoiceStatusOutboundClaimed)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusOutboundClaimed, rows)
}

func (s *SQLStore) MarkAsyncRotatingInvoiceInboundCancelled(ctx context.Context, paymentHash string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoices
		SET status = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE payment_hash = ?
		  AND status IN (?, ?, ?)
	`, asyncInvoiceStatusInboundCancelled, paymentHash, asyncInvoiceStatusReserved, asyncInvoiceStatusActive, asyncInvoiceStatusClaimable)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusClaimable, rows)
}

func (s *SQLStore) MarkAsyncRotatingInvoiceOutboundCancelled(ctx context.Context, paymentHash string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoices
		SET status = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE payment_hash = ?
		  AND status IN (?, ?, ?, ?)
	`, asyncInvoiceStatusOutboundCancelled, paymentHash, asyncInvoiceStatusOutboundRequested, asyncInvoiceStatusOutboundPending, asyncInvoiceStatusOutboundPaid, asyncInvoiceStatusOutboundClaimed)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusOutboundRequested, rows)
}

func (s *SQLStore) MarkAsyncRotatingInvoiceFailed(ctx context.Context, paymentHash string) (bool, error) {
	paymentHash = strings.ToLower(strings.TrimSpace(paymentHash))
	if !isValidPaymentHash(paymentHash) {
		return false, errors.New("invalid payment_hash")
	}

	var transitioned bool
	err := s.inDBTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE async_rotating_invoices
			SET status = ?,
			    updated_at = CURRENT_TIMESTAMP
			WHERE payment_hash = ?
			  AND status IN (?, ?)
		`, asyncInvoiceStatusFailed, paymentHash, asyncInvoiceStatusClaimable, asyncInvoiceStatusOutboundRequested)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		transitioned, err = s.asyncRotatingInvoiceTransitionResult(ctx, paymentHash, asyncInvoiceStatusClaimable, rows)
		if err != nil {
			return err
		}
		return nil
	})
	return transitioned, err
}

func (s *SQLStore) markAsyncRotatingInvoiceFailedTx(ctx context.Context, tx *sql.Tx, reservationID int64) error {
	query := `UPDATE async_rotating_invoices SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := tx.ExecContext(ctx, query, asyncInvoiceStatusFailed, reservationID)
	return err
}

func (s *SQLStore) consumeAsyncHashPoolEntryTx(ctx context.Context, tx *sql.Tx, orderID, hashIndex int64) error {
	query := `UPDATE async_hash_pool SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE order_id = ? AND hash_index = ?`
	_, err := tx.ExecContext(ctx, query, asyncPoolStatusConsumed, orderID, hashIndex)
	return err
}

func (s *SQLStore) releaseAsyncHashPoolEntryTx(ctx context.Context, tx *sql.Tx, orderID, hashIndex int64) error {
	query := `UPDATE async_hash_pool SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE order_id = ? AND hash_index = ?`
	_, err := tx.ExecContext(ctx, query, asyncPoolStatusAvailable, orderID, hashIndex)
	return err
}

func (s *SQLStore) updateAsyncOrderCurrentInvoiceTx(ctx context.Context, tx *sql.Tx, orderID, invoiceID, invoiceSlot, hashIndex int64, paymentHash string) error {
	query := `UPDATE async_orders SET current_invoice_id = ?, current_invoice_slot = ?, current_hash_index = ?, current_payment_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE order_id = ?`
	_, err := tx.ExecContext(ctx, query, invoiceID, invoiceSlot, hashIndex, paymentHash, orderID)
	return err
}

func (s *SQLStore) updateAsyncOrderAcceptedThroughIndexTx(ctx context.Context, tx *sql.Tx, orderID, acceptedThroughIndex int64) error {
	query := `UPDATE async_orders SET accepted_through_index = ?, updated_at = CURRENT_TIMESTAMP WHERE order_id = ?`
	_, err := tx.ExecContext(ctx, query, acceptedThroughIndex, orderID)
	return err
}

func (s *SQLStore) updateAsyncOrderStatusTx(ctx context.Context, tx *sql.Tx, orderID int64, status AsyncOrderStatus) error {
	if status == "" {
		return errors.New("empty status")
	}
	query := `UPDATE async_orders SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE order_id = ?`
	_, err := tx.ExecContext(ctx, query, status, orderID)
	return err
}

func (s *SQLStore) refreshAsyncOrderStatusTx(ctx context.Context, tx *sql.Tx, orderID int64) (AsyncOrderStatus, error) {
	available, err := s.countAvailableAsyncHashPoolTx(ctx, tx, orderID)
	if err != nil {
		return "", err
	}

	status := asyncOrderStatusActive
	if available == 0 {
		status = asyncOrderStatusExhausted
	}

	if err := s.updateAsyncOrderStatusTx(ctx, tx, orderID, status); err != nil {
		return "", err
	}
	return status, nil
}

func (s *SQLStore) loadAsyncOrderAcceptedThroughIndexTx(ctx context.Context, tx *sql.Tx, orderID int64) (sql.NullInt64, error) {
	query := `SELECT accepted_through_index FROM async_orders WHERE order_id = ? LIMIT 1`
	row := tx.QueryRowContext(ctx, query, orderID)
	var acceptedThroughIndex sql.NullInt64
	if err := row.Scan(&acceptedThroughIndex); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt64{}, errAsyncOrderNotFound
		}
		return sql.NullInt64{}, err
	}
	return acceptedThroughIndex, nil
}

func nextInt64FromCurrent(current sql.NullInt64) int64 {
	if current.Valid && current.Int64 > 0 {
		return current.Int64 + 1
	}
	return 1
}

func nullInt64ToUint32(v sql.NullInt64) *uint32 {
	if !v.Valid || v.Int64 < 0 || v.Int64 > int64(^uint32(0)) {
		return nil
	}
	out := uint32(v.Int64)
	return &out
}

func nullInt64ToUint64(v sql.NullInt64) *uint64 {
	if !v.Valid || v.Int64 < 0 {
		return nil
	}
	out := uint64(v.Int64)
	return &out
}

func nullStringToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

func nullStringToString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func nullTimeToPtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	out := v.Time.UTC()
	return &out
}
