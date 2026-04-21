from __future__ import annotations

import subprocess
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

import pytest

from e2e.clients.sdk_node import SdkNodeClient
from e2e.support.config import E2EConfig
from e2e.support.harness import create_sdk_node, reset_environment
from e2e.support.utils import wait_until, write_json


@dataclass
class VirtualPaymentEnv:
    cfg: E2EConfig
    node_a: SdkNodeClient
    node_b: SdkNodeClient
    asset_id: str
    node_a_pubkey: str
    node_b_pubkey: str
    artifact_dir: Path


def _run_regtest(cfg: E2EConfig, *args: str):
    subprocess.run([str(cfg.rgbln_repo / "regtest.sh"), *args], cwd=cfg.rgbln_repo, check=True)


def _sync_nodes(env: VirtualPaymentEnv):
    env.node_a.sync()
    env.node_b.sync()


def _fund_and_prepare(cfg: E2EConfig, node_a: SdkNodeClient, node_b: SdkNodeClient):
    for address in (node_a.address(), node_b.address()):
        _run_regtest(cfg, "sendtoaddress", address, "1")
    _run_regtest(cfg, "mine", "6")
    node_a.sync()
    node_b.sync()

    node_a.createutxos()
    node_b.createutxos()
    _run_regtest(cfg, "mine", "1")
    node_a.sync()
    node_b.sync()


@pytest.fixture(scope="function")
def virtual_payment_env(cfg: E2EConfig, request: pytest.FixtureRequest):
    reset_environment(cfg)

    artifact_dir = cfg.logs_dir / datetime.now().strftime("%Y%m%d-%H%M%S-%f")
    artifact_dir.mkdir(parents=True, exist_ok=True)

    node_a_sdk = create_sdk_node(
        cfg,
        artifact_dir / "sdk-node-a",
        cfg.user_a_daemon_port,
        cfg.user_a_peer_port,
    )
    request.addfinalizer(node_a_sdk.shutdown)
    node_a = SdkNodeClient(node_a_sdk)
    node_a.init(cfg.password)
    node_a.unlock(cfg)
    node_a_pubkey = node_a.nodeinfo()["pubkey"]

    node_b_sdk = create_sdk_node(
        cfg,
        artifact_dir / "sdk-node-b",
        cfg.user_b_daemon_port,
        cfg.user_b_peer_port,
        # Virtual client nodes must explicitly allow the host peer.
        virtual_peer_pubkeys=[node_a_pubkey],
    )
    request.addfinalizer(node_b_sdk.shutdown)
    node_b = SdkNodeClient(node_b_sdk)
    node_b.init(cfg.password)
    node_b.unlock(cfg)
    node_b_pubkey = node_b.nodeinfo()["pubkey"]

    _fund_and_prepare(cfg, node_a, node_b)

    asset_resp = node_a.issueassetnia()
    asset_id = asset_resp["asset"]["asset_id"]
    balance = node_a.assetbalance(asset_id)
    assert balance["settled"] > 0, f"node A asset issuance did not settle: {balance}"

    env = VirtualPaymentEnv(
        cfg=cfg,
        node_a=node_a,
        node_b=node_b,
        asset_id=asset_id,
        node_a_pubkey=node_a_pubkey,
        node_b_pubkey=node_b_pubkey,
        artifact_dir=artifact_dir,
    )

    def dump_state():
        _sync_nodes(env)
        write_json(artifact_dir / "node-a-listchannels.json", env.node_a.listchannels())
        write_json(artifact_dir / "node-b-listchannels.json", env.node_b.listchannels())
        write_json(artifact_dir / "node-a-listpayments.json", env.node_a.listpayments())
        write_json(artifact_dir / "node-b-listpayments.json", env.node_b.listpayments())

    request.addfinalizer(dump_state)
    return env


def test_virtual_channel_asset_payment_succeeds(virtual_payment_env: VirtualPaymentEnv):
    env = virtual_payment_env
    peer_uri = f"{env.node_b_pubkey}@{env.cfg.daemon_host}:{env.cfg.user_b_peer_port}"

    env.node_a.connectpeer(peer_uri)
    # This is the control path: open a known-good virtual RGB channel directly,
    # without involving the LSP provisioning logic under investigation.
    open_response = env.node_a.openchannel(
        peer_pubkey_and_opt_addr=peer_uri,
        capacity_sat=100_000,
        push_msat=0,
        public=False,
        with_anchors=True,
        asset_id=env.asset_id,
        asset_amount=200,
        push_asset_amount=None,
        virtual_open_mode=env.cfg.default_virtual_open_mode,
    )
    assert open_response["temporary_channel_id"]

    def node_a_virtual_rgb_channel_ready():
        _sync_nodes(env)
        channels = env.node_a.listchannels()["channels"]
        channel = next((c for c in channels if c["peer_pubkey"] == env.node_b_pubkey), None)
        if channel is None:
            return False
        if channel["status"] != "Opened" or channel["is_usable"] is not True:
            return False
        if channel["virtual_open_mode"] != env.cfg.default_virtual_open_mode:
            return False
        if channel["asset_id"] != env.asset_id:
            return False
        if channel["asset_local_amount"] is None and channel["asset_remote_amount"] is None:
            return False
        return channel

    node_a_channel = wait_until(
        node_a_virtual_rgb_channel_ready,
        timeout=env.cfg.channel_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="node A virtual RGB channel ready",
    )
    assert node_a_channel["asset_id"] == env.asset_id
    assert node_a_channel["virtual_open_mode"] == env.cfg.default_virtual_open_mode

    def node_b_rgb_channel_visible():
        _sync_nodes(env)
        channels = env.node_b.listchannels()["channels"]
        channel = next((c for c in channels if c["peer_pubkey"] == env.node_a_pubkey), None)
        if channel is None:
            return False
        if channel["status"] != "Opened" or channel["is_usable"] is not True:
            return False
        if channel["asset_id"] != env.asset_id:
            return False
        if channel["asset_local_amount"] is None and channel["asset_remote_amount"] is None:
            return False
        return channel

    node_b_channel = wait_until(
        node_b_rgb_channel_visible,
        timeout=env.cfg.channel_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="node B RGB channel visible",
    )
    assert node_b_channel["asset_id"] == env.asset_id
    initial_node_a_balance = env.node_a.assetbalance(env.asset_id)
    initial_node_b_balance = env.node_b.assetbalance(env.asset_id)

    invoice = env.node_b.lninvoice(
        amt_msat=env.cfg.payment_msat,
        expiry_sec=3600,
        asset_id=env.asset_id,
        asset_amount=env.cfg.payment_asset_amount,
    )["invoice"]
    decoded_invoice = env.node_a.decodelninvoice(invoice)
    assert decoded_invoice["asset_id"] == env.asset_id
    assert decoded_invoice["asset_amount"] == env.cfg.payment_asset_amount

    payment = env.node_a.sendpayment(invoice)
    assert payment["status"] != "Failed", f"sendpayment failed immediately: {payment}"

    def node_b_invoice_succeeded():
        _sync_nodes(env)
        return env.node_b.invoicestatus(invoice)["status"] == "Succeeded"

    wait_until(
        node_b_invoice_succeeded,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="node B RGB invoice succeeded",
    )

    def offchain_balances_updated():
        _sync_nodes(env)
        node_a_balance = env.node_a.assetbalance(env.asset_id)
        node_b_balance = env.node_b.assetbalance(env.asset_id)
        if node_a_balance["offchain_outbound"] != initial_node_a_balance["offchain_outbound"] - env.cfg.payment_asset_amount:
            return False
        if node_b_balance["offchain_outbound"] != initial_node_b_balance["offchain_outbound"] + env.cfg.payment_asset_amount:
            return False
        return {"node_a": node_a_balance, "node_b": node_b_balance}

    updated_balances = wait_until(
        offchain_balances_updated,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="offchain RGB balances updated after payment",
    )
    assert updated_balances["node_a"]["offchain_outbound"] == (
        initial_node_a_balance["offchain_outbound"] - env.cfg.payment_asset_amount
    )
    assert updated_balances["node_b"]["offchain_outbound"] == (
        initial_node_b_balance["offchain_outbound"] + env.cfg.payment_asset_amount
    )

    # Send the asset back in the opposite direction to prove the same virtual
    # channel supports symmetric RGB payments and restores balances as expected.
    reverse_invoice = env.node_a.lninvoice(
        amt_msat=env.cfg.payment_msat,
        expiry_sec=3600,
        asset_id=env.asset_id,
        asset_amount=env.cfg.payment_asset_amount,
    )["invoice"]
    decoded_reverse_invoice = env.node_b.decodelninvoice(reverse_invoice)
    assert decoded_reverse_invoice["asset_id"] == env.asset_id
    assert decoded_reverse_invoice["asset_amount"] == env.cfg.payment_asset_amount

    reverse_payment = env.node_b.sendpayment(reverse_invoice)
    assert reverse_payment["status"] != "Failed", f"reverse sendpayment failed immediately: {reverse_payment}"

    def node_a_invoice_succeeded():
        _sync_nodes(env)
        return env.node_a.invoicestatus(reverse_invoice)["status"] == "Succeeded"

    wait_until(
        node_a_invoice_succeeded,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="node A reverse RGB invoice succeeded",
    )

    def offchain_balances_restored():
        _sync_nodes(env)
        node_a_balance = env.node_a.assetbalance(env.asset_id)
        node_b_balance = env.node_b.assetbalance(env.asset_id)
        if node_a_balance["offchain_outbound"] != initial_node_a_balance["offchain_outbound"]:
            return False
        if node_b_balance["offchain_outbound"] != initial_node_b_balance["offchain_outbound"]:
            return False
        return {"node_a": node_a_balance, "node_b": node_b_balance}

    restored_balances = wait_until(
        offchain_balances_restored,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="offchain RGB balances restored after reverse payment",
    )
    assert restored_balances["node_a"]["offchain_outbound"] == initial_node_a_balance["offchain_outbound"]
    assert restored_balances["node_b"]["offchain_outbound"] == initial_node_b_balance["offchain_outbound"]
