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

## Endpoints

- `GET /health`
- `GET /get_info`
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
- `RGB_NODE_BASE_URL` default `LSP_BASE_URL`
- `HTTP_TIMEOUT` default `15s`
- `CRON_EVERY` default `30s`
- `EXPIRY_MATCH_TOLERANCE_SEC` default `5`
- `MIN_AMT_MSAT` default `3000000`
- `MIN_CONFIRMATIONS` default `1`
- `DEFAULT_RGB_ASSIGNMENT` default `Any`
- `SUPPORTED_ASSET_IDS` comma-separated allowlist (example: `assetA,assetB`)
- `DEFAULT_VIRTUAL_OPEN_MODE` optional

Route override env vars:

- `LSP_GET_INFO_PATH`, `LSP_OPENCONNECTION_PATH`, `LSP_LISTCONNECTIONS_PATH`, `LSP_LISTCHANNELS_PATH`, `LSP_OPENCHANNEL_PATH`
- `LSP_LNINVOICE_PATH`, `LSP_INVOICESTATUS_PATH`, `LSP_CANCELLNINVOICE_PATH`, `LSP_SENDRGB_PATH`, `LSP_SENDLN_PATH`
- `RGB_DECODE_LN_PATH`, `RGB_DECODE_RGB_PATH`, `RGB_INVOICE_PATH`, `RGB_REFRESH_TRANSFERS_PATH`, `RGB_LIST_TRANSFERS_PATH`, `RGB_LIST_UNSPENTS_PATH`, `RGB_CREATE_UTXOS_PATH`

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
export RGB_NODE_BASE_URL="http://127.0.0.1:3001"
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

## CI e2e with RLN

This repository includes a CI workflow that reuses the canonical regtest
bootstrap from `rgb-lightning-node` instead of duplicating setup logic.

- Workflow: `.github/workflows/e2e-rln.yml`
- Runner script: [`./scripts/ci_e2e_with_rln.sh`](./scripts/ci_e2e_with_rln.sh)
- RLN checkout is pinned via `RLN_REF` in the workflow for reproducibility

The flow is:

1. checkout `utexo-lsp`
2. checkout `rgb-lightning-node` at pinned ref
3. run RLN `./regtest.sh start`
4. start two RLN nodes in docker
5. start `utexo-lsp`
6. run PoC checks (`preflight`, `two-nodes-openchannel-verify`)

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
