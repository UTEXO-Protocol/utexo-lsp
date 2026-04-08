from __future__ import annotations

from e2e.support.flows import run_lightning_receive_flow
from e2e.support.harness import sync_sdk_nodes


def test_lightning_receive_core_flow(env):
    run_lightning_receive_flow(env)

    sync_sdk_nodes(env)
    channels_a = env.user_a.listchannels()["channels"]
    assert channels_a, "expected at least one channel for User A"
    channel_a = next((c for c in channels_a if c["peer_pubkey"] == env.lsp_pubkey), channels_a[0])
    assert channel_a["is_usable"] is True
    assert channel_a["status"] == "Opened"

    channels_lsp = env.lsp_rln.listchannels()["channels"]
    assert len(channels_lsp) >= 2, "expected at least two LSP-side channels"
    channel_from_lsp = next((c for c in channels_lsp if c["peer_pubkey"] == env.user_a.nodeinfo()["pubkey"]), None)
    assert channel_from_lsp is not None, "expected an LSP-side channel for User A"
    assert channel_from_lsp["virtual_open_mode"] == env.cfg.default_virtual_open_mode
