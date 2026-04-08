from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path


def _env_int(name: str, default: int) -> int:
    return int(os.environ.get(name, str(default)))


def _env_bool(name: str, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


@dataclass(frozen=True)
class E2EConfig:
    rgbln_repo: Path = field(
        default_factory=lambda: Path(os.environ.get("RGBLN_REPO", "/home/mrmay/work/rgb-lightning-node"))
    )
    utexo_repo: Path = field(
        default_factory=lambda: Path(os.environ.get("UTEXO_LSP_REPO", "/home/mrmay/work/utexo-lsp"))
    )
    logs_dir: Path = field(default_factory=lambda: Path(os.environ.get("UTEXO_E2E_LOGS_DIR", "/tmp/utexo-lsp-e2e")))

    daemon_host: str = field(default_factory=lambda: os.environ.get("RGBLN_HOST", "127.0.0.1"))
    utexo_host: str = field(default_factory=lambda: os.environ.get("UTEXO_LSP_HOST", "127.0.0.1"))

    lsp_daemon_port: int = field(default_factory=lambda: _env_int("RGBLN_LSP_DAEMON_PORT", 3001))
    user_a_daemon_port: int = field(default_factory=lambda: _env_int("RGBLN_USER_A_DAEMON_PORT", 3002))
    user_b_daemon_port: int = field(default_factory=lambda: _env_int("RGBLN_USER_B_DAEMON_PORT", 3003))
    faucet_daemon_port: int = field(default_factory=lambda: _env_int("RGBLN_FAUCET_DAEMON_PORT", 3004))
    utexo_port: int = field(default_factory=lambda: _env_int("UTEXO_LSP_PORT", 8080))

    lsp_peer_port: int = field(default_factory=lambda: _env_int("RGBLN_LSP_PEER_PORT", 9735))
    user_a_peer_port: int = field(default_factory=lambda: _env_int("RGBLN_USER_A_PEER_PORT", 9736))
    user_b_peer_port: int = field(default_factory=lambda: _env_int("RGBLN_USER_B_PEER_PORT", 9737))
    faucet_peer_port: int = field(default_factory=lambda: _env_int("RGBLN_FAUCET_PEER_PORT", 9738))

    password: str = field(default_factory=lambda: os.environ.get("RGBLN_PASSWORD", "password123"))
    bitcoind_user: str = field(default_factory=lambda: os.environ.get("BITCOIND_RPC_USER", "user"))
    bitcoind_password: str = field(default_factory=lambda: os.environ.get("BITCOIND_RPC_PASSWORD", "password"))
    bitcoind_host: str = field(default_factory=lambda: os.environ.get("BITCOIND_RPC_HOST", "localhost"))
    bitcoind_port: int = field(default_factory=lambda: _env_int("BITCOIND_RPC_PORT", 18443))
    indexer_url: str = field(default_factory=lambda: os.environ.get("RGBLN_INDEXER_URL", "127.0.0.1:50001"))
    proxy_endpoint: str = field(default_factory=lambda: os.environ.get("RGBLN_PROXY_ENDPOINT", "rpc://127.0.0.1:3000/json-rpc"))
    docker_network: str = field(default_factory=lambda: os.environ.get("RGBLN_DOCKER_NETWORK", "rgb-lightning-node_default"))
    docker_image: str = field(default_factory=lambda: os.environ.get("RGBLN_DOCKER_IMAGE", "rgb-lightning-node"))
    docker_bitcoind_host: str = field(default_factory=lambda: os.environ.get("RGBLN_DOCKER_BITCOIND_HOST", "bitcoind"))
    docker_indexer_url: str = field(default_factory=lambda: os.environ.get("RGBLN_DOCKER_INDEXER_URL", "electrs:50001"))
    docker_proxy_endpoint: str = field(
        default_factory=lambda: os.environ.get("RGBLN_DOCKER_PROXY_ENDPOINT", "rpc://proxy:3000/json-rpc")
    )

    cron_every_seconds: int = 5
    payment_msat: int = 3_000_000
    payment_asset_amount: int = field(default_factory=lambda: _env_int("RGBLN_PAYMENT_ASSET_AMOUNT", 1))
    faucet_pay_amount: int = 1

    default_channel_capacity_sat: int = 200_000
    default_virtual_open_mode: str = field(
        default_factory=lambda: os.environ.get("UTEXO_DEFAULT_VIRTUAL_OPEN_MODE", "trusted_no_broadcast").strip()
    )
    enable_virtual_channels_v0: bool = field(
        default_factory=lambda: _env_bool("RGBLN_ENABLE_VIRTUAL_CHANNELS_V0", True)
    )

    node_boot_timeout_seconds: int = 30
    channel_timeout_seconds: int = 120
    payment_timeout_seconds: int = 60
    poll_interval_seconds: int = 2

    @property
    def lsp_container_name(self) -> str:
        return os.environ.get("RGBLN_LSP_CONTAINER_NAME", "utexo-e2e-lsp")

    @property
    def faucet_container_name(self) -> str:
        return os.environ.get("RGBLN_FAUCET_CONTAINER_NAME", "utexo-e2e-faucet")

    @property
    def lsp_url(self) -> str:
        return f"http://{self.daemon_host}:{self.lsp_daemon_port}"

    @property
    def user_a_url(self) -> str:
        return f"http://{self.daemon_host}:{self.user_a_daemon_port}"

    @property
    def user_b_url(self) -> str:
        return f"http://{self.daemon_host}:{self.user_b_daemon_port}"

    @property
    def faucet_url(self) -> str:
        return f"http://{self.daemon_host}:{self.faucet_daemon_port}"

    @property
    def utexo_url(self) -> str:
        return f"http://{self.utexo_host}:{self.utexo_port}"
