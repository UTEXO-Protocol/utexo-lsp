from __future__ import annotations

import os
import shutil
import socket
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

from e2e.clients.lsp import LspApiClient
from e2e.clients.rln import RlnClient
from e2e.clients.sdk_node import SdkNodeClient
from e2e.support.config import E2EConfig
from e2e.support.utils import wait_until, write_json


@dataclass
class Env:
    cfg: E2EConfig
    lsp_rln: RlnClient
    user_a: RlnClient | SdkNodeClient
    user_b: RlnClient | SdkNodeClient
    faucet: RlnClient
    lsp_api: LspApiClient
    asset_id: str
    lsp_pubkey: str
    artifact_dir: Path


# Process & container management
def kill_matching(pattern: str):
    subprocess.run(["pkill", "-f", pattern], check=False)


def remove_container(name: str):
    subprocess.run(["docker", "rm", "-f", name], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def terminate_process(process: subprocess.Popen | None):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def reset_environment(cfg: E2EConfig):
    kill_matching("^rgb-lightning-node ")
    kill_matching("utexo-lsp")
    kill_matching("go run \\.")
    remove_container(cfg.lsp_container_name)
    remove_container(cfg.faucet_container_name)

    shutil.rmtree(cfg.logs_dir, ignore_errors=True)


# Runtime / bootstrap
def ensure_rln_docker_image(cfg: E2EConfig):
    inspect = subprocess.run(
        ["docker", "image", "inspect", cfg.docker_image],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    if inspect.returncode == 0:
        return
    subprocess.run(["docker", "build", "-t", cfg.docker_image, "."], cwd=cfg.rgbln_repo, check=True)


def spawn_rln_node(cfg: E2EConfig, name: str, datadir: Path, daemon_port: int, peer_port: int, log_path: Path):
    log_path.parent.mkdir(parents=True, exist_ok=True)
    datadir.mkdir(parents=True, exist_ok=True)
    log_file = log_path.open("wb")
    remove_container(name)
    cmd = [
        "docker",
        "run",
        "--rm",
        "--name",
        name,
        "--user",
        f"{os.getuid()}:{os.getgid()}",
        "-p",
        f"{daemon_port}:{daemon_port}",
        "-p",
        f"{peer_port}:{peer_port}",
        "-v",
        f"{datadir}:/RLNdata",
        "--network",
        cfg.docker_network,
        cfg.docker_image,
        "/RLNdata",
        "--daemon-listening-port",
        str(daemon_port),
        "--ldk-peer-listening-port",
        str(peer_port),
        "--network",
        "regtest",
        "--disable-authentication",
    ]
    if cfg.enable_virtual_channels_v0:
        cmd.append("--enable-virtual-channels-v0")
    return subprocess.Popen(
        cmd,
        cwd=cfg.rgbln_repo,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )


def create_sdk_node(
    cfg: E2EConfig,
    storage_dir: Path,
    daemon_port: int,
    peer_port: int,
    *,
    virtual_peer_pubkeys: list[str] | None = None,
):
    import rgb_lightning_node as rln

    storage_dir.mkdir(parents=True, exist_ok=True)
    return rln.SdkNode.create(
        rln.SdkInitRequest(
            storage_dir_path=str(storage_dir),
            daemon_listening_port=daemon_port,
            ldk_peer_listening_port=peer_port,
            network="regtest",
            max_media_upload_size_mb=20,
            enable_virtual_channels_v0=cfg.enable_virtual_channels_v0,
            virtual_peer_pubkeys=virtual_peer_pubkeys,
        )
    )


def docker_unlock_payload(cfg: E2EConfig) -> dict[str, object]:
    # Dockerized RLN daemons must reach sibling services by compose service name,
    # unlike SDK nodes running on the host, which use localhost endpoints.
    return {
        "password": cfg.password,
        "bitcoind_rpc_username": cfg.bitcoind_user,
        "bitcoind_rpc_password": cfg.bitcoind_password,
        "bitcoind_rpc_host": cfg.docker_bitcoind_host,
        "bitcoind_rpc_port": cfg.bitcoind_port,
        "indexer_url": cfg.docker_indexer_url,
        "proxy_endpoint": cfg.docker_proxy_endpoint,
        "announce_addresses": [],
    }


def wait_for_rln_boot(client: RlnClient, cfg: E2EConfig):
    host = client.base_url.split("://", 1)[-1].split("/", 1)[0]
    if ":" in host:
        hostname, port_str = host.rsplit(":", 1)
        port = int(port_str)
    else:
        hostname = host
        port = 80

    def ready():
        try:
            with socket.create_connection((hostname, port), timeout=1):
                return True
        except OSError:
            return False

    wait_until(ready, timeout=cfg.node_boot_timeout_seconds, interval=1, desc=f"{client.base_url} boot")


def wait_for_utexo_boot(client: LspApiClient, cfg: E2EConfig):
    def ready():
        try:
            return client.get("/health")["ok"] is True
        except Exception:  # noqa: BLE001
            return False

    wait_until(ready, timeout=cfg.node_boot_timeout_seconds, interval=1, desc="utexo-lsp health")


def fund_nodes(cfg: E2EConfig, clients: dict[str, object]):
    addresses = [clients["lsp"].address(), clients["user_a"].address(), clients["user_b"].address(), clients["faucet"].address()]
    for address in addresses:
        subprocess.run([str(cfg.rgbln_repo / "regtest.sh"), "sendtoaddress", address, "1"], cwd=cfg.rgbln_repo, check=True)
    subprocess.run([str(cfg.rgbln_repo / "regtest.sh"), "mine", "6"], cwd=cfg.rgbln_repo, check=True)
    sync_clients(clients.values())


def create_utxos(cfg: E2EConfig, clients: dict[str, object]):
    for client in clients.values():
        client.createutxos()
    subprocess.run([str(cfg.rgbln_repo / "regtest.sh"), "mine", "1"], cwd=cfg.rgbln_repo, check=True)
    sync_clients(clients.values())


def ensure_faucet_asset_balance(cfg: E2EConfig, faucet: RlnClient, asset_id: str):
    balance = faucet.assetbalance(asset_id)
    if balance["settled"] <= 0:
        raise AssertionError(f"faucet did not have expected asset balance after issuance: {balance}")


# RGB / transfer helpers
def seed_lsp_from_faucet(cfg: E2EConfig, lsp: RlnClient, faucet: RlnClient, asset_id: str):
    # LSP must receive at least one unit before /rgbinvoice can reference this
    # contract; otherwise the LSP-side RGB node returns UnknownContractId.
    invoice = lsp.rgbinvoice_any()
    faucet.sendrgb(
        {
            "donation": True,
            "fee_rate": 7,
            "min_confirmations": 1,
            "skip_sync": False,
            "recipient_map": {
                asset_id: [
                    {
                        "recipient_id": invoice["recipient_id"],
                        "assignment": {"type": "Fungible", "value": 1},
                        "transport_endpoints": [cfg.docker_proxy_endpoint],
                    }
                ]
            },
        }
    )
    subprocess.run(
        [str(cfg.rgbln_repo / "regtest.sh"), "mine", "1"],
        cwd=cfg.rgbln_repo,
        check=True,
    )

    def settled():
        refresh_transfers_for_clients(lsp, faucet)
        lsp_recv = next(
            (t for t in reversed(lsp.listtransfers(asset_id)["transfers"]) if t["kind"] == "ReceiveBlind"),
            None,
        )
        faucet_send = next(
            (t for t in reversed(faucet.listtransfers(asset_id)["transfers"]) if t["kind"] == "Send"),
            None,
        )
        if not lsp_recv or not faucet_send:
            return False
        return lsp_recv["status"] == "Settled" and faucet_send["status"] == "Settled"

    wait_until(
        settled,
        timeout=cfg.payment_timeout_seconds,
        interval=cfg.poll_interval_seconds,
        desc="LSP seeded from faucet",
    )

    balance = lsp.assetbalance(asset_id)
    if balance["settled"] < 1:
        raise AssertionError(f"LSP did not receive seeded asset from faucet: {balance}")


def spawn_utexo_lsp(cfg: E2EConfig, asset_id: str, log_path: Path):
    env = os.environ.copy()
    env["LSP_BASE_URL"] = cfg.lsp_url
    env["RGB_NODE_BASE_URL"] = cfg.lsp_url
    env["SUPPORTED_ASSET_IDS"] = asset_id
    env["CRON_EVERY"] = f"{cfg.cron_every_seconds}s"
    env["DEFAULT_CHANNEL_CAPACITY_SAT"] = str(cfg.default_channel_capacity_sat)
    if cfg.default_virtual_open_mode:
        env["DEFAULT_VIRTUAL_OPEN_MODE"] = cfg.default_virtual_open_mode
    else:
        env.pop("DEFAULT_VIRTUAL_OPEN_MODE", None)

    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_file = log_path.open("wb")
    return subprocess.Popen(
        ["go", "run", "."],
        cwd=cfg.utexo_repo,
        env=env,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )


def mine(env: Env, blocks: int):
    subprocess.run(
        [str(env.cfg.rgbln_repo / "regtest.sh"), "mine", str(blocks)],
        cwd=env.cfg.rgbln_repo,
        check=True,
    )
    sync_sdk_nodes(env)


def refresh_transfers(env: Env):
    refresh_transfers_for_clients(env.lsp_rln, env.faucet)


def refresh_transfers_for_clients(lsp_rln: RlnClient, faucet: RlnClient):
    # Double refresh is intentional. In practice the first call kicks transfer
    # processing and the second surfaces the updated RGB state reliably.
    lsp_rln.refreshtransfers()
    lsp_rln.refreshtransfers()
    faucet.refreshtransfers()
    faucet.refreshtransfers()


def sync_clients(clients: Iterable[object]):
    for client in clients:
        sync = getattr(client, "sync", None)
        if callable(sync):
            sync()


def sync_sdk_nodes(env: Env):
    sync_clients((env.user_a, env.user_b))


def wait_for_channels_usable(env: Env):
    mine(env, 6)

    def ready():
        sync_sdk_nodes(env)
        channels_lsp = env.lsp_rln.listchannels()["channels"]
        channels_a = env.user_a.listchannels()["channels"]
        channels_b = env.user_b.listchannels()["channels"]
        if len(channels_lsp) < 2 or len(channels_a) < 1 or len(channels_b) < 1:
            # LSP opens channels asynchronously after connectpeer; on regtest we
            # advance the chain until those channels become visible and usable.
            mine(env, 1)
            return False
        if not all(c["status"] == "Opened" and c["ready"] and c["is_usable"] for c in channels_lsp + channels_a + channels_b):
            mine(env, 1)
            return False
        expected_mode = env.cfg.default_virtual_open_mode
        if expected_mode and not all(c.get("virtual_open_mode") == expected_mode for c in channels_lsp):
            mine(env, 1)
            return False
        return True

    wait_until(ready, timeout=env.cfg.channel_timeout_seconds, interval=env.cfg.poll_interval_seconds, desc="usable channels")


# Snapshot / debug
def dump_current_state(env: Env):
    try:
        write_json(Path(env.artifact_dir) / "lsp-listchannels.json", env.lsp_rln.listchannels())
        write_json(Path(env.artifact_dir) / "user-a-listchannels.json", env.user_a.listchannels())
        write_json(Path(env.artifact_dir) / "user-b-listchannels.json", env.user_b.listchannels())
        write_json(Path(env.artifact_dir) / "user-a-listpayments.json", env.user_a.listpayments())
        write_json(Path(env.artifact_dir) / "user-b-listpayments.json", env.user_b.listpayments())
        write_json(Path(env.artifact_dir) / "lsp-listtransfers.json", env.lsp_rln.listtransfers(env.asset_id))
        write_json(Path(env.artifact_dir) / "faucet-listtransfers.json", env.faucet.listtransfers(env.asset_id))
    except Exception:  # noqa: BLE001
        pass
