"""Contracts from RLN v0.13.0-beta.3 (af03c7f) routes.rs and uniffi_api/types.rs."""
from dataclasses import replace
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from e2e.clients.rln import RlnClient
from e2e.clients.sdk_node import SdkNodeClient
from e2e.support.config import E2EConfig
from e2e.support.harness import docker_unlock_payload, ensure_rln_docker_image


@pytest.mark.parametrize("docker", [False, True])
def test_unlock_chain_sync_and_endpoint_scopes(docker):
    cfg = replace(E2EConfig(), bitcoind_host="host-rpc", docker_bitcoind_host="docker-rpc",
                  indexer_url="host-indexer:50001", docker_indexer_url="docker-indexer:50001",
                  proxy_endpoint="rpc://host-proxy/json-rpc", shared_proxy_endpoint="rpc://shared/json-rpc")
    client = RlnClient("http://unused")
    client.post = Mock()
    if docker:
        payload = docker_unlock_payload(cfg)
    else:
        client.unlock(cfg)
        endpoint, payload = client.post.call_args.args
        assert endpoint == "/unlock"
    assert payload["ldk_chain_sync"] == {
        "mode": "BlockSync", "config": {
            "bitcoind_rpc_username": cfg.bitcoind_user,
            "bitcoind_rpc_password": cfg.bitcoind_password,
            "bitcoind_rpc_host": "docker-rpc" if docker else "host-rpc",
            "bitcoind_rpc_port": cfg.bitcoind_port,
        },
    }
    assert not any(k.startswith("bitcoind_rpc_") for k in payload)
    assert payload["indexer_url"] == (cfg.docker_indexer_url if docker else cfg.indexer_url)
    assert payload["proxy_endpoint"] == (cfg.shared_proxy_endpoint if docker else cfg.proxy_endpoint)


def test_docker_proxy_alias():
    cfg = replace(E2EConfig(), shared_proxy_endpoint="", docker_proxy_endpoint="rpc://proxy:3000/json-rpc",
                  docker_proxy_host_alias="host.test")
    assert docker_unlock_payload(cfg)["proxy_endpoint"] == "rpc://host.test:3000/json-rpc"


def test_missing_image_never_builds(monkeypatch):
    run = Mock(return_value=SimpleNamespace(returncode=1))
    monkeypatch.setattr("e2e.support.harness.subprocess.run", run)
    with pytest.raises(RuntimeError, match="Prebuilt RLN image"):
        ensure_rln_docker_image(E2EConfig())
    assert run.call_count == 1
    assert run.call_args.args[0][:3] == ["docker", "image", "inspect"]


def test_rest_transfer_filter_and_invoice():
    client = RlnClient("http://unused")
    client.post = Mock()
    client.listtransfers("rgb:asset")
    client.post.assert_called_with("/listtransfers", {"asset_filter": {"type": "Id", "value": "rgb:asset"}})
    client.rgbinvoice_any()
    assert client.post.call_args.args[1]["transport_endpoints"] == []
    assert "expiration_timestamp" not in client.post.call_args.args[1]


def test_sdk_unlock_uses_typed_chain_config(monkeypatch):
    def block_sync(*, bitcoind_rpc_username, bitcoind_rpc_password, bitcoind_rpc_host, bitcoind_rpc_port):
        return locals()

    def unlock_request(*, password, ldk_chain_sync, indexer_url, proxy_endpoint,
                       announce_addresses, announce_alias, gossip_rgs_server_url):
        return locals()

    monkeypatch.setattr("e2e.clients.sdk_node._rln", lambda: SimpleNamespace(
        SdkLdkChainSync=SimpleNamespace(BLOCK_SYNC=block_sync), SdkUnlockRequest=unlock_request))
    node = Mock()
    cfg = E2EConfig()
    SdkNodeClient(node).unlock(cfg)
    payload = node.unlock.call_args.args[0]
    assert payload["ldk_chain_sync"]["bitcoind_rpc_host"] == cfg.bitcoind_host
    assert payload["gossip_rgs_server_url"] is None
