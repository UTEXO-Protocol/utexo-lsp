from __future__ import annotations

from .base import HttpClient


class RlnClient(HttpClient):
    def init(self, password: str):
        return self.post("/init", {"password": password})

    def unlock_with_payload(self, payload: dict):
        return self.post("/unlock", payload)

    def unlock(self, cfg):
        return self.unlock_with_payload(
            {
                "password": cfg.password,
                "bitcoind_rpc_username": cfg.bitcoind_user,
                "bitcoind_rpc_password": cfg.bitcoind_password,
                "bitcoind_rpc_host": cfg.bitcoind_host,
                "bitcoind_rpc_port": cfg.bitcoind_port,
                "indexer_url": cfg.indexer_url,
                "proxy_endpoint": cfg.proxy_endpoint,
                "announce_addresses": [],
            },
        )

    def nodeinfo(self):
        return self.get("/nodeinfo")

    def address(self):
        return self.post("/address")["address"]

    def btcbalance(self):
        return self.post("/btcbalance", {"skip_sync": False})

    def createutxos(self, *, num: int = 10, size: int = 100000, fee_rate: int = 7):
        return self.post(
            "/createutxos",
            {
                "up_to": False,
                "num": num,
                "size": size,
                "fee_rate": fee_rate,
                "skip_sync": False,
            },
        )

    def issueassetnia(self):
        return self.post(
            "/issueassetnia",
            {
                "ticker": "USDT",
                "name": "Tether",
                "amounts": [1000],
                "precision": 0,
            },
        )

    def rgbinvoice_any(self):
        return self.post(
            "/rgbinvoice",
            {
                "min_confirmations": 1,
                "witness": False,
            },
        )

    def decodergbinvoice(self, invoice: str):
        return self.post("/decodergbinvoice", {"invoice": invoice})

    def assetbalance(self, asset_id: str):
        return self.post("/assetbalance", {"asset_id": asset_id})

    def sendrgb(self, payload: dict):
        return self.post("/sendrgb", payload)

    def refreshtransfers(self):
        return self.post("/refreshtransfers", {"skip_sync": False})

    def listtransfers(self, asset_id: str):
        return self.post("/listtransfers", {"asset_id": asset_id})

    def connectpeer(self, peer_uri: str):
        return self.post("/connectpeer", {"peer_pubkey_and_addr": peer_uri})

    def listchannels(self):
        return self.get("/listchannels")

    def listpayments(self):
        return self.get("/listpayments")

    def lninvoice(
        self,
        *,
        amt_msat: int,
        expiry_sec: int,
        asset_id: str | None = None,
        asset_amount: int | None = None,
    ):
        payload = {"amt_msat": amt_msat, "expiry_sec": expiry_sec}
        if asset_id is not None:
            payload["asset_id"] = asset_id
        if asset_amount is not None:
            payload["asset_amount"] = asset_amount
        return self.post("/lninvoice", payload)

    def decodelninvoice(self, invoice: str):
        return self.post("/decodelninvoice", {"invoice": invoice})

    def invoicestatus(self, invoice: str):
        return self.post("/invoicestatus", {"invoice": invoice})

    def sendpayment(self, invoice: str):
        return self.post("/sendpayment", {"invoice": invoice})
