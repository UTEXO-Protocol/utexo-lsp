#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RLN_DIR="${RLN_DIR:-$PROJECT_ROOT/rgb-lightning-node}"

NODE_A_URL="${NODE_A_URL:-http://127.0.0.1:3001}"
NODE_B_URL="${NODE_B_URL:-http://127.0.0.1:3002}"
LSP_URL="${LSP_URL:-http://127.0.0.1:8080}"
NODE_A_P2P_PORT="${NODE_A_P2P_PORT:-9735}"
NODE_B_P2P_PORT="${NODE_B_P2P_PORT:-9736}"
NODE_A_CONTAINER="${NODE_A_CONTAINER:-rln-node-a}"
NODE_B_CONTAINER="${NODE_B_CONTAINER:-rln-node-b}"
RLN_IMAGE="${RLN_IMAGE:-utexo/rgb-lightning-node:e2e}"
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-rlnreg}"

NODE_PASSWORD="${NODE_PASSWORD:-password123}"
BITCOIND_RPC_USERNAME="${BITCOIND_RPC_USERNAME:-user}"
BITCOIND_RPC_PASSWORD="${BITCOIND_RPC_PASSWORD:-password}"
BITCOIND_RPC_HOST="${BITCOIND_RPC_HOST:-bitcoind}"
BITCOIND_RPC_PORT="${BITCOIND_RPC_PORT:-18443}"
INDEXER_URL="${INDEXER_URL:-electrs:50001}"
PROXY_ENDPOINT="${PROXY_ENDPOINT:-rpc://proxy:3000/json-rpc}"

WAIT_RETRIES="${WAIT_RETRIES:-120}"
WAIT_SECONDS="${WAIT_SECONDS:-1}"

LSP_LOG="${LSP_LOG:-$PROJECT_ROOT/.tmp-e2e-lsp.log}"

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1"; exit 1; }
}

wait_http() {
  local url="$1"
  local attempt=1
  while (( attempt <= WAIT_RETRIES )); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$WAIT_SECONDS"
    attempt=$((attempt + 1))
  done
  echo "timeout waiting for $url"
  return 1
}

node_json_field() {
  local url="$1"
  local path="$2"
  local field="$3"
  curl -fsS "$url$path" | jq -r "$field"
}

cleanup() {
  set +e

  if [[ -n "${LSP_PID:-}" ]]; then
    kill "$LSP_PID" >/dev/null 2>&1 || true
    wait "$LSP_PID" >/dev/null 2>&1 || true
  fi

  docker rm -f "$NODE_A_CONTAINER" "$NODE_B_CONTAINER" >/dev/null 2>&1 || true

  if [[ -d "$RLN_DIR" ]]; then
    (
      cd "$RLN_DIR"
      COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" ./regtest.sh stop
    ) >/dev/null 2>&1 || true
  fi
}

start_rlnd_container() {
  local name="$1"
  local api_port="$2"
  local p2p_port="$3"
  local volume_name="$4"

  docker rm -f "$name" >/dev/null 2>&1 || true
  docker volume rm "$volume_name" >/dev/null 2>&1 || true

  docker run -d \
    --name "$name" \
    --network "${COMPOSE_PROJECT_NAME}_default" \
    -p "$api_port:$api_port" \
    -p "$p2p_port:$p2p_port" \
    -v "$volume_name:/RLNdata" \
    "$RLN_IMAGE" \
      --daemon-listening-port "$api_port" \
      --ldk-peer-listening-port "$p2p_port" \
      --network regtest \
      --disable-authentication \
      RLNdata >/dev/null
}

main() {
  need_cmd docker
  need_cmd curl
  need_cmd jq
  need_cmd go

  [[ -d "$RLN_DIR" ]] || { echo "RLN_DIR does not exist: $RLN_DIR"; exit 1; }

  trap cleanup EXIT

  echo "== starting RLN regtest services via canonical script =="
  (
    cd "$RLN_DIR"
    COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" ./regtest.sh start
  )

  echo "== building RLN runtime image =="
  docker build -t "$RLN_IMAGE" "$RLN_DIR"

  echo "== starting RLN node containers =="
  start_rlnd_container "$NODE_A_CONTAINER" 3001 "$NODE_A_P2P_PORT" rln-node-a-data
  start_rlnd_container "$NODE_B_CONTAINER" 3002 "$NODE_B_P2P_PORT" rln-node-b-data

  echo "== waiting for RLN APIs =="
  wait_http "$NODE_A_URL/health"
  wait_http "$NODE_B_URL/health"

  echo "== init/unlock RLN node A =="
  NODE_BASE_URL="$NODE_A_URL" NODE_PASSWORD="$NODE_PASSWORD" "$PROJECT_ROOT/scripts/poc_flow.sh" node-init
  NODE_BASE_URL="$NODE_A_URL" \
  NODE_PASSWORD="$NODE_PASSWORD" \
  BITCOIND_RPC_USERNAME="$BITCOIND_RPC_USERNAME" \
  BITCOIND_RPC_PASSWORD="$BITCOIND_RPC_PASSWORD" \
  BITCOIND_RPC_HOST="$BITCOIND_RPC_HOST" \
  BITCOIND_RPC_PORT="$BITCOIND_RPC_PORT" \
  INDEXER_URL="$INDEXER_URL" \
  PROXY_ENDPOINT="$PROXY_ENDPOINT" \
  "$PROJECT_ROOT/scripts/poc_flow.sh" node-unlock

  echo "== init/unlock RLN node B =="
  NODE_BASE_URL="$NODE_B_URL" NODE_PASSWORD="$NODE_PASSWORD" "$PROJECT_ROOT/scripts/poc_flow.sh" node-init
  NODE_BASE_URL="$NODE_B_URL" \
  NODE_PASSWORD="$NODE_PASSWORD" \
  BITCOIND_RPC_USERNAME="$BITCOIND_RPC_USERNAME" \
  BITCOIND_RPC_PASSWORD="$BITCOIND_RPC_PASSWORD" \
  BITCOIND_RPC_HOST="$BITCOIND_RPC_HOST" \
  BITCOIND_RPC_PORT="$BITCOIND_RPC_PORT" \
  INDEXER_URL="$INDEXER_URL" \
  PROXY_ENDPOINT="$PROXY_ENDPOINT" \
  "$PROJECT_ROOT/scripts/poc_flow.sh" node-unlock

  echo "== funding node A wallet and mining confirmations =="
  local node_a_address
  node_a_address="$(node_json_field "$NODE_A_URL" /address '.address // empty')"
  [[ -n "$node_a_address" ]] || { echo "failed to get node A address"; exit 1; }
  (
    cd "$RLN_DIR"
    COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" ./regtest.sh sendtoaddress "$node_a_address" 1
    COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" ./regtest.sh mine 6
  )

  echo "== starting utexo-lsp service =="
  rm -f "$PROJECT_ROOT/utexo_lsp_e2e.db"
  (
    cd "$PROJECT_ROOT"
    SERVER_ADDR=":8080" \
    LSP_BASE_URL="$NODE_A_URL" \
    RGB_NODE_BASE_URL="$NODE_A_URL" \
    DATABASE_URL="$PROJECT_ROOT/utexo_lsp_e2e.db" \
    CRON_EVERY="5s" \
    go run . >"$LSP_LOG" 2>&1
  ) &
  LSP_PID=$!

  wait_http "$LSP_URL/health"

  echo "== running smoke checks through poc_flow =="
  NODE_BASE_URL="$NODE_A_URL" "$PROJECT_ROOT/scripts/poc_flow.sh" preflight
  NODE_BASE_URL="$NODE_A_URL" \
  SECOND_NODE_BASE_URL="$NODE_B_URL" \
  SECOND_NODE_P2P_ADDR="${NODE_B_CONTAINER}:${NODE_B_P2P_PORT}" \
  OPENCHANNEL_VERIFY_TIMEOUT=180 \
  OPENCHANNEL_VERIFY_INTERVAL=5 \
  "$PROJECT_ROOT/scripts/poc_flow.sh" two-nodes-openchannel-verify

  echo "== e2e completed successfully =="
}

main "$@"
