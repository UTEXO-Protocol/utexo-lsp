from __future__ import annotations

from e2e.support.harness import Env, mine, refresh_transfers, sync_sdk_nodes
from e2e.support.utils import wait_until


def run_lightning_receive_flow(env: Env):
    # User A must receive local RGB balance before it can send an RGB payment later.
    a_invoice = env.user_a.lninvoice(
        amt_msat=env.cfg.payment_msat,
        expiry_sec=3600,
        asset_id=env.asset_id,
        asset_amount=env.cfg.payment_asset_amount,
    )["invoice"]

    lr = env.lsp_api.lightning_receive(ln_invoice=a_invoice, asset_id=env.asset_id)
    rgb_invoice = lr["rgb_invoice"]

    decoded = env.faucet.decodergbinvoice(rgb_invoice)
    assignment = decoded["assignment"]
    if assignment["type"] == "Any":
        amount = env.cfg.faucet_pay_amount
    elif assignment["type"] == "Fungible":
        amount = assignment["value"] or env.cfg.faucet_pay_amount
    else:
        raise AssertionError(f"expected fungible receive invoice, got {assignment}")

    env.faucet.sendrgbinvoice(
        {
            "invoice": rgb_invoice,
            "asset_id": env.asset_id,
            "amount": amount,
            "donation": False,
            "fee_rate": 7,
            "min_confirmations": 1,
        }
    )

    def rgb_delivery_settled():
        mine(env, 1)
        refresh_transfers(env)

        lsp_transfers = env.lsp_rln.listtransfers(env.asset_id)["transfers"]
        faucet_transfers = env.faucet.listtransfers(env.asset_id)["transfers"]

        lsp_receive = next((t for t in lsp_transfers if t["kind"] == "ReceiveBlind"), None)
        faucet_send = next((t for t in faucet_transfers if t["kind"] == "Send"), None)

        if lsp_receive is None or faucet_send is None:
            return False
        if lsp_receive["status"] != "Settled" or faucet_send["status"] != "Settled":
            return False
        return True

    wait_until(
        rgb_delivery_settled,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="RGB delivery settled on LSP and faucet",
    )

    def user_a_invoice_succeeded():
        sync_sdk_nodes(env)
        return env.user_a.invoicestatus(a_invoice)["status"] == "Succeeded"

    wait_until(
        user_a_invoice_succeeded,
        timeout=env.cfg.payment_timeout_seconds,
        interval=env.cfg.poll_interval_seconds,
        desc="User A invoice succeeded",
    )

    return {
        "a_invoice": a_invoice,
    }
