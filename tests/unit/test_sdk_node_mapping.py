from __future__ import annotations

from types import SimpleNamespace

from e2e.clients.sdk_node import _channel_to_dict, _maybe_str, _payment_to_dict


def test_maybe_str_preserves_none_and_stringifies_values():
    assert _maybe_str(None) is None
    assert _maybe_str(123) == "123"


def test_channel_to_dict_maps_sdk_channel_shape():
    channel = SimpleNamespace(
        channel_id="abc",
        peer_pubkey="peer",
        status=SimpleNamespace(name="OPENED"),
        ready=True,
        capacity_sat=100_000,
        local_balance_sat=50_000,
        outbound_balance_msat=3_000_000,
        inbound_balance_msat=0,
        next_outbound_htlc_limit_msat=1_000_000,
        next_outbound_htlc_minimum_msat=3_000_000,
        is_usable=True,
        public=False,
        funding_txid=None,
        peer_alias="lsp",
        short_channel_id="1x1x1",
        asset_id=None,
        asset_local_amount=1,
        asset_remote_amount=2,
        virtual_open_mode="trusted_no_broadcast",
    )

    mapped = _channel_to_dict(channel)

    assert mapped["channel_id"] == "abc"
    assert mapped["peer_pubkey"] == "peer"
    assert mapped["status"] == "Opened"
    assert mapped["is_usable"] is True
    assert mapped["funding_txid"] is None
    assert mapped["virtual_open_mode"] == "trusted_no_broadcast"


def test_payment_to_dict_maps_nullable_fields():
    payment = SimpleNamespace(
        amt_msat=3_000_000,
        asset_amount=1,
        asset_id=None,
        payment_hash="hash",
        payment_type=SimpleNamespace(name="OUTBOUND"),
        status=SimpleNamespace(name="FAILED"),
        created_at=1,
        updated_at=2,
        payee_pubkey=None,
        preimage=None,
    )

    mapped = _payment_to_dict(payment)

    assert mapped["amt_msat"] == 3_000_000
    assert mapped["asset_amount"] == 1
    assert mapped["asset_id"] is None
    assert mapped["inbound"] is False
    assert mapped["payment_type"] == "Outbound"
    assert mapped["status"] == "Failed"
    assert mapped["payee_pubkey"] is None
