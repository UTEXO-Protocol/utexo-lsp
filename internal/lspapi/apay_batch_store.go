package lspapi

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

func (s *SQLStore) persistApayBatchTx(ctx context.Context, tx *sql.Tx, orderID int64, recipientPubkey string, batch *ApayBatchCommitment) error {
	var expiresAt any
	if batch.ExpiresAt != 0 {
		expiresAt = int64(batch.ExpiresAt)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO apay_hash_batch
			(batch_id, order_id, recipient_pubkey, host_pubkey, batch_root, batch_size, batch_sig, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		strings.ToLower(strings.TrimSpace(batch.BatchID)), orderID,
		strings.ToLower(strings.TrimSpace(recipientPubkey)),
		strings.ToLower(strings.TrimSpace(batch.HostPubkey)),
		strings.ToLower(strings.TrimSpace(batch.BatchRoot)),
		int64(batch.BatchSize),
		strings.ToLower(strings.TrimSpace(batch.BatchSig)),
		int64(batch.CreatedAt), expiresAt,
	)
	return err
}

func (s *SQLStore) storeApayAddressAttestationTx(ctx context.Context, tx *sql.Tx, peerPubkey string, sig *string) error {
	if sig == nil || strings.TrimSpace(*sig) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE lnaddr_accounts SET address_sig = ? WHERE peer_pubkey = ?
	`, strings.ToLower(strings.TrimSpace(*sig)), normalizePeerPubkey(peerPubkey))
	return err
}

func validateApayBatchCommitment(recipientPubkey string, batch *ApayBatchCommitment, hashes []parsedAsyncOrderHash) *AsyncOrderError {
	rpk, err := decodePubkey33(recipientPubkey)
	if err != nil {
		return asyncOrderInvalidHashBatch()
	}
	if _, err := decodePubkey33(batch.HostPubkey); err != nil {
		return asyncOrderInvalidHashBatch()
	}
	bid, err := decodeBatchId16(batch.BatchID)
	if err != nil {
		return asyncOrderInvalidHashBatch()
	}
	if uint64(len(hashes)) != batch.BatchSize {
		return asyncOrderInvalidHashBatch()
	}
	entries := make([]apayLeafInput, 0, len(hashes))
	for _, h := range hashes {
		ph, err := decodeHash32(h.PaymentHash)
		if err != nil {
			return asyncOrderInvalidHashBatch()
		}
		entries = append(entries, apayLeafInput{hashIndex: uint64(h.HashIndex), paymentHash: ph})
	}
	root := merkleRoot(apayLeaves(rpk, bid, entries))
	if hex.EncodeToString(root[:]) != strings.ToLower(strings.TrimSpace(batch.BatchRoot)) {
		return asyncOrderInvalidHashBatch()
	}
	return nil
}

func (s *SQLStore) BuildApayInvoiceProof(ctx context.Context, orderID int64, hashIndex int64) (*ApayInvoiceProof, error) {
	var batchID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT batch_id FROM async_hash_pool WHERE order_id = ? AND hash_index = ? LIMIT 1`,
		orderID, hashIndex).Scan(&batchID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !batchID.Valid || strings.TrimSpace(batchID.String) == "" {
		return nil, nil
	}

	var (
		recipientPubkey, hostPubkey, batchRoot, batchSig string
		batchSize, createdAt                             int64
		expiresAt                                        sql.NullInt64
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT recipient_pubkey, host_pubkey, batch_root, batch_size, batch_sig, created_at, expires_at
		FROM apay_hash_batch WHERE batch_id = ? LIMIT 1
	`, batchID.String).Scan(&recipientPubkey, &hostPubkey, &batchRoot, &batchSize, &batchSig, &createdAt, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	rp, err := decodePubkey33(recipientPubkey)
	if err != nil {
		return nil, err
	}
	bid, err := decodeBatchId16(batchID.String)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT hash_index, payment_hash FROM async_hash_pool WHERE batch_id = ? ORDER BY hash_index ASC`,
		batchID.String)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []apayLeafInput
	targetIdx := -1
	targetPaymentHash := ""
	for rows.Next() {
		var hi int64
		var ph string
		if err := rows.Scan(&hi, &ph); err != nil {
			return nil, err
		}
		phb, err := decodeHash32(ph)
		if err != nil {
			return nil, err
		}
		if hi == hashIndex {
			targetIdx = len(entries)
			targetPaymentHash = ph
		}
		entries = append(entries, apayLeafInput{hashIndex: uint64(hi), paymentHash: phb})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if targetIdx < 0 {
		return nil, fmt.Errorf("hash_index %d not found in batch %s", hashIndex, batchID.String)
	}

	leaves := apayLeaves(rp, bid, entries)
	var exp uint64
	if expiresAt.Valid {
		exp = uint64(expiresAt.Int64)
	}
	return &ApayInvoiceProof{
		Version:         1,
		RecipientPubkey: recipientPubkey,
		HostPubkey:      hostPubkey,
		BatchID:         batchID.String,
		HashIndex:       uint64(hashIndex),
		PaymentHash:     targetPaymentHash,
		BatchRoot:       batchRoot,
		BatchSize:       uint64(batchSize),
		MerkleProof:     merkleProof(leaves, targetIdx),
		BatchSig:        batchSig,
		CreatedAt:       uint64(createdAt),
		ExpiresAt:       exp,
	}, nil
}

func (s *SQLStore) GetApayAddressAttestation(ctx context.Context, peerPubkey string) (sig *string, err error) {
	var sigNS sql.NullString
	e := s.db.QueryRowContext(ctx,
		`SELECT address_sig FROM lnaddr_accounts WHERE peer_pubkey = ? LIMIT 1`,
		normalizePeerPubkey(peerPubkey)).Scan(&sigNS)
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, e
	}
	if sigNS.Valid && sigNS.String != "" {
		v := sigNS.String
		sig = &v
	}
	return sig, nil
}

func decodePubkey33(s string) ([33]byte, error) {
	var out [33]byte
	b, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(s)))
	if err != nil {
		return out, err
	}
	if len(b) != 33 {
		return out, fmt.Errorf("expected 33-byte pubkey, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

func decodeBatchId16(s string) ([16]byte, error) {
	var out [16]byte
	b, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(s)))
	if err != nil {
		return out, err
	}
	if len(b) != 16 {
		return out, fmt.Errorf("expected 16-byte batch_id, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}
