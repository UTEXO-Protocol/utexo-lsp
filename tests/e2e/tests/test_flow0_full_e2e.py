from __future__ import annotations

from e2e.support.flows import run_lightning_receive_flow
from e2e.support.harness import sync_sdk_nodes
from e2e.support.utils import wait_until, write_json


def test_flow0_full_e2e(env):
    run_lightning_receive_flow(env)

    def user_a_has_outbound_liquidity():
        sync_sdk_nodes(env)
        channels = env.user_a.listchannels()["channels"]
        if not channels:
            return False
        channel = next((c for c in channels if c["peer_pubkey"] == env.lsp_pubkey), channels[0])
        if channel["status"] != "Opened" or channel["is_usable"] is not True:
            return False
        if channel["outbound_balance_msat"] != env.cfg.payment_msat:
            return False
        return channel

    wait_until(
        user_a_has_outbound_liquidity,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="User A outbound liquidity equals invoice amount",
    )

    b_invoice = env.user_b.lninvoice(
        amt_msat=env.cfg.payment_msat,
        expiry_sec=3600,
        asset_id=env.asset_id,
        asset_amount=env.cfg.payment_asset_amount,
    )["invoice"]

    decoded_b_invoice = env.user_b.decodelninvoice(b_invoice)
    assert decoded_b_invoice["asset_id"] == env.asset_id
    assert decoded_b_invoice["asset_amount"] == env.cfg.payment_asset_amount

    def dump_step8_snapshot(reason: str, *, pay: dict | None = None):
        sync_sdk_nodes(env)
        user_a_channels = env.user_a.listchannels()["channels"]
        user_b_channels = env.user_b.listchannels()["channels"]
        user_a_channel = next((c for c in user_a_channels if c["peer_pubkey"] == env.lsp_pubkey), None)
        user_b_channel = next((c for c in user_b_channels if c["peer_pubkey"] == env.lsp_pubkey), None)
        user_b_invoice_status = env.user_b.invoicestatus(b_invoice)
        summary = {
            "reason": reason,
            "asset_id": env.asset_id,
            "payment_msat": env.cfg.payment_msat,
            "payment_asset_amount": env.cfg.payment_asset_amount,
            "user_a_channel": user_a_channel,
            "user_b_channel": user_b_channel,
            "user_b_invoice_status": user_b_invoice_status,
            "sendpayment_result": pay,
        }
        write_json(env.artifact_dir / "flow0-step8-summary.json", summary)
        write_json(env.artifact_dir / "flow0-step8-b-invoice-decoded.json", decoded_b_invoice)
        if pay is not None:
            write_json(env.artifact_dir / "flow0-step8-sendpayment-result.json", pay)

    pay = env.user_a.sendpayment(b_invoice)
    if pay["status"] == "Failed":
        dump_step8_snapshot("sendpayment failed immediately", pay=pay)
    assert pay["status"] != "Failed", f"sendpayment failed immediately: {pay}"

    def user_b_invoice_succeeded():
        sync_sdk_nodes(env)
        return env.user_b.invoicestatus(b_invoice)["status"] == "Succeeded"

    try:
        wait_until(
            user_b_invoice_succeeded,
            timeout=env.cfg.payment_timeout_seconds,
            interval=env.cfg.poll_interval_seconds,
            desc="User B invoice succeeded",
        )
    except AssertionError:
        dump_step8_snapshot("User B invoice did not reach Succeeded", pay=pay)
        raise
