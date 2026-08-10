from __future__ import annotations

import json
import urllib.request
from .base import HttpClient


class LspApiClient(HttpClient):
    def get_info(self):
        return self.get("/get_info")

    def lightning_receive(self, *, ln_invoice: str, asset_id: str):
        return self.post(
            "/lightning_receive",
            {
                "ln_invoice": ln_invoice,
                "rgb_invoice": {
                    "asset_id": asset_id,
                    "assignment": "Value",
                    "min_confirmations": 1,
                    "witness": False,
                },
            },
        )

    def set_lightning_address(self, *, pubkey: str, username: str, signature: str):
        return self.post(
            "/lightning_address",
            {
                "pubkey": pubkey,
                "username": username,
                "signature": signature,
            },
        )

    def get_lightning_address_by_pubkey(self, pubkey: str):
        return self.get(f"/lightning_address/by_pubkey/{pubkey}")

    def lnurlp_discovery(self, username: str):
        return self.get(f"/.well-known/lnurlp/{username}")

    def lnurlp_callback(self, username: str, amount_msat: int):
        # The query parameter can be passed as path/query
        return self.get(f"/pay/callback/{username}?amount={amount_msat}")

    def post_internal_async_order_new(self, payload: dict):
        url = f"{self.base_url}/internal/async_order/new"
        body = json.dumps(payload).encode("utf-8")
        headers = {
            "Content-Type": "application/json",
            "Authorization": "Bearer testtoken",
        }
        req = urllib.request.Request(url, data=body, method="POST", headers=headers)
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read().decode("utf-8")
            return self._decode(raw)
