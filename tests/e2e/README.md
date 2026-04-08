# E2E Tests

These tests cover `flow 0`.

Current harness shape:
- LSP RLN and Faucet run as dockerized daemon processes
- User A and User B run via the Python UniFFI `SdkNode` path
- regtest services (`bitcoind`, `electrs`, `proxy`) must already be running before `pytest`

Current status:
- `test_lightning_receive.py` is green
- `test_flow0_full_e2e.py` reproduces the current blocker at flow 0 step 8 (`User A pay invoice`)

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
cargo build --release --features uniffi --lib
./scripts/ci/uniffi_generate_python.sh

export RGBLN_REPO=/path/to/rgb-lightning-node
export PYTHONPATH="$RGBLN_REPO/target/uniffi/python:${PYTHONPATH:-}"
export LD_LIBRARY_PATH="$RGBLN_REPO/target/release:${LD_LIBRARY_PATH:-}"
```

The Python SDK is backed by the native Rust library, so both `PYTHONPATH` and
`LD_LIBRARY_PATH` are required.

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
python3 -m pytest tests/e2e/tests/test_lightning_receive.py -vv
python3 -m pytest tests/e2e/tests/test_flow0_full_e2e.py -vv
```

Expected results:
- `tests/unit` — pass
- `test_lightning_receive.py` — pass
- `test_flow0_full_e2e.py` — fails at flow 0 step 8 (`User A pay invoice`)

On failure, diagnostic artifacts are written under `/tmp/utexo-lsp-e2e/`.

`pytest` does not reset blockchain state between runs. This matches the upstream
`rgb-lightning-node` test workflow: regtest is started externally once, then the
tests fund fresh wallets and create fresh node state per run.

## Environment

- `RGBLN_REPO` — path to `rgb-lightning-node`
- `UTEXO_LSP_REPO` — path to this repo
- `UTEXO_E2E_LOGS_DIR` — artifact directory
- `RGBLN_HOST` / `UTEXO_LSP_HOST` — service hosts

For port and password overrides, see [support/config.py](support/config.py).

## CI

In CI:
1. Build UniFFI bindings
2. Export `RGBLN_REPO`, `PYTHONPATH`, and `LD_LIBRARY_PATH`
3. Start regtest with `./regtest.sh start`
4. Run:
   - `python3 -m pytest tests/unit -vv`
   - `python3 -m pytest tests/e2e/tests/test_lightning_receive.py -vv`
5. Stop regtest in cleanup

`test_flow0_full_e2e.py` should stay out of blocking CI until step 8 is implemented.
