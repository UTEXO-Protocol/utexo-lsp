# CLAUDE.md — utexo-lsp

## Project overview

`utexo-lsp` is a Lightning Service Provider (LSP) bridge API written in Go that enables cross-protocol payments between RGB (on-chain assets) and Lightning Network. It sits between a `rgb-lightning-node` backend and end users, orchestrating invoice creation, transfer monitoring, channel management, and async payment flows.

## Stack & key dependencies

- **Go 1.26**, standard library `net/http` (no framework)
- **Database**: SQLite (default, via `github.com/mattn/go-sqlite3`, CGO required) or PostgreSQL (`github.com/lib/pq`)
- **Crypto**: `github.com/btcsuite/btcd/btcec/v2` for secp256k1 operations (Merkle proof / APay batch signing)
- **External dependency**: `rgb-lightning-node` REST API — all node calls go through `pkg/node_client`
- **E2E tests**: Python (`tests/e2e/`), unit tests in Go (`internal/lspapi/*_test.go`)

## How to build and test

```bash
# Build (CGO required for sqlite3)
CGO_ENABLED=1 go build -o application ./main.go

# Run locally
cp .env.example .env && go run .

# Go unit tests
go test ./internal/lspapi/...

# Docker image (multi-stage, alpine)
docker build -t utexo-lsp .

# Python e2e tests
pytest tests/e2e/
```

Configuration is entirely via environment variables; see `.env.example` for the full list.

## Project structure

```
main.go                          Entrypoint — calls lspapi.Run()
internal/lspapi/
  run.go                         Server bootstrap, graceful shutdown, cron goroutine
  api.go                         HTTP handlers, cron tick, all business logic
  config.go                      Env-based config loading and validation
  models.go                      All types: API requests/responses, DB records, state enums
  db.go                          Store interface + SQLStore impl (SQLite + Postgres, inline migrations)
  lightning_send.go              POST /lightning_send — HODL relay flow
  lightning_address.go           LNURL-pay discovery and callback
  lightning_address_accounts.go  Account seeding from peer list
  lightning_send_store.go        DB ops for lightning_send_mappings
  async_order.go                 APay async payment order DB ops
  apay_batch_store.go            APay batch commitment / hash pool DB ops
  apay_merkle.go                 Merkle proof construction for APay invoice proofs
  async_payments_protocol.go     APay protocol constants
  convertible_asset.go           Asset pair resolution for 1:1 cross-asset conversion
  get_info.go                    GET /get_info with TTL-cached asset metadata
  *_test.go                      Unit tests (table-driven, no test DB — logic only)
pkg/node_client/
  client.go                      HTTP client for rgb-lightning-node (Bearer auth, JSON)
  node_endpoints.go              Typed wrappers for all node API calls
```

## Code conventions & patterns

- **No framework**: routing uses Go 1.22+ `http.ServeMux` with method+path patterns (`"POST /foo"`).
- **Context + timeout on every handler**: `context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)`.
- **Internal endpoints** (`/internal/async_order/*`) are authenticated via `APAY_BEARER_TOKEN` Bearer header — always check auth before any logic.
- **u64 as JSON strings**: `GetInfoResponse` serializes large integers as strings to avoid JS precision loss. All new u64 fields on public responses must follow this.
- **Dual-driver SQL**: all queries are written twice (SQLite `?` placeholders, Postgres `$N`). The `rebindPostgres` helper converts `?` to `$N`. New queries must handle both drivers.
- **Inline migrations**: `pingAndMigrate` runs `ALTER TABLE … ADD COLUMN IF NOT EXISTS` on every startup. Additive-only — no destructive migrations.
- **State machines with rank tables**: `lightningSendStateRank` and `asyncRotatingInvoiceStatusAtOrBeyond` guard against replays and backward transitions. New states must be added to rank maps.
- **Error sentinels**: domain errors are `var err… = errors.New(…)` in db.go; callers use `errors.Is`.
- **Cron outbox pattern**: side-effectful async jobs are queued in `async_rotating_invoice_outbox` and processed by `runAsyncOrderOutbox` — at-most-once per tick, up to 10 per tick.
- **Logging**: plain `log.Printf`; no structured logger.
- **`Store` interface**: all DB access goes through the interface defined in `db.go`; unit tests can mock it without a real DB.

## Code review focus areas

### Security
- **Internal endpoint auth**: `/internal/async_order/*` routes must check `APAY_BEARER_TOKEN` before processing. An empty token must reject all requests.
- **Preimage verification**: `handleInternalAsyncOrderPaymentSent` validates `sha256(preimage) == payment_hash` before accepting. Any shortcut here breaks atomicity.
- **Payment hash uniqueness**: `lightning_send_mappings` and `async_rotating_invoices` use `payment_hash` as unique key. Duplicate checks must run before any HODL invoice is created.
- **CONVERTIBLE_PAIRS authorization**: this list is the sole authorization for 1:1 asset conversion. Bypassing it exposes the LSP to inventory loss.
- **Claim deadline enforcement**: `aPayRequestOutboundInvoiceJob` must validate `claim_deadline_height` before paying. Paying after deadline means the LSP cannot collect the inbound HTLC.

### Correctness
- **Cron idempotency**: outbox jobs use `(payment_hash, action)` unique constraints. New outbox action types must include this constraint.
- **`canDeliverNow` check before every LN payment attempt**: skipping it can burn a delivery attempt against a channel that cannot carry the payment.
- **`SetLightningAddressPayoutAsset` is write-once**: the payout asset must never be overwritten; the `WHERE payout_asset_id IS NULL` guard is load-bearing.
- **Duplicate-driver query divergence**: after any SQL change, confirm both SQLite and Postgres paths are updated.

### Performance
- **Cron batch limit**: `ListOnchainPending` and `ListLightningPending` cap at 200 rows per tick.
- **`GET /get_info` asset TTL cache**: avoids a node call per request. Bypassing the cache is a regression.
- **Node client timeout**: all node calls use `cfg.HTTPTimeout`. New node calls must pass a bounded context.
