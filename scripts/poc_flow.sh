#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

API_BASE_URL="${API_BASE_URL:-http://127.0.0.1:8080}"
NODE_BASE_URL="${NODE_BASE_URL:-http://127.0.0.1:3001}"
SECOND_NODE_BASE_URL="${SECOND_NODE_BASE_URL:-http://127.0.0.1:3002}"
DB_PATH="${DB_PATH:-$PROJECT_ROOT/utexo_lsp.db}"
WAIT_SECONDS="${WAIT_SECONDS:-15}"
OPENCHANNEL_VERIFY_TIMEOUT="${OPENCHANNEL_VERIFY_TIMEOUT:-90}"
OPENCHANNEL_VERIFY_INTERVAL="${OPENCHANNEL_VERIFY_INTERVAL:-5}"
EXPECT_VIRTUAL_OPEN_MODE="${EXPECT_VIRTUAL_OPEN_MODE:-}"

# Optional bearer token for rgb-lightning-node.
NODE_TOKEN="${NODE_TOKEN:-}"
SECOND_NODE_TOKEN="${SECOND_NODE_TOKEN:-}"
NODE_PASSWORD="${NODE_PASSWORD:-password123}"
NODE_MNEMONIC="${NODE_MNEMONIC:-}"

# Unlock params (regtest defaults from rgb-lightning-node docs).
BITCOIND_RPC_USERNAME="${BITCOIND_RPC_USERNAME:-user}"
BITCOIND_RPC_PASSWORD="${BITCOIND_RPC_PASSWORD:-password}"
BITCOIND_RPC_HOST="${BITCOIND_RPC_HOST:-localhost}"
BITCOIND_RPC_PORT="${BITCOIND_RPC_PORT:-18443}"
INDEXER_URL="${INDEXER_URL:-127.0.0.1:50001}"
PROXY_ENDPOINT="${PROXY_ENDPOINT:-rpc://127.0.0.1:3000/json-rpc}"
ANNOUNCE_ADDRESSES="${ANNOUNCE_ADDRESSES:-[]}"
ANNOUNCE_ALIAS="${ANNOUNCE_ALIAS:-null}"

# Inputs for flows.
ASSET_ID="${ASSET_ID:-}"
SERVER_ASSET_ID="${SERVER_ASSET_ID:-$ASSET_ID}"
CLIENT_ASSET_ID="${CLIENT_ASSET_ID:-$ASSET_ID}"
USER_LN_INVOICE="${USER_LN_INVOICE:-}"
USER_RGB_INVOICE="${USER_RGB_INVOICE:-}"
LN_AMT_MSAT="${LN_AMT_MSAT:-3000000}"
LN_EXPIRY_SEC="${LN_EXPIRY_SEC:-0}" # LSP reserves the proxy ACK/delivery interval.
CLIENT_LN_AMT_MSAT="${CLIENT_LN_AMT_MSAT:-3000000}"
CLIENT_LN_EXPIRY_SEC="${CLIENT_LN_EXPIRY_SEC:-3600}"
RGB_DURATION_SECONDS="${RGB_DURATION_SECONDS:-3600}"
RGB_MIN_CONFIRMATIONS="${RGB_MIN_CONFIRMATIONS:-1}"
RGB_WITNESS="${RGB_WITNESS:-false}"
SENDRGB_FEE_RATE="${SENDRGB_FEE_RATE:-1}"
RGB_PAY_AMOUNT="${RGB_PAY_AMOUNT:-1}"

# Auto-pay toggles for test automation.
AUTO_PAY_LN="${AUTO_PAY_LN:-false}"
AUTO_PAY_RGB="${AUTO_PAY_RGB:-false}"

# Optional peer bootstrap.
PEER_PUBKEY_AND_ADDR="${PEER_PUBKEY_AND_ADDR:-}"
SECOND_NODE_P2P_ADDR="${SECOND_NODE_P2P_ADDR:-127.0.0.1:9736}"
AUTH_CHECK_PATH="${AUTH_CHECK_PATH:-/nodeinfo}"
SDK_SMOKE_VIRTUAL_OPEN_MODE="${SDK_SMOKE_VIRTUAL_OPEN_MODE:-}"
SDK_SMOKE_CHANNEL_CAPACITY_SAT="${SDK_SMOKE_CHANNEL_CAPACITY_SAT:-200000}"
SDK_SMOKE_CHANNEL_TIMEOUT="${SDK_SMOKE_CHANNEL_TIMEOUT:-90}"
SDK_SMOKE_CHANNEL_INTERVAL="${SDK_SMOKE_CHANNEL_INTERVAL:-5}"

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1"; exit 1; }
}

node_curl() {
  local method="$1"; shift
  local path="$1"; shift
  local data="${1:-}"
  local auth=()
  local tmp http_code curl_rc
  if [[ -n "$NODE_TOKEN" ]]; then
    auth=(-H "Authorization: Bearer $NODE_TOKEN")
  fi
  tmp="$(mktemp)"
  if [[ -n "$data" ]]; then
    set +e
    http_code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$NODE_BASE_URL$path" "${auth[@]}" -H 'content-type: application/json' -d "$data")"
    curl_rc=$?
    set -e
  else
    set +e
    http_code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$NODE_BASE_URL$path" "${auth[@]}")"
    curl_rc=$?
    set -e
  fi
  cat "$tmp" 2>/dev/null || true
  rm -f "$tmp"
  if [[ "$curl_rc" -ne 0 ]]; then
    return "$curl_rc"
  fi
  if [[ "$http_code" -ge 400 ]]; then
    return 22
  fi
}

second_node_curl() {
  local method="$1"; shift
  local path="$1"; shift
  local data="${1:-}"
  local auth=()
  local tmp http_code curl_rc
  if [[ -n "$SECOND_NODE_TOKEN" ]]; then
    auth=(-H "Authorization: Bearer $SECOND_NODE_TOKEN")
  fi
  tmp="$(mktemp)"
  if [[ -n "$data" ]]; then
    set +e
    http_code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$SECOND_NODE_BASE_URL$path" "${auth[@]}" -H 'content-type: application/json' -d "$data")"
    curl_rc=$?
    set -e
  else
    set +e
    http_code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$SECOND_NODE_BASE_URL$path" "${auth[@]}")"
    curl_rc=$?
    set -e
  fi
  cat "$tmp" 2>/dev/null || true
  rm -f "$tmp"
  if [[ "$curl_rc" -ne 0 ]]; then
    return "$curl_rc"
  fi
  if [[ "$http_code" -ge 400 ]]; then
    return 22
  fi
}

api_curl() {
  local method="$1"; shift
  local path="$1"; shift
  local data="${1:-}"
  local tmp http_code curl_rc
  tmp="$(mktemp)"
  if [[ -n "$data" ]]; then
    set +e
    http_code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$API_BASE_URL$path" -H 'content-type: application/json' -d "$data")"
    curl_rc=$?
    set -e
  else
    set +e
    http_code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$API_BASE_URL$path")"
    curl_rc=$?
    set -e
  fi
  cat "$tmp" 2>/dev/null || true
  rm -f "$tmp"
  if [[ "$curl_rc" -ne 0 ]]; then
    return "$curl_rc"
  fi
  if [[ "$http_code" -ge 400 ]]; then
    return 22
  fi
}

extract_json_field() {
  local json="$1"
  local jq_expr="$2"
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$json" | jq -r "$jq_expr"
  else
    # Minimal fallback for string fields only.
    local field
    field="$(printf '%s' "$jq_expr" | sed 's/^\.//')"
    printf '%s' "$json" | sed -n "s/.*\"$field\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
  fi
}

pay_ln_invoice() {
  local invoice="$1"
  if [[ -z "$invoice" ]]; then
    echo "pay_ln_invoice: missing invoice"
    return 1
  fi
  echo "== Paying LN invoice via /sendpayment =="
  node_curl POST /sendpayment "{\"invoice\":\"$invoice\"}"
  echo
}

pay_rgb_invoice() {
  local invoice="$1"
  if [[ -z "$invoice" ]]; then
    echo "pay_rgb_invoice: missing invoice"
    return 1
  fi
  need_cmd jq

  echo "== Decoding RGB invoice =="
  local dec
  dec="$(node_curl POST /decodergbinvoice "{\"invoice\":\"$invoice\"}")"
  echo "$dec"
  echo

  local asset_id recipient_id
  asset_id="$(printf '%s' "$dec" | jq -r '.asset_id // empty')"
  recipient_id="$(printf '%s' "$dec" | jq -r '.recipient_id // empty')"
  if [[ -z "$asset_id" || -z "$recipient_id" ]]; then
    echo "pay_rgb_invoice: could not extract asset_id/recipient_id from decoded invoice"
    return 1
  fi

  if [[ "$(printf '%s' "$dec" | jq -r '.recipient_type')" == "Witness" ]]; then
    echo "pay_rgb_invoice: witness invoices require an explicit witness funding policy"
    return 1
  fi
  local amount
  amount="$(printf '%s' "$dec" | jq -er --argjson fallback "$RGB_PAY_AMOUNT" '
    .assignment |
    if .type == "Any" or (.type == "Fungible" and .value == 0) then $fallback
    elif .type == "Fungible" then .value
    else error("expected fungible invoice") end |
    select(. > 0)
  ')" || return 1

  echo "== Paying RGB invoice via /sendrgbinvoice =="
  local payload
  payload="$(jq -n \
    --arg asset_id "$asset_id" \
    --arg invoice "$invoice" \
    --argjson amount "$amount" \
    --argjson fee_rate "$SENDRGB_FEE_RATE" \
    --argjson min_conf "$RGB_MIN_CONFIRMATIONS" \
    '{
      invoice: $invoice,
      asset_id: $asset_id,
      amount: $amount,
      donation: false,
      fee_rate: $fee_rate,
      min_confirmations: $min_conf
    }')"
  node_curl POST /sendrgbinvoice "$payload"
  echo
}

preflight() {
  need_cmd curl
  need_cmd sqlite3

  echo "== API health =="
  api_curl GET /health
  echo

  echo "== Node listpeers =="
  node_curl GET /listpeers
  echo

  echo "== Node listchannels =="
  node_curl GET /listchannels
  echo
}

node_initial() {
  echo "== Node initial requests =="
  echo "-- /networkinfo"
  node_curl GET /networkinfo || true
  echo
  echo "-- /nodeinfo"
  node_curl GET /nodeinfo || true
  echo
  echo "-- /listpeers"
  node_curl GET /listpeers
  echo
  echo "-- /listchannels"
  node_curl GET /listchannels
  echo

  if [[ -n "$PEER_PUBKEY_AND_ADDR" ]]; then
    echo "-- /connectpeer"
    node_curl POST /connectpeer "{\"peer_pubkey_and_addr\":\"$PEER_PUBKEY_AND_ADDR\"}"
    echo
  fi
}

node_init() {
  echo "== Node init =="
  local payload
  if [[ -n "$NODE_MNEMONIC" ]]; then
    payload="{\"password\":\"$NODE_PASSWORD\",\"mnemonic\":\"$NODE_MNEMONIC\"}"
  else
    payload="{\"password\":\"$NODE_PASSWORD\"}"
  fi
  node_curl POST /init "$payload"
  echo
}

node_unlock() {
  echo "== Node unlock =="
  local payload
  payload=$(cat <<JSON
{
  "password":"$NODE_PASSWORD",
  "ldk_chain_sync": {
    "mode": "BlockSync",
    "config": {
      "bitcoind_rpc_username":"$BITCOIND_RPC_USERNAME",
      "bitcoind_rpc_password":"$BITCOIND_RPC_PASSWORD",
      "bitcoind_rpc_host":"$BITCOIND_RPC_HOST",
      "bitcoind_rpc_port":$BITCOIND_RPC_PORT
    }
  },
  "indexer_url":"$INDEXER_URL",
  "proxy_endpoint":"$PROXY_ENDPOINT",
  "announce_addresses":$ANNOUNCE_ADDRESSES,
  "announce_alias":$ANNOUNCE_ALIAS
}
JSON
)
  node_curl POST /unlock "$payload"
  echo
}

tolerant_unlock() {
  set +e
  local resp
  resp="$(node_unlock 2>&1)"
  local rc=$?
  set -e
  if [[ $rc -eq 0 ]]; then
    return 0
  fi
  if printf '%s' "$resp" | grep -qiE "already.*unlocked|AlreadyUnlocked|UnlockedNode"; then
    echo "Node already unlocked, continuing."
    return 0
  fi
  printf '%s\n' "$resp"
  return $rc
}

lightning_receive() {
  if [[ -z "$ASSET_ID" || -z "$USER_LN_INVOICE" ]]; then
    echo "ASSET_ID and USER_LN_INVOICE are required for lightning-receive"
    exit 1
  fi

  local payload
  payload=$(cat <<JSON
{
  "ln_invoice": "$USER_LN_INVOICE",
  "rgb_invoice": {
    "asset_id": "$ASSET_ID",
    "duration_seconds": $RGB_DURATION_SECONDS,
    "min_confirmations": $RGB_MIN_CONFIRMATIONS,
    "witness": $RGB_WITNESS
  }
}
JSON
)

  echo "== POST /lightning_receive =="
  local resp
  resp="$(api_curl POST /lightning_receive "$payload")"
  echo "$resp"
  echo

  local mapping
  if command -v jq >/dev/null 2>&1; then
    mapping="$(printf '%s' "$resp" | jq -r '.mapping_id // empty')"
  else
    mapping="$(printf '%s' "$resp" | sed -n 's/.*"mapping_id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')"
  fi
  if [[ -n "$mapping" ]]; then
    echo "lightning_receive mapping_id=$mapping"
  fi

  if [[ "$AUTO_PAY_RGB" == "true" ]]; then
    local rgb_invoice
    rgb_invoice="$(extract_json_field "$resp" '.rgb_invoice')"
    if [[ -n "$rgb_invoice" && "$rgb_invoice" != "null" ]]; then
      pay_rgb_invoice "$rgb_invoice"
    else
      echo "AUTO_PAY_RGB enabled but rgb_invoice missing in response"
    fi
  fi
}

onchain_send() {
  if [[ -z "$USER_RGB_INVOICE" ]]; then
    echo "USER_RGB_INVOICE is required for onchain-send"
    exit 1
  fi

  local payload
  payload=$(cat <<JSON
{
  "rgb_invoice": "$USER_RGB_INVOICE",
  "lninvoice": {
    "amt_msat": $LN_AMT_MSAT,
    "expiry_sec": $LN_EXPIRY_SEC
  }
}
JSON
)

  echo "== POST /onchain_send =="
  local resp
  resp="$(api_curl POST /onchain_send "$payload")"
  echo "$resp"
  echo

  if command -v jq >/dev/null 2>&1; then
    local ln_invoice
    ln_invoice="$(printf '%s' "$resp" | jq -r '.ln_invoice // empty')"
    if [[ -n "$ln_invoice" ]]; then
      echo "Pay this LN invoice to progress flow:"
      echo "$ln_invoice"
      if [[ "$AUTO_PAY_LN" == "true" ]]; then
        pay_ln_invoice "$ln_invoice"
      fi
    fi
  fi
}

monitor() {
  echo "Sleeping ${WAIT_SECONDS}s for cron ticks..."
  sleep "$WAIT_SECONDS"

  echo "== DB onchain_send_mappings =="
  sqlite3 "$DB_PATH" "select id,status,datetime(created_at),coalesce(last_error,'') from onchain_send_mappings order by id desc limit 10;"
  echo

  echo "== DB lightning_receive_mappings =="
  sqlite3 "$DB_PATH" "select id,status,rgb_asset_id,batch_transfer_idx,datetime(created_at),coalesce(last_error,'') from lightning_receive_mappings order by id desc limit 10;"
  echo

  if [[ -n "$ASSET_ID" ]]; then
    echo "== Node refreshtransfers + listtransfers(asset_id=$ASSET_ID) =="
    node_curl POST /refreshtransfers '{"skip_sync":false}'
    echo
    node_curl POST /listtransfers "{\"asset_id\":\"$ASSET_ID\"}"
    echo
  fi
}

all() {
  tolerant_unlock
  preflight
  node_initial
  if [[ -n "$USER_LN_INVOICE" && -n "$ASSET_ID" ]]; then
    lightning_receive
  fi
  if [[ -n "$USER_RGB_INVOICE" ]]; then
    onchain_send
  fi
  monitor
}

auth_check() {
  need_cmd curl

  echo "== Auth check =="
  echo "Node API: $NODE_BASE_URL"
  echo "Path: $AUTH_CHECK_PATH"

  local tmp_noauth noauth_code tmp_auth auth_code
  tmp_noauth="$(mktemp)"
  set +e
  noauth_code="$(curl -sS -o "$tmp_noauth" -w '%{http_code}' "$NODE_BASE_URL$AUTH_CHECK_PATH")"
  local noauth_rc=$?
  set -e
  if [[ "$noauth_rc" -ne 0 ]]; then
    echo "request without token failed at transport level"
    cat "$tmp_noauth" 2>/dev/null || true
    rm -f "$tmp_noauth"
    exit 1
  fi

  echo "-- without token: HTTP $noauth_code"
  cat "$tmp_noauth" 2>/dev/null || true
  echo

  if [[ -z "$NODE_TOKEN" ]]; then
    rm -f "$tmp_noauth"
    if [[ "$noauth_code" == "401" || "$noauth_code" == "403" ]]; then
      echo "Auth is enforced. Set NODE_TOKEN and rerun to validate authorized request."
      return 0
    fi
    echo "Auth is not enforced on this node (or endpoint is public)."
    return 1
  fi

  tmp_auth="$(mktemp)"
  set +e
  auth_code="$(curl -sS -H "Authorization: Bearer $NODE_TOKEN" -o "$tmp_auth" -w '%{http_code}' "$NODE_BASE_URL$AUTH_CHECK_PATH")"
  local auth_rc=$?
  set -e
  if [[ "$auth_rc" -ne 0 ]]; then
    echo "request with token failed at transport level"
    cat "$tmp_auth" 2>/dev/null || true
    rm -f "$tmp_noauth" "$tmp_auth"
    exit 1
  fi

  echo "-- with token: HTTP $auth_code"
  cat "$tmp_auth" 2>/dev/null || true
  echo

  rm -f "$tmp_noauth" "$tmp_auth"

  if [[ ("$noauth_code" == "401" || "$noauth_code" == "403") && "$auth_code" == "200" ]]; then
    echo "SUCCESS: auth enforcement and token access both verified."
    return 0
  fi

  echo "FAILED: expected no-token 401/403 and token 200."
  exit 1
}

two_nodes_openchannel_verify() {
  need_cmd jq
  need_cmd curl

  echo "== Two-node openchannel verification =="
  echo "Node A API: $NODE_BASE_URL"
  echo "Node B API: $SECOND_NODE_BASE_URL"
  echo

  echo "-- node A /nodeinfo"
  local node_a_info node_a_pubkey
  node_a_info="$(node_curl GET /nodeinfo)"
  echo "$node_a_info"
  node_a_pubkey="$(printf '%s' "$node_a_info" | jq -r '.pubkey // empty')"
  echo

  echo "-- node B /nodeinfo"
  local node_b_info node_b_pubkey
  node_b_info="$(second_node_curl GET /nodeinfo)"
  echo "$node_b_info"
  node_b_pubkey="$(printf '%s' "$node_b_info" | jq -r '.pubkey // empty')"
  echo

  if [[ -z "$node_a_pubkey" || -z "$node_b_pubkey" ]]; then
    echo "failed to parse node pubkeys from /nodeinfo"
    exit 1
  fi

  local peer_target="${node_b_pubkey}@${SECOND_NODE_P2P_ADDR}"
  echo "-- connect node A to node B: $peer_target"
  node_curl POST /connectpeer "{\"peer_pubkey_and_addr\":\"$peer_target\"}" || true
  echo

  echo "-- waiting for PoC cron to run reconcileChannels/openchannel"
  local deadline
  deadline=$((SECONDS + OPENCHANNEL_VERIFY_TIMEOUT))
  local opened=0
  while (( SECONDS < deadline )); do
    local channels_a
    channels_a="$(node_curl GET /listchannels || true)"
    if printf '%s' "$channels_a" | jq -e --arg p "$node_b_pubkey" '.channels // [] | any(.peer_pubkey == $p)' >/dev/null 2>&1; then
      opened=1
      break
    fi
    sleep "$OPENCHANNEL_VERIFY_INTERVAL"
  done

  echo "-- node A /listchannels"
  local final_channels_a final_channels_b
  final_channels_a="$(node_curl GET /listchannels || true)"
  echo "$final_channels_a"
  echo
  echo "-- node B /listchannels"
  final_channels_b="$(second_node_curl GET /listchannels || true)"
  echo "$final_channels_b"
  echo

  if [[ -n "$EXPECT_VIRTUAL_OPEN_MODE" ]]; then
    local detected_mode
    detected_mode="$(printf '%s' "$final_channels_a" | jq -r --arg p "$node_b_pubkey" '.channels // [] | map(select(.peer_pubkey == $p)) | .[0].virtual_open_mode // empty')"
    if [[ -z "$detected_mode" ]]; then
      echo "FAILED: expected virtual_open_mode=$EXPECT_VIRTUAL_OPEN_MODE but channel has no virtual_open_mode"
      exit 1
    fi
    if [[ "$detected_mode" != "$EXPECT_VIRTUAL_OPEN_MODE" ]]; then
      echo "FAILED: expected virtual_open_mode=$EXPECT_VIRTUAL_OPEN_MODE, got $detected_mode"
      exit 1
    fi
    echo "virtual_open_mode verified: $detected_mode"
  fi

  if [[ "$opened" -eq 1 ]]; then
    echo "SUCCESS: channel opened on node A towards node B"
  else
    echo "FAILED: channel was not detected within ${OPENCHANNEL_VERIFY_TIMEOUT}s"
    echo "Check: PoC process is running, node unlocked, funds available, and peer connectivity."
    exit 1
  fi
}

sdk_client_smoke() {
  need_cmd jq
  need_cmd curl

  if [[ -z "$SERVER_ASSET_ID" ]]; then
    echo "SERVER_ASSET_ID (or ASSET_ID) is required for sdk-client-smoke"
    exit 1
  fi
  if [[ -z "$CLIENT_ASSET_ID" ]]; then
    echo "CLIENT_ASSET_ID (or ASSET_ID) is required for sdk-client-smoke"
    exit 1
  fi

  echo "== SDK client smoke (LSP server node + client node) =="
  echo "PoC API: $API_BASE_URL"
  echo "Server node (A): $NODE_BASE_URL"
  echo "Client node (B): $SECOND_NODE_BASE_URL"
  echo

  echo "-- check node identities"
  local node_a_info node_b_info node_a_pubkey node_b_pubkey
  node_a_info="$(node_curl GET /nodeinfo)"
  node_b_info="$(second_node_curl GET /nodeinfo)"
  node_a_pubkey="$(printf '%s' "$node_a_info" | jq -r '.pubkey // empty')"
  node_b_pubkey="$(printf '%s' "$node_b_info" | jq -r '.pubkey // empty')"
  echo "node_a_pubkey=$node_a_pubkey"
  echo "node_b_pubkey=$node_b_pubkey"
  if [[ -z "$node_a_pubkey" || -z "$node_b_pubkey" ]]; then
    echo "failed to read pubkeys; check both nodes are running and unlocked"
    exit 1
  fi
  if [[ "$node_a_pubkey" == "$node_b_pubkey" ]]; then
    echo "node A and node B pubkeys are identical; use different datadirs"
    exit 1
  fi
  echo

  if [[ -n "$SDK_SMOKE_VIRTUAL_OPEN_MODE" ]]; then
    echo "-- ensure virtual channel node A -> node B (mode=$SDK_SMOKE_VIRTUAL_OPEN_MODE)"
    node_curl POST /connectpeer "{\"peer_pubkey_and_addr\":\"${node_b_pubkey}@${SECOND_NODE_P2P_ADDR}\"}" || true

    local channels_a existing_mode
    channels_a="$(node_curl GET /listchannels || true)"
    existing_mode="$(printf '%s' "$channels_a" | jq -r --arg p "$node_b_pubkey" '.channels // [] | map(select(.peer_pubkey == $p)) | .[0].virtual_open_mode // empty')"
    if [[ "$existing_mode" == "$SDK_SMOKE_VIRTUAL_OPEN_MODE" ]]; then
      echo "virtual channel already present with mode=$existing_mode"
    else
      local open_payload
      open_payload="$(jq -n \
        --arg peer "${node_b_pubkey}@${SECOND_NODE_P2P_ADDR}" \
        --arg mode "$SDK_SMOKE_VIRTUAL_OPEN_MODE" \
        --argjson cap "$SDK_SMOKE_CHANNEL_CAPACITY_SAT" \
        '{
          peer_pubkey_and_opt_addr:$peer,
          capacity_sat:$cap,
          push_msat:0,
          asset_amount:null,
          asset_id:null,
          push_asset_amount:null,
          public:false,
          with_anchors:true,
          fee_base_msat:null,
          fee_proportional_millionths:null,
          temporary_channel_id:null,
          virtual_open_mode:$mode
        }')"
      node_curl POST /openchannel "$open_payload" || true
    fi

    local deadline
    deadline=$((SECONDS + SDK_SMOKE_CHANNEL_TIMEOUT))
    local mode_detected=""
    while (( SECONDS < deadline )); do
      channels_a="$(node_curl GET /listchannels || true)"
      mode_detected="$(printf '%s' "$channels_a" | jq -r --arg p "$node_b_pubkey" '.channels // [] | map(select(.peer_pubkey == $p)) | .[0].virtual_open_mode // empty')"
      if [[ "$mode_detected" == "$SDK_SMOKE_VIRTUAL_OPEN_MODE" ]]; then
        break
      fi
      sleep "$SDK_SMOKE_CHANNEL_INTERVAL"
    done
    if [[ "$mode_detected" != "$SDK_SMOKE_VIRTUAL_OPEN_MODE" ]]; then
      echo "failed to confirm virtual_open_mode=$SDK_SMOKE_VIRTUAL_OPEN_MODE on node A channel"
      node_curl GET /listchannels || true
      exit 1
    fi
    echo "virtual channel mode confirmed: $mode_detected"
    echo
  fi

  echo "-- create LN invoice on client node (B)"
  local ln_resp client_ln_invoice
  ln_resp="$(second_node_curl POST /lninvoice "{\"amt_msat\":$CLIENT_LN_AMT_MSAT,\"expiry_sec\":$CLIENT_LN_EXPIRY_SEC}")"
  echo "$ln_resp"
  client_ln_invoice="$(printf '%s' "$ln_resp" | jq -r '.invoice // empty')"
  if [[ -z "$client_ln_invoice" ]]; then
    echo "failed to create client LN invoice"
    exit 1
  fi
  echo

  echo "-- call PoC /lightning_receive with client LN invoice"
  local lr_payload lr_resp
  lr_payload="$(jq -n \
    --arg inv "$client_ln_invoice" \
    --arg asset "$SERVER_ASSET_ID" \
    --argjson dur "$RGB_DURATION_SECONDS" \
    --argjson minc "$RGB_MIN_CONFIRMATIONS" \
    --argjson wit "$RGB_WITNESS" \
    '{ln_invoice:$inv,rgb_invoice:{asset_id:$asset,duration_seconds:$dur,min_confirmations:$minc,witness:$wit}}')"
  lr_resp="$(api_curl POST /lightning_receive "$lr_payload")"
  echo "$lr_resp"
  echo

  echo "-- create RGB invoice on client node (B)"
  local rgb_req rgb_resp client_rgb_invoice
  rgb_req="$(jq -n \
    --arg asset "$CLIENT_ASSET_ID" \
    --argjson dur "$RGB_DURATION_SECONDS" \
    --argjson minc "$RGB_MIN_CONFIRMATIONS" \
    --argjson wit "$RGB_WITNESS" \
    --argjson amount "$RGB_PAY_AMOUNT" \
    --arg proxy "$PROXY_ENDPOINT" \
    '{asset_id:$asset,assignment:{type:"Fungible",value:$amount},expiration_timestamp:((now|floor)+$dur),transport_endpoints:[$proxy],min_confirmations:$minc,witness:$wit}')"
  rgb_resp="$(second_node_curl POST /rgbinvoice "$rgb_req")"
  echo "$rgb_resp"
  client_rgb_invoice="$(printf '%s' "$rgb_resp" | jq -r '.invoice // empty')"
  if [[ -z "$client_rgb_invoice" ]]; then
    echo "failed to create client RGB invoice"
    exit 1
  fi
  echo

  echo "-- call PoC /onchain_send with client RGB invoice"
  local os_payload os_resp
  os_payload="$(jq -n \
    --arg inv "$client_rgb_invoice" \
    --argjson amt "$LN_AMT_MSAT" \
    --argjson exp "$LN_EXPIRY_SEC" \
    '{rgb_invoice:$inv,lninvoice:{amt_msat:$amt,expiry_sec:$exp}}')"
  os_resp="$(api_curl POST /onchain_send "$os_payload")"
  echo "$os_resp"
  echo

  echo "SDK client smoke completed."
}

usage() {
  cat <<USAGE
Usage: $(basename "$0") <command>

Commands:
  preflight          Check API health and basic node endpoints.
  node-init          Call node /init (optional one-time setup).
  node-unlock        Call node /unlock.
  node-initial       Run initial node requests (and optional connectpeer).
  lightning-receive  Call PoC /lightning_receive.
  pay-ln            Pay LN invoice via node /sendpayment (requires LN_INVOICE env).
  pay-rgb           Pay RGB invoice via node /sendrgbinvoice (requires RGB_INVOICE env).
  onchain-send       Call PoC /onchain_send.
  monitor            Wait and inspect DB + transfer status.
  auth-check         Verify RLN auth behavior (no token vs token).
  two-nodes-openchannel-verify
                     Verify PoC cron opens channel from node A to node B.
  sdk-client-smoke   Use second node as SDK-like client for PoC endpoint smoke test.
  all                Run full sequence.

Environment variables:
  API_BASE_URL, NODE_BASE_URL, NODE_TOKEN, DB_PATH, WAIT_SECONDS
  SECOND_NODE_BASE_URL, SECOND_NODE_TOKEN, SECOND_NODE_P2P_ADDR
  OPENCHANNEL_VERIFY_TIMEOUT, OPENCHANNEL_VERIFY_INTERVAL
  EXPECT_VIRTUAL_OPEN_MODE
  SDK_SMOKE_VIRTUAL_OPEN_MODE, SDK_SMOKE_CHANNEL_CAPACITY_SAT
  SDK_SMOKE_CHANNEL_TIMEOUT, SDK_SMOKE_CHANNEL_INTERVAL
  AUTH_CHECK_PATH
  NODE_PASSWORD, NODE_MNEMONIC
  BITCOIND_RPC_USERNAME, BITCOIND_RPC_PASSWORD, BITCOIND_RPC_HOST, BITCOIND_RPC_PORT
  INDEXER_URL, PROXY_ENDPOINT, ANNOUNCE_ADDRESSES, ANNOUNCE_ALIAS
  ASSET_ID, USER_LN_INVOICE, USER_RGB_INVOICE
  SERVER_ASSET_ID, CLIENT_ASSET_ID
  LN_INVOICE, RGB_INVOICE
  LN_AMT_MSAT, LN_EXPIRY_SEC
  CLIENT_LN_AMT_MSAT, CLIENT_LN_EXPIRY_SEC
  RGB_DURATION_SECONDS, RGB_MIN_CONFIRMATIONS, RGB_WITNESS
  SENDRGB_FEE_RATE
  RGB_PAY_AMOUNT
  AUTO_PAY_LN, AUTO_PAY_RGB
  PEER_PUBKEY_AND_ADDR
USAGE
}

main() {
  local cmd="${1:-}"
  case "$cmd" in
    preflight) preflight ;;
    node-init) node_init ;;
    node-unlock) node_unlock ;;
    node-initial) node_initial ;;
    lightning-receive) lightning_receive ;;
    pay-ln)
      if [[ -z "${LN_INVOICE:-}" ]]; then
        echo "LN_INVOICE is required"
        exit 1
      fi
      pay_ln_invoice "$LN_INVOICE"
      ;;
    pay-rgb)
      if [[ -z "${RGB_INVOICE:-}" ]]; then
        echo "RGB_INVOICE is required"
        exit 1
      fi
      pay_rgb_invoice "$RGB_INVOICE"
      ;;
    onchain-send) onchain_send ;;
    monitor) monitor ;;
    auth-check) auth_check ;;
    two-nodes-openchannel-verify) two_nodes_openchannel_verify ;;
    sdk-client-smoke) sdk_client_smoke ;;
    all) all ;;
    *) usage; exit 1 ;;
  esac
}

main "$@"
