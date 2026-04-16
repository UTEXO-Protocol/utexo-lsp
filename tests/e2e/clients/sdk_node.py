from __future__ import annotations

from typing import Any


def _rln():
    import rgb_lightning_node as rln

    return rln


def _maybe_str(value: Any):
    if value is None:
        return None
    return str(value)


def _channel_to_dict(channel: Any) -> dict[str, Any]:
    return {
        "channel_id": str(channel.channel_id),
        "peer_pubkey": str(channel.peer_pubkey),
        "status": channel.status.name.title(),
        "ready": bool(channel.ready),
        "capacity_sat": int(channel.capacity_sat),
        "local_balance_sat": int(channel.local_balance_sat),
        "outbound_balance_msat": int(channel.outbound_balance_msat),
        "inbound_balance_msat": int(channel.inbound_balance_msat),
        "next_outbound_htlc_limit_msat": int(channel.next_outbound_htlc_limit_msat),
        "next_outbound_htlc_minimum_msat": int(channel.next_outbound_htlc_minimum_msat),
        "is_usable": bool(channel.is_usable),
        "public": bool(channel.public),
        "funding_txid": _maybe_str(channel.funding_txid),
        "peer_alias": channel.peer_alias,
        "short_channel_id": channel.short_channel_id,
        "asset_id": _maybe_str(channel.asset_id),
        "asset_local_amount": channel.asset_local_amount,
        "asset_remote_amount": channel.asset_remote_amount,
        "virtual_open_mode": channel.virtual_open_mode,
    }


def _payment_to_dict(payment: Any) -> dict[str, Any]:
    return {
        "amt_msat": payment.amt_msat,
        "asset_amount": payment.asset_amount,
        "asset_id": _maybe_str(payment.asset_id),
        "payment_hash": str(payment.payment_hash),
        "inbound": bool(payment.inbound),
        "status": payment.status.name.title(),
        "created_at": int(payment.created_at),
        "updated_at": int(payment.updated_at),
        "payee_pubkey": _maybe_str(payment.payee_pubkey),
        "preimage": payment.preimage,
    }


def _decode_ln_invoice_to_dict(decoded: Any) -> dict[str, Any]:
    return {
        "amt_msat": decoded.amt_msat,
        "expiry_sec": int(decoded.expiry_sec),
        "timestamp": int(decoded.timestamp),
        "asset_id": _maybe_str(decoded.asset_id),
        "asset_amount": decoded.asset_amount,
        "payment_hash": str(decoded.payment_hash),
        "payment_secret": decoded.payment_secret,
        "payee_pubkey": _maybe_str(decoded.payee_pubkey),
        "network": decoded.network,
    }


class SdkNodeClient:
    def __init__(self, node: Any):
        self._node = node

    def shutdown(self):
        self._node.shutdown()

    def sync(self):
        self._node.sync()

    def init(self, password: str):
        mnemonic = self._node.init(password, None)
        return {"mnemonic": mnemonic}

    def unlock(self, cfg):
        rln = _rln()
        return self._node.unlock(
            rln.SdkUnlockRequest(
                password=cfg.password,
                bitcoind_rpc_username=cfg.bitcoind_user,
                bitcoind_rpc_password=cfg.bitcoind_password,
                bitcoind_rpc_host=cfg.bitcoind_host,
                bitcoind_rpc_port=cfg.bitcoind_port,
                indexer_url=cfg.indexer_url,
                proxy_endpoint=cfg.proxy_endpoint,
                announce_addresses=[],
                announce_alias=None,
            )
        )

    def nodeinfo(self):
        info = self._node.node_info()
        return {
            "pubkey": str(info.pubkey),
            "num_channels": int(info.num_channels),
            "num_usable_channels": int(info.num_usable_channels),
            "local_balance_sat": int(info.local_balance_sat),
            "rgb_htlc_min_msat": int(info.rgb_htlc_min_msat),
        }

    def address(self):
        return self._node.address().address

    def btcbalance(self):
        balance = self._node.btc_balance(False)
        return {
            "vanilla": {
                "settled": int(balance.vanilla.settled),
                "future": int(balance.vanilla.future),
                "spendable": int(balance.vanilla.spendable),
            },
            "colored": {
                "settled": int(balance.colored.settled),
                "future": int(balance.colored.future),
                "spendable": int(balance.colored.spendable),
            },
        }

    def createutxos(self, *, num: int = 10, size: int = 100000, fee_rate: int = 7):
        rln = _rln()
        return self._node.createutxos(
            rln.SdkCreateUtxosRequest(
                up_to=False,
                num=num,
                size=size,
                fee_rate=fee_rate,
                skip_sync=False,
            )
        )

    def connectpeer(self, peer_uri: str):
        return self._node.connectpeer(peer_uri)

    def listchannels(self):
        return {"channels": [_channel_to_dict(channel) for channel in self._node.list_channels()]}

    def listpayments(self):
        return {"payments": [_payment_to_dict(payment) for payment in self._node.list_payments()]}

    def lninvoice(
        self,
        *,
        amt_msat: int,
        expiry_sec: int,
        asset_id: str | None = None,
        asset_amount: int | None = None,
    ):
        rln = _rln()
        response = self._node.ln_invoice(
            rln.LnInvoiceRequest(
                amt_msat=amt_msat,
                expiry_sec=expiry_sec,
                asset_id=asset_id,
                asset_amount=asset_amount,
                payment_hash=None,
                description_hash=None,
            )
        )
        return {"invoice": response.invoice}

    def decodelninvoice(self, invoice: str):
        decoded = self._node.decode_ln_invoice(invoice)
        return _decode_ln_invoice_to_dict(decoded)

    def invoicestatus(self, invoice: str):
        status = self._node.invoice_status(invoice)
        return {"status": status.name.title()}

    def sendpayment(
        self,
        invoice: str,
        *,
        amt_msat: int | None = None,
        asset_id: str | None = None,
        asset_amount: int | None = None,
    ):
        rln = _rln()
        decoded = self._node.decode_ln_invoice(invoice)
        response = self._node.sendpayment(
            rln.SdkSendPaymentRequest(
                invoice=invoice,
                amt_msat=amt_msat if amt_msat is not None else decoded.amt_msat,
                asset_id=asset_id if asset_id is not None else decoded.asset_id,
                asset_amount=asset_amount if asset_amount is not None else decoded.asset_amount,
            )
        )
        return {
            "payment_id": str(response.payment_id),
            "payment_hash": str(response.payment_hash),
            "payment_secret": response.payment_secret,
            "status": response.status.name.title(),
        }
