package lspapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

const (
	statusPendingLN  = "pending_ln"
	statusPendingRGB = "pending_rgb"
	statusCompleted  = "completed"
	statusExpired    = "expired"
	statusCanceled   = "canceled"
	statusFailed     = "failed"
)

var errLightningAddressAccountNotFound = errors.New("lightning address account not found")

type Store interface {
	Close() error
	InsertOnchainSend(ctx context.Context, userRGBInvoice, lspLNInvoice string, lnExpiresAt *time.Time) (int64, error)
	InsertLightningReceive(ctx context.Context, userLNInvoice, lspRGBInvoice, rgbAssetID string, batchTransferIdx int64, rgbExpiresAt *time.Time) (int64, error)
	GetLightningAddressAccountByUsername(ctx context.Context, username string) (LightningAddressAccount, error)
	GetLightningAddressAccountByPeerPubkey(ctx context.Context, peerPubkey string) (LightningAddressAccount, error)
	InsertLightningAddressAccount(ctx context.Context, account LightningAddressAccount) (bool, error)
	ReserveLightningAddressInvoiceSlot(ctx context.Context, account LightningAddressAccount, amountMsat uint64, assetID *string, assetAmount *uint64, expiry time.Duration) (AsyncRotatingInvoice, error)
	FinalizeLightningAddressInvoiceSlot(ctx context.Context, reservationID int64, invoice string) error
	GetAsyncOrderPeerPubkeyByOrderID(ctx context.Context, orderID int64) (string, error)
	LoadAsyncRotatingInvoiceByPaymentHash(ctx context.Context, paymentHash string) (AsyncRotatingInvoice, error)
	MarkAsyncRotatingInvoiceClaimable(ctx context.Context, paymentHash string, amountMsat uint64, claimDeadlineHeight *uint32) (bool, error)
	MarkAsyncRotatingInvoiceOutboundRequested(ctx context.Context, paymentHash string) (bool, error)
	MarkAsyncRotatingInvoiceOutboundPending(ctx context.Context, paymentHash, invoice string) (bool, error)
	MarkAsyncRotatingInvoiceOutboundPaid(ctx context.Context, paymentHash string) (bool, error)
	MarkAsyncRotatingInvoiceOutboundClaimed(ctx context.Context, paymentHash, paymentPreimage string) (bool, error)
	MarkAsyncRotatingInvoiceInboundClaimed(ctx context.Context, paymentHash string) (bool, error)
	MarkAsyncRotatingInvoiceInboundCancelled(ctx context.Context, paymentHash string) (bool, error)
	MarkAsyncRotatingInvoiceOutboundCancelled(ctx context.Context, paymentHash string) (bool, error)
	MarkAsyncRotatingInvoiceFailed(ctx context.Context, paymentHash string) (bool, error)
	ClaimAsyncRotatingInvoiceOutboxJob(ctx context.Context) (AsyncRotatingInvoiceOutboxJob, bool, error)
	MarkAsyncRotatingInvoiceOutboxDone(ctx context.Context, jobID int64) error
	MarkAsyncRotatingInvoiceOutboxRetry(ctx context.Context, jobID int64, lastErr string) error
	ReleaseLightningAddressInvoiceSlot(ctx context.Context, reservationID int64, lastErr string) error
	ApplyAsyncOrderNew(ctx context.Context, req AsyncOrderNewRequest) (AsyncOrderNewResponse, *AsyncOrderError, error)
	ListOnchainPending(ctx context.Context, limit int) ([]OnchainSendRecord, error)
	ListLightningPending(ctx context.Context, limit int) ([]LightningReceiveRecord, error)
	UpdateOnchainStatus(ctx context.Context, id int64, status, lastErr string) error
	UpdateLightningStatus(ctx context.Context, id int64, status, lastErr string) error
}

type SQLStore struct {
	db *sql.DB
}

func NewStore(cfg Config) (*SQLStore, error) {
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	s := &SQLStore{db: db}
	if err := s.pingAndMigrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLStore) Close() error {
	return s.db.Close()
}

func (s *SQLStore) pingAndMigrate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS onchain_send_mappings (
			id BIGSERIAL PRIMARY KEY,
			user_rgb_invoice TEXT NOT NULL UNIQUE,
			lsp_ln_invoice TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			ln_expires_at TIMESTAMPTZ NULL,
			last_error TEXT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS lightning_receive_mappings (
			id BIGSERIAL PRIMARY KEY,
			user_ln_invoice TEXT NOT NULL UNIQUE,
			lsp_rgb_invoice TEXT NOT NULL UNIQUE,
			rgb_asset_id TEXT NULL,
			batch_transfer_idx BIGINT NULL,
			status TEXT NOT NULL,
			rgb_expires_at TIMESTAMPTZ NULL,
			last_error TEXT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS lnaddr_accounts (
			peer_pubkey TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS async_orders (
			order_id BIGSERIAL PRIMARY KEY,
			peer_pubkey TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			accepted_through_index BIGINT NULL,
			current_invoice_slot BIGINT NULL,
			current_hash_index BIGINT NULL,
			current_payment_hash TEXT NULL,
			current_invoice_id BIGINT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS async_hash_pool (
			id BIGSERIAL PRIMARY KEY,
			order_id BIGINT NOT NULL REFERENCES async_orders(order_id) ON DELETE CASCADE,
			hash_index BIGINT NOT NULL,
			payment_hash TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(order_id, hash_index),
			UNIQUE(order_id, payment_hash)
		);
		CREATE TABLE IF NOT EXISTS async_rotating_invoices (
			id BIGSERIAL PRIMARY KEY,
			order_id BIGINT NOT NULL REFERENCES async_orders(order_id) ON DELETE CASCADE,
			invoice_slot BIGINT NOT NULL,
			hash_index BIGINT NOT NULL,
			payment_hash TEXT NOT NULL,
			asset_amount BIGINT NULL,
			asset_id TEXT NULL,
			invoice_string TEXT NULL,
			amount_msat BIGINT NOT NULL,
			claimable_at TIMESTAMPTZ NULL,
			claim_deadline_height BIGINT NULL,
			outbound_pending_at TIMESTAMPTZ NULL,
			outbound_paid_at TIMESTAMPTZ NULL,
			request_invoice_at TIMESTAMPTZ NULL,
			request_invoice_bolt11 TEXT NULL,
			payment_preimage TEXT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(order_id, invoice_slot)
		);
		CREATE TABLE IF NOT EXISTS async_rotating_invoice_outbox (
			id BIGSERIAL PRIMARY KEY,
			payment_hash TEXT NOT NULL,
			action TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts BIGINT NOT NULL DEFAULT 0,
			available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			locked_until TIMESTAMPTZ NULL,
			last_error TEXT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(payment_hash, action)
		);
	`)
	return err
}

func (s *SQLStore) InsertOnchainSend(ctx context.Context, userRGBInvoice, lspLNInvoice string, lnExpiresAt *time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO onchain_send_mappings (user_rgb_invoice, lsp_ln_invoice, status, ln_expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, userRGBInvoice, lspLNInvoice, statusPendingLN, lnExpiresAt).Scan(&id)
	return id, err
}

func (s *SQLStore) InsertLightningReceive(ctx context.Context, userLNInvoice, lspRGBInvoice, rgbAssetID string, batchTransferIdx int64, rgbExpiresAt *time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO lightning_receive_mappings (user_ln_invoice, lsp_rgb_invoice, rgb_asset_id, batch_transfer_idx, status, rgb_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, userLNInvoice, lspRGBInvoice, rgbAssetID, batchTransferIdx, statusPendingRGB, rgbExpiresAt).Scan(&id)
	return id, err
}

func (s *SQLStore) GetLightningAddressAccountByUsername(ctx context.Context, username string) (LightningAddressAccount, error) {
	row := s.db.QueryRowContext(ctx, `SELECT peer_pubkey, username, created_at FROM lnaddr_accounts WHERE username = $1 LIMIT 1`, username)
	return scanLightningAddressAccount(row)
}

func (s *SQLStore) GetLightningAddressAccountByPeerPubkey(ctx context.Context, peerPubkey string) (LightningAddressAccount, error) {
	row := s.db.QueryRowContext(ctx, `SELECT peer_pubkey, username, created_at FROM lnaddr_accounts WHERE peer_pubkey = $1 LIMIT 1`, peerPubkey)
	return scanLightningAddressAccount(row)
}

func (s *SQLStore) InsertLightningAddressAccount(ctx context.Context, account LightningAddressAccount) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO lnaddr_accounts (peer_pubkey, username)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, account.PeerPubkey, account.Username)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *SQLStore) ListOnchainPending(ctx context.Context, limit int) ([]OnchainSendRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_rgb_invoice, lsp_ln_invoice, status, ln_expires_at, created_at FROM onchain_send_mappings WHERE status = $1 ORDER BY id ASC LIMIT $2`, statusPendingLN, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]OnchainSendRecord, 0)
	for rows.Next() {
		var r OnchainSendRecord
		var lnExpires sql.NullTime
		if err := rows.Scan(&r.ID, &r.UserRGBInvoice, &r.LspLNInvoice, &r.Status, &lnExpires, &r.CreatedAt); err != nil {
			return nil, err
		}
		if lnExpires.Valid {
			t := lnExpires.Time
			r.LNExpiresAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLStore) ListLightningPending(ctx context.Context, limit int) ([]LightningReceiveRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_ln_invoice, lsp_rgb_invoice, rgb_asset_id, batch_transfer_idx, status, rgb_expires_at, created_at FROM lightning_receive_mappings WHERE status = $1 ORDER BY id ASC LIMIT $2`, statusPendingRGB, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]LightningReceiveRecord, 0)
	for rows.Next() {
		var r LightningReceiveRecord
		var rgbExpires sql.NullTime
		if err := rows.Scan(&r.ID, &r.UserLNInvoice, &r.LspRGBInvoice, &r.RGBAssetID, &r.BatchTransferIdx, &r.Status, &rgbExpires, &r.CreatedAt); err != nil {
			return nil, err
		}
		if rgbExpires.Valid {
			t := rgbExpires.Time
			r.RGBExpiresAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLStore) UpdateOnchainStatus(ctx context.Context, id int64, status, lastErr string) error {
	if status == "" {
		return errors.New("empty status")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE onchain_send_mappings SET status = $1, last_error = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3`, status, nullIfEmpty(lastErr), id)
	return err
}

func (s *SQLStore) UpdateLightningStatus(ctx context.Context, id int64, status, lastErr string) error {
	if status == "" {
		return errors.New("empty status")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lightning_receive_mappings SET status = $1, last_error = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3`, status, nullIfEmpty(lastErr), id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLightningAddressAccount(row rowScanner) (LightningAddressAccount, error) {
	var account LightningAddressAccount
	var createdAt sql.NullTime
	if err := row.Scan(&account.PeerPubkey, &account.Username, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LightningAddressAccount{}, errLightningAddressAccountNotFound
		}
		return LightningAddressAccount{}, err
	}
	if createdAt.Valid {
		account.CreatedAt = createdAt.Time
	}
	return account, nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func wrapErr(msg string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}
