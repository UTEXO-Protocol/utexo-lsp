package lspapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
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
	BuildApayInvoiceProof(ctx context.Context, orderID int64, hashIndex int64) (*ApayInvoiceProof, error)
	GetApayAddressAttestation(ctx context.Context, peerPubkey string) (sig *string, err error)
	ListOnchainPending(ctx context.Context, limit int) ([]OnchainSendRecord, error)
	ListLightningPending(ctx context.Context, limit int) ([]LightningReceiveRecord, error)
	UpdateOnchainStatus(ctx context.Context, id int64, status, lastErr string) error
	UpdateLightningStatus(ctx context.Context, id int64, status, lastErr string) error
}

type SQLStore struct {
	db     *sql.DB
	driver string
}

func NewStore(cfg Config) (*SQLStore, error) {
	driver := cfg.DatabaseDriver
	dsn := cfg.DatabaseURL
	if driver == "sqlite" {
		driver = "sqlite3"
	}
	if driver == "postgres" {
		driver = "postgres"
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}

	s := &SQLStore{db: db, driver: cfg.DatabaseDriver}
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

	if s.driver == "postgres" {
		_, err := s.db.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS onchain_send_mappings (
				id BIGSERIAL PRIMARY KEY,
				user_rgb_invoice TEXT NOT NULL UNIQUE,
				lsp_ln_invoice TEXT NOT NULL UNIQUE,
				status TEXT NOT NULL,
				ln_expires_at TIMESTAMPTZ NULL,
				last_error TEXT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);
			CREATE TABLE IF NOT EXISTS lightning_receive_mappings (
				id BIGSERIAL PRIMARY KEY,
				user_ln_invoice TEXT NOT NULL UNIQUE,
				lsp_rgb_invoice TEXT NOT NULL UNIQUE,
				rgb_asset_id TEXT NOT NULL,
				batch_transfer_idx BIGINT NOT NULL,
				status TEXT NOT NULL,
				rgb_expires_at TIMESTAMPTZ NULL,
				last_error TEXT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);
			CREATE TABLE IF NOT EXISTS lnaddr_accounts (
				peer_pubkey TEXT PRIMARY KEY,
				username TEXT NOT NULL UNIQUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
		`)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `
			ALTER TABLE lightning_receive_mappings ADD COLUMN IF NOT EXISTS rgb_asset_id TEXT;
			ALTER TABLE lightning_receive_mappings ADD COLUMN IF NOT EXISTS batch_transfer_idx BIGINT;
		`)
		return err
	}

	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS onchain_send_mappings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_rgb_invoice TEXT NOT NULL UNIQUE,
			lsp_ln_invoice TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			ln_expires_at DATETIME NULL,
			last_error TEXT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS lightning_receive_mappings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_ln_invoice TEXT NOT NULL UNIQUE,
			lsp_rgb_invoice TEXT NOT NULL UNIQUE,
			rgb_asset_id TEXT,
			batch_transfer_idx INTEGER,
			status TEXT NOT NULL,
			rgb_expires_at DATETIME NULL,
			last_error TEXT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS lnaddr_accounts (
			peer_pubkey TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS async_orders (
			order_id INTEGER PRIMARY KEY AUTOINCREMENT,
			peer_pubkey TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			accepted_through_index INTEGER NULL,
			current_invoice_slot INTEGER NULL,
			current_hash_index INTEGER NULL,
			current_payment_hash TEXT NULL,
			current_invoice_id INTEGER NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS async_hash_pool (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id INTEGER NOT NULL REFERENCES async_orders(order_id) ON DELETE CASCADE,
			hash_index INTEGER NOT NULL,
			payment_hash TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(order_id, hash_index),
			UNIQUE(order_id, payment_hash)
		);
		CREATE TABLE IF NOT EXISTS async_rotating_invoices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id INTEGER NOT NULL REFERENCES async_orders(order_id) ON DELETE CASCADE,
			invoice_slot INTEGER NOT NULL,
			hash_index INTEGER NOT NULL,
			payment_hash TEXT NOT NULL,
			asset_amount INTEGER NULL,
			asset_id TEXT NULL,
			invoice_string TEXT NULL,
			amount_msat INTEGER NOT NULL,
			claimable_at DATETIME NULL,
			claim_deadline_height INTEGER NULL,
			outbound_pending_at DATETIME NULL,
			outbound_paid_at DATETIME NULL,
			request_invoice_at DATETIME NULL,
			request_invoice_bolt11 TEXT NULL,
			payment_preimage TEXT NULL,
			expires_at DATETIME NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(order_id, invoice_slot)
		);
		CREATE TABLE IF NOT EXISTS async_rotating_invoice_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			payment_hash TEXT NOT NULL,
			action TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			available_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			locked_until DATETIME NULL,
			last_error TEXT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(payment_hash, action)
		);
		CREATE TABLE IF NOT EXISTS apay_hash_batch (
			batch_id TEXT PRIMARY KEY,
			order_id INTEGER NOT NULL REFERENCES async_orders(order_id) ON DELETE CASCADE,
			recipient_pubkey TEXT NOT NULL,
			host_pubkey TEXT NOT NULL,
			batch_root TEXT NOT NULL,
			batch_size INTEGER NOT NULL,
			batch_sig TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NULL,
			inserted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_orders", "accepted_through_index INTEGER"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "lightning_receive_mappings", "rgb_asset_id TEXT"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "lightning_receive_mappings", "batch_transfer_idx INTEGER"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "claimable_at DATETIME NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "claim_deadline_height INTEGER NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "asset_amount INTEGER NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "asset_id TEXT NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "outbound_pending_at DATETIME NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "outbound_paid_at DATETIME NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "request_invoice_at DATETIME NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "request_invoice_bolt11 TEXT NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_rotating_invoices", "payment_preimage TEXT NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "async_hash_pool", "batch_id TEXT NULL"); err != nil {
		return err
	}
	if err := s.tryAddColumnSQLite(ctx, "lnaddr_accounts", "address_sig TEXT NULL"); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE async_rotating_invoices
		SET status = CASE
			WHEN payment_preimage IS NOT NULL THEN 'outbound_claimed'
			WHEN outbound_paid_at IS NOT NULL THEN 'outbound_paid'
			WHEN outbound_pending_at IS NOT NULL THEN 'outbound_pending'
			WHEN request_invoice_at IS NOT NULL THEN 'outbound_requested'
			WHEN claimable_at IS NOT NULL THEN 'claimable'
			WHEN invoice_string IS NOT NULL THEN 'active'
			ELSE status
		END
		WHERE status IN ('reserved', 'active', 'failed');
	`)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE async_orders
		SET accepted_through_index = COALESCE(
			accepted_through_index,
			(SELECT COALESCE(MAX(hash_index), 0) FROM async_hash_pool WHERE async_hash_pool.order_id = async_orders.order_id)
		)
		WHERE accepted_through_index IS NULL;
	`)
	return err
}

func (s *SQLStore) InsertOnchainSend(ctx context.Context, userRGBInvoice, lspLNInvoice string, lnExpiresAt *time.Time) (int64, error) {
	if s.driver == "postgres" {
		var id int64
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO onchain_send_mappings (user_rgb_invoice, lsp_ln_invoice, status, ln_expires_at)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, userRGBInvoice, lspLNInvoice, statusPendingLN, lnExpiresAt).Scan(&id)
		return id, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO onchain_send_mappings (user_rgb_invoice, lsp_ln_invoice, status, ln_expires_at)
		VALUES (?, ?, ?, ?)
	`, userRGBInvoice, lspLNInvoice, statusPendingLN, lnExpiresAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *SQLStore) InsertLightningReceive(ctx context.Context, userLNInvoice, lspRGBInvoice, rgbAssetID string, batchTransferIdx int64, rgbExpiresAt *time.Time) (int64, error) {
	if s.driver == "postgres" {
		var id int64
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO lightning_receive_mappings (user_ln_invoice, lsp_rgb_invoice, rgb_asset_id, batch_transfer_idx, status, rgb_expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id
		`, userLNInvoice, lspRGBInvoice, rgbAssetID, batchTransferIdx, statusPendingRGB, rgbExpiresAt).Scan(&id)
		return id, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO lightning_receive_mappings (user_ln_invoice, lsp_rgb_invoice, rgb_asset_id, batch_transfer_idx, status, rgb_expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, userLNInvoice, lspRGBInvoice, rgbAssetID, batchTransferIdx, statusPendingRGB, rgbExpiresAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *SQLStore) GetLightningAddressAccountByUsername(ctx context.Context, username string) (LightningAddressAccount, error) {
	query := `SELECT peer_pubkey, username, created_at FROM lnaddr_accounts WHERE username = ? LIMIT 1`
	args := []any{username}
	if s.driver == "postgres" {
		query = `SELECT peer_pubkey, username, created_at FROM lnaddr_accounts WHERE username = $1 LIMIT 1`
	}
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanLightningAddressAccount(row)
}

func (s *SQLStore) GetLightningAddressAccountByPeerPubkey(ctx context.Context, peerPubkey string) (LightningAddressAccount, error) {
	query := `SELECT peer_pubkey, username, created_at FROM lnaddr_accounts WHERE peer_pubkey = ? LIMIT 1`
	args := []any{peerPubkey}
	if s.driver == "postgres" {
		query = `SELECT peer_pubkey, username, created_at FROM lnaddr_accounts WHERE peer_pubkey = $1 LIMIT 1`
	}
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanLightningAddressAccount(row)
}

func (s *SQLStore) InsertLightningAddressAccount(ctx context.Context, account LightningAddressAccount) (bool, error) {
	if s.driver == "postgres" {
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
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO lnaddr_accounts (peer_pubkey, username)
		VALUES (?, ?)
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
	query := `SELECT id, user_rgb_invoice, lsp_ln_invoice, status, ln_expires_at, created_at FROM onchain_send_mappings WHERE status = ? ORDER BY id ASC LIMIT ?`
	args := []any{statusPendingLN, limit}
	if s.driver == "postgres" {
		query = `SELECT id, user_rgb_invoice, lsp_ln_invoice, status, ln_expires_at, created_at FROM onchain_send_mappings WHERE status = $1 ORDER BY id ASC LIMIT $2`
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	query := `SELECT id, user_ln_invoice, lsp_rgb_invoice, rgb_asset_id, batch_transfer_idx, status, rgb_expires_at, created_at FROM lightning_receive_mappings WHERE status = ? ORDER BY id ASC LIMIT ?`
	args := []any{statusPendingRGB, limit}
	if s.driver == "postgres" {
		query = `SELECT id, user_ln_invoice, lsp_rgb_invoice, rgb_asset_id, batch_transfer_idx, status, rgb_expires_at, created_at FROM lightning_receive_mappings WHERE status = $1 ORDER BY id ASC LIMIT $2`
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	if s.driver == "postgres" {
		_, err := s.db.ExecContext(ctx, `UPDATE onchain_send_mappings SET status=$1, last_error=$2, updated_at=NOW() WHERE id=$3`, status, nullIfEmpty(lastErr), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE onchain_send_mappings SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, nullIfEmpty(lastErr), id)
	return err
}

func (s *SQLStore) UpdateLightningStatus(ctx context.Context, id int64, status, lastErr string) error {
	if status == "" {
		return errors.New("empty status")
	}
	if s.driver == "postgres" {
		_, err := s.db.ExecContext(ctx, `UPDATE lightning_receive_mappings SET status=$1, last_error=$2, updated_at=NOW() WHERE id=$3`, status, nullIfEmpty(lastErr), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lightning_receive_mappings SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, nullIfEmpty(lastErr), id)
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

func (s *SQLStore) tryAddColumnSQLite(ctx context.Context, table, columnDef string) error {
	if s.driver == "postgres" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDef))
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return nil
	}
	return err
}
