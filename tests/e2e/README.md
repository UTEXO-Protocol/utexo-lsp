# E2E Tests

These tests cover `flow 0`.

Current harness shape:
- LSP RLN and Faucet run as dockerized daemon processes
- User A and User B run via the Python UniFFI `SdkNode` path
- regtest services (`bitcoind`, `electrs`, `proxy`) must already be running before `pytest`

Current status:
- `test_flow0_full_e2e.py` covers flow 0 end-to-end

## Prerequisites

- Docker with Compose v2
- user in `docker` group (`sudo` must not be required during test runs)
- Go toolchain (used by `go run .` for `utexo-lsp`)
- `rgb-lightning-node` checkout available via `RGBLN_REPO`
- Python UniFFI bindings for `rgb-lightning-node`

If `rgb-lightning-node` is a git submodule, point `RGBLN_REPO` to the submodule path.

## Build Python SDK bindings

```bash
cd /path/to/rgb-lightning-node
cargo build --release --locked --features uniffi --lib
./scripts/ci/uniffi_generate_python.sh

export RGBLN_REPO=/path/to/rgb-lightning-node
export PYTHONPATH="$RGBLN_REPO/target/uniffi/python:${PYTHONPATH:-}"
export LD_LIBRARY_PATH="$RGBLN_REPO/target/release:${LD_LIBRARY_PATH:-}"
```

The Python SDK is backed by the native Rust library, so both `PYTHONPATH` and
`LD_LIBRARY_PATH` are required.

## Prepare the daemon image explicitly

Build from the pinned submodule. Set `RGBLN_DOCKER_IMAGE` to the image name and
tag chosen for your environment (default: `rgb-lightning-node:e2e`). CI accepts
the same name through the `RGBLN_DOCKER_IMAGE` repository variable. A normal test
run only checks that the image exists; it never builds, pulls, or updates RLN.

```bash
export RGBLN_DOCKER_IMAGE="${RGBLN_DOCKER_IMAGE:-rgb-lightning-node:e2e}"
docker build -t "$RGBLN_DOCKER_IMAGE" "$RGBLN_REPO"
```

Native iOS compatibility still requires a separate APay Bridge acceptance run:
open real LNUSDT merchant and BUSDT buyer channels, verify funding transactions
and usability on both ends, then complete an APay payment and check asset balance
deltas on buyer, merchant and LSP. Flow 0 does not cover that two-asset scenario. Run this harness only in an isolated local
Regtest environment: it terminates matching processes and recreates its containers.

## Start regtest

E2E tests require an already running regtest environment:

```bash
cd "$RGBLN_REPO"
./regtest.sh start
```

`pytest` does not manage regtest lifecycle. This is intentional and matches the
upstream `rgb-lightning-node` workflow.

Stop regtest manually when done:

```bash
cd "$RGBLN_REPO"
./regtest.sh stop
```

## Run tests

From the `utexo-lsp` repo root:

```bash
python3 -m pytest tests/unit -vv
python3 -m pytest tests/e2e/tests/test_flow0_full_e2e.py -vv
```

Expected results:
- `tests/unit` — pass
- `test_flow0_full_e2e.py` — pass

On failure, diagnostic artifacts are written under `/tmp/utexo-lsp-e2e/`.

`pytest` does not reset blockchain state between runs. This matches the upstream
`rgb-lightning-node` test workflow: regtest is started externally once, then the
tests fund fresh wallets and create fresh node state per run.

## Environment

- `RGBLN_REPO` — path to `rgb-lightning-node`
- `UTEXO_LSP_REPO` — path to this repo
- `UTEXO_E2E_LOGS_DIR` — artifact directory
- `RGBLN_HOST` / `UTEXO_LSP_HOST` — service hosts
- `RGBLN_ENABLE_VIRTUAL_CHANNELS_V0` — virtual channels feature flag (default: `false`)
- `UTEXO_DEFAULT_VIRTUAL_OPEN_MODE` — virtual open mode forwarded to `utexo-lsp` when virtual channels are enabled
- `RGBLN_SHARED_PROXY_ENDPOINT` — optional explicit RGB proxy endpoint reachable from both Docker RLN and host SDK nodes; recommended for cross-environment portability

Notes:
- Virtual channels are disabled by default. To test virtual-channel behavior, set both:
  - `RGBLN_ENABLE_VIRTUAL_CHANNELS_V0=true`
  - `UTEXO_DEFAULT_VIRTUAL_OPEN_MODE=<mode>`
- If `RGBLN_SHARED_PROXY_ENDPOINT` is unset, the harness attempts Docker network gateway auto-discovery.

For port and password overrides, see [support/config.py](support/config.py).

## CI

In CI:
1. Build UniFFI bindings
2. Export `RGBLN_REPO`, `PYTHONPATH`, and `LD_LIBRARY_PATH`
3. Start regtest with `./regtest.sh start`
4. Run:
   - `python3 -m pytest tests/unit -vv`
   - `python3 -m pytest tests/e2e/tests/test_flow0_full_e2e.py -vv`
5. Stop regtest in cleanup
