package lspapi

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testPostgresDatabaseURL = "postgres://pguser:pgpass@127.0.0.1:5432/utexo_lsp?sslmode=disable"

func resetTestPostgresSchema(t *testing.T) {
	t.Helper()

	db := openTestPostgresDB(t)
	defer func() {
		_ = db.Close()
	}()

	var err error
	_, err = db.ExecContext(context.Background(), `
		DROP SCHEMA IF EXISTS public CASCADE;
		CREATE SCHEMA public;
	`)
	require.NoError(t, err)
}

func openTestPostgresDB(t *testing.T) *sql.DB {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	var lastErr error

	// wait for db to start with a deadline and backoff.
	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", testPostgresDatabaseURL)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}

		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return db
		}

		_ = db.Close()
		time.Sleep(250 * time.Millisecond)
	}

	require.NoError(t, lastErr)
	return nil
}

func newPostgresTestStore(t *testing.T) *SQLStore {
	t.Helper()

	resetTestPostgresSchema(t)

	store, err := NewStore(Config{
		DatabaseURL: testPostgresDatabaseURL,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
		resetTestPostgresSchema(t)
	})
	return store
}
