# utexo-lsp

POC bridge API for RGB + Lightning LSP workflows.

## Table of contents

- [Overview](#overview)
- [Endpoints](#endpoints)
- [Request examples](#request-examples)
- [Cron jobs](#cron-jobs)
- [Method mapping and transfer status model](#method-mapping-and-transfer-status-model)
- [Configuration](#configuration)
- [Run locally](#run-locally)
- [Manual flow tests](#manual-flow-tests)
- [Automation script](#automation-script)
- [Troubleshooting](#troubleshooting)

## Overview

This service exposes API endpoints for two flows:

- `onchain_send`: user provides RGB invoice -> service creates LN invoice -> once paid, service executes `sendrgb`
- `lightning_receive`: user provides LN invoice -> service creates RGB invoice -> once RGB transfer settles, service executes `sendpayment`
- `lightning_address`: user provides `username@domain` -> service serves LNURL-pay discovery and callback for a DB-backed account, with haiku handles minted once and persisted per `peer_pubkey`

## Endpoints

- `GET /health`
- `GET /get_info`
- `GET /.well-known/lnurlp/{username}`
- `GET /pay/callback/{username}`
- `GET /lightning_address/by_pubkey/{pubkey}`
- `POST /lightning_address`
- `POST /onchain_send`
- `POST /lightning_receive`

## Request examples

### `POST /onchain_send`

```json
{
  "rgb_invoice": "rgb1...",
  "lninvoice": {
    "amt_msat": 3000000,
    "expiry_sec": 3600,
    "asset_id": "...",
    "asset_amount": 1000
  }
}
```

Validation rules:

- if `lninvoice.asset_id` is provided, it must match decoded RGB `asset_id`
- if `lninvoice.asset_amount` is provided, it must match decoded fungible assignment amount
- if either field is omitted, service auto-fills from decoded RGB invoice when available
- `lninvoice.expiry_sec` must match decoded RGB remaining lifetime (within tolerance)
- if `lninvoice.expiry_sec` is omitted/zero, service auto-fills from RGB remaining lifetime

### `POST /lightning_receive`

```json
{
  "ln_invoice": "lnbc...",
  "rgb_invoice": {
    "asset_id": "...",
    "assignment": "Value",
    "duration_seconds": 3600,
    "min_confirmations": 1,
    "witness": false
  }
}
```

Validation and normalization:

- `rgb_invoice.asset_id` is required
- `rgb_invoice.min_confirmations` is backend-controlled via `MIN_CONFIRMATIONS`; caller value is ignored
- assignment default is `Any`
- input `"Value"` is accepted and normalized to `Any`
- unsupported assignment values are rejected
- `duration_seconds` is validated against LN remaining lifetime; if missing/zero, auto-filled from decoded LN invoice

### `GET /.well-known/lnurlp/{username}`

Returns LNURL-pay discovery metadata for a Lightning Address account stored in `lnaddr_accounts`.

Example:

```bash
curl -s http://127.0.0.1:8080/.well-known/lnurlp/txalkan
```

### `GET /pay/callback/{username}?amount=<msat>`

Returns a BOLT11 invoice for the requested amount in millisatoshis.
The callback includes the LNURL metadata hash as `description_hash` in the underlying `/lninvoice` request, which is required by LUD-06 so the invoice `h` tag matches the metadata string.
It also sends `min_final_cltv_expiry_delta` from the configured inbound Lightning Address CLTV policy.

Example:

```bash
curl -s "http://127.0.0.1:8080/pay/callback/txalkan?amount=3000000"
```

### `POST /lightning_address`

Sets or updates an arbitrary, unique Lightning Address for a node public key. To prevent hijacking, requests must be cryptographically signed by the node's private key.

Example request:

```json
{
  "pubkey": "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
  "username": "alice",
  "signature": "d9pnu65rp497f5or7ik9e6o47y4hata65wkbssh7q7we18t68g1jcdd3k4s9iepmk6w94crndiiazfq6meekg8ndkxikfbw8cnff4cwh"
}
```

Validation & Security Rules:
* `pubkey` must be a valid compressed hex-encoded secp256k1 public key.
* `username` must be normalized (lowercased, trimmed) and strictly **between 3 and 64 characters**.
* `username` must match the strict pattern `^[a-z0-9]+([._-][a-z0-9]+)*$` (preventing invalid symbols, consecutive delimiters or leading/trailing hyphens/dots).
* `username` must not be a blacklisted/reserved system handle (e.g. `admin`, `support`, `root`, `well-known`). Returns `403 Forbidden`.
* `signature` is required and must decode (from hex or standard zbase32 compact format) to exactly 65 bytes.
* The signature must be verified using public-key recovery against the double-SHA256 message hash of `Lightning Signed Message:username`. Returns `401 Unauthorized` if verification fails.
* Request body size is capped at `4KB` via `http.MaxBytesReader` to protect against DoS memory exhaustion.

### `GET /lightning_address/by_pubkey/{pubkey}`

Returns the registered handle and domain, along with the fully qualified `lightning_address` string for the given node public key.

Example request:

```bash
curl -s http://127.0.0.1:8080/lightning_address/by_pubkey/0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798
```

Example response:

```json
{
  "username": "alice",
  "domain": "users.utexo.com",
  "lightning_address": "alice@users.utexo.com"
}
```

## Cron jobs

Runs every `CRON_EVERY` (default `30s`):

1. `listpeers` + `listchannels`, and auto `openchannel` if channel is missing.
2. UTXO maintenance: if count drops below `UTXO_MIN_COUNT`, call `createutxos` with `UTXO_TARGET_COUNT - UTXO_MIN_COUNT`.
3. Monitor LN invoices for `onchain_send`; if paid, execute `sendrgb`.
4. Monitor RGB transfers for `lightning_receive`; if settled, execute `sendpayment`.
5. Mark expired unpaid invoices as `expired` and optionally call cancel endpoint.

## Method mapping and transfer status model

This POC maps `rgb-lightning-node` routes:

- `listconnections` -> `listpeers`
- `openconnection` -> `connectpeer` (or rely on `openchannel` auto-connect)
- `sendln` -> `sendpayment`
- `rgbinvoicestatus` -> `refreshtransfers` + `listtransfers` (matched by `batch_transfer_idx`)

Why `refreshtransfers + listtransfers`:

1. `POST /rgbinvoice` returns `batch_transfer_idx` and `expiration_timestamp`.
2. `POST /refreshtransfers` updates wallet transfer states.
3. `POST /listtransfers` returns transfer states for an `asset_id`.
4. Transfer with `idx == batch_transfer_idx` is used as tracked invoice state.

Relevant transfer states:

- `WaitingCounterparty`
- `WaitingConfirmations`
- `Settled`
- `Failed`

For deterministic tracking of `lightning_receive`, persist:

- user LN invoice
- generated RGB invoice
- `batch_transfer_idx`
- `asset_id`
- `expiration_timestamp` (`rgb_expires_at`)

## Configuration

Core env vars:

- `SERVER_ADDR` default `:8080`
- `DATABASE_DRIVER` `sqlite` (default) or `postgres`
- `DATABASE_URL` default `utexo_lsp.db`
- `LSP_BASE_URL` default `http://127.0.0.1:3001`
- `LSP_TOKEN` optional bearer token used by utexo-lsp for outbound calls to the node API
- `RGB_NODE_BASE_URL` default `LSP_BASE_URL`
- `HTTP_TIMEOUT` default `15s`
- `CRON_EVERY` default `30s`
- `EXPIRY_MATCH_TOLERANCE_SEC` default `5`
- `MIN_AMT_MSAT` default `3000000`
- `MIN_CONFIRMATIONS` default `1`
- `DEFAULT_RGB_ASSIGNMENT` default `Any`
- `SUPPORTED_ASSET_IDS` comma-separated allowlist (example: `assetA,assetB`)
- `DEFAULT_VIRTUAL_OPEN_MODE` optional

Lightning Address / Async Payments (APay) env vars:

- `LIGHTNING_ADDRESS_DOMAIN_URL` default `http://127.0.0.1:8080` (must be an http(s) origin only, with no path/query/fragment; host used for `username@domain`)
- `LIGHTNING_ADDRESS_SHORT_DESCRIPTION` default `Payment to utexo-lsp`
- `LIGHTNING_ADDRESS_MIN_SENDABLE_MSAT` default `3_000_000`
- `LIGHTNING_ADDRESS_MAX_SENDABLE_MSAT` default `3_000_000`
- `APAY_INBOUND_INVOICE_EXPIRY` default `3600s` (`APayInboundInvoiceExpiry` in config)
- `APAY_OUTBOUND_INVOICE_EXPIRY` default `900s` (`APayOutboundInvoiceExpiry` in config)
- `APAY_INBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA` default `144` (`APayInboundMinFinalCltvExpiryDelta` in config)
- `APAY_OUTBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA` default `18` (`APayOutboundMinFinalCltvExpiryDelta` in config)
- `APAY_CLAIM_MARGIN_BLOCKS` default `12` (`APayClaimMarginBlocks` in config)
- `APAY_BEARER_TOKEN` bearer token required for `POST /internal/async_order/new` (`APayBearerToken` in config)
- `ADMIN_HANDLES` (optional): comma-separated `username:pubkey` pairs (e.g. `admin:0279be66...`) defining static, database-independent administrative accounts in memory (bypasses signature check and database fallback).

Lightning address accounts:

- `lnaddr_accounts.peer_pubkey` is the primary key
- The `localpart` (used as `username`) is generated once using `go-haikunator` and then stored persistently.
- `reconcileChannels` seeds accounts automatically for peers discovered from `listconnections` or the `listpeers` fallback

UTXO/channel tuning:

- `DEFAULT_CHANNEL_CAPACITY_SAT` default `200000`
- `DEFAULT_CHANNEL_PUSH_MSAT` default `0`
- `UTXO_MIN_COUNT`, `UTXO_TARGET_COUNT`, `UTXO_SIZE_SAT`, `UTXO_FEE_RATE`, `UTXO_SKIP_SYNC`

`SUPPORTED_ASSET_IDS` behavior:

- BTC channels (empty `asset_id`) are allowed
- RGB channels are auto-opened only if `asset_id` is in allowlist
- `POST /lightning_receive` and `POST /onchain_send` reject asset IDs outside allowlist
- if allowlist is empty, asset-bound flows are rejected

## Run locally

From project root:

```bash
export LSP_BASE_URL="http://127.0.0.1:3001"
export LSP_TOKEN=""
export RGB_NODE_BASE_URL="http://127.0.0.1:3001"
export LIGHTNING_ADDRESS_DOMAIN_URL="http://127.0.0.1:8080"
export APAY_BEARER_TOKEN=""
export CRON_EVERY="10s"
go run .
```

Health check:

```bash
curl -s http://127.0.0.1:8080/health
```

## Manual flow tests

### 1) `lightning_receive` (`ln -> rgb -> sendpayment`)

```bash
curl -s -X POST http://127.0.0.1:8080/lightning_receive \
  -H 'content-type: application/json' \
  -d '{
    "ln_invoice":"<USER_LN_INVOICE>",
    "rgb_invoice":{
      "asset_id":"<ASSET_ID>",
      "assignment":"Value",
      "duration_seconds":3600,
      "min_confirmations":1,
      "witness":false
    }
  }'
```

Then pay the returned RGB invoice and check status:

```bash
sqlite3 utexo_lsp.db "select id,status,rgb_asset_id,batch_transfer_idx,created_at from lightning_receive_mappings order by id desc limit 5;"
```

Expected: `pending_rgb -> completed` (or `failed` / `expired`).

### 2) `onchain_send` (`rgb -> ln -> sendrgb`)

```bash
curl -s -X POST http://127.0.0.1:8080/onchain_send \
  -H 'content-type: application/json' \
  -d '{
    "rgb_invoice":"<USER_RGB_INVOICE>",
    "lninvoice":{
      "amt_msat":3000000,
      "expiry_sec":3600
    }
  }'
```

Then pay the returned LN invoice and check status:

```bash
sqlite3 utexo_lsp.db "select id,status,created_at from onchain_send_mappings order by id desc limit 5;"
```

Expected: `pending_ln -> completed` (or `failed` / `expired`).

## Automation script

Use [`./scripts/poc_flow.sh`](./scripts/poc_flow.sh).

Quick start:

```bash
# Optional one-time init
NODE_PASSWORD="password123" ./scripts/poc_flow.sh node-init

# Unlock node
NODE_PASSWORD="password123" \
BITCOIND_RPC_USERNAME="user" \
BITCOIND_RPC_PASSWORD="password" \
BITCOIND_RPC_HOST="localhost" \
BITCOIND_RPC_PORT=18443 \
INDEXER_URL="127.0.0.1:50001" \
PROXY_ENDPOINT="rpc://127.0.0.1:3000/json-rpc" \
./scripts/poc_flow.sh node-unlock

./scripts/poc_flow.sh preflight
./scripts/poc_flow.sh node-initial
```

Auth check:

```bash
NODE_BASE_URL="http://127.0.0.1:3001" \
NODE_TOKEN="<YOUR_RLN_TOKEN>" \
AUTH_CHECK_PATH="/nodeinfo" \
./scripts/poc_flow.sh auth-check
```

`lightning_receive` script flow:

```bash
ASSET_ID="<ASSET_ID>" \
USER_LN_INVOICE="<USER_LN_INVOICE>" \
AUTO_PAY_RGB=true \
./scripts/poc_flow.sh lightning-receive
./scripts/poc_flow.sh monitor
```

`onchain_send` script flow:

```bash
USER_RGB_INVOICE="<USER_RGB_INVOICE>" \
LN_AMT_MSAT=3000000 \
LN_EXPIRY_SEC=3600 \
AUTO_PAY_LN=true \
./scripts/poc_flow.sh onchain-send
./scripts/poc_flow.sh monitor
```

All-in-one run:

```bash
NODE_PASSWORD="password123" \
BITCOIND_RPC_USERNAME="user" \
BITCOIND_RPC_PASSWORD="password" \
BITCOIND_RPC_HOST="localhost" \
BITCOIND_RPC_PORT=18443 \
INDEXER_URL="127.0.0.1:50001" \
PROXY_ENDPOINT="rpc://127.0.0.1:3000/json-rpc" \
ASSET_ID="<ASSET_ID>" \
USER_LN_INVOICE="<USER_LN_INVOICE>" \
USER_RGB_INVOICE="<USER_RGB_INVOICE>" \
AUTO_PAY_LN=true \
AUTO_PAY_RGB=true \
WAIT_SECONDS=20 \
./scripts/poc_flow.sh all
```

Two-node openchannel verification:

```bash
NODE_BASE_URL="http://127.0.0.1:3001" \
SECOND_NODE_BASE_URL="http://127.0.0.1:3002" \
SECOND_NODE_P2P_ADDR="127.0.0.1:9736" \
OPENCHANNEL_VERIFY_TIMEOUT=120 \
OPENCHANNEL_VERIFY_INTERVAL=5 \
./scripts/poc_flow.sh two-nodes-openchannel-verify
```

SDK client smoke:

```bash
NODE_BASE_URL="http://127.0.0.1:3001" \
SECOND_NODE_BASE_URL="http://127.0.0.1:3002" \
SERVER_ASSET_ID="<ASSET_ID_ON_NODE_A>" \
CLIENT_ASSET_ID="<ASSET_ID_ON_NODE_B>" \
CLIENT_LN_AMT_MSAT=3000000 \
CLIENT_LN_EXPIRY_SEC=3600 \
LN_AMT_MSAT=3000000 \
LN_EXPIRY_SEC=3600 \
./scripts/poc_flow.sh sdk-client-smoke
```

## Troubleshooting

- `lightning_receive` not completing:
  - inspect `POST /refreshtransfers` and `POST /listtransfers`
  - verify `asset_id` matches transfer records
- `POST /lninvoice` EOF/empty reply:
  - verify bitcoind RPC port (regtest here uses `18443`)
  - ensure node data dir has `.ldk/`
  - restart node after fixing `.ldk`
- auto `openchannel` failing:
  - verify peers via `GET /listpeers`
  - verify channel defaults are valid for node policy
