from __future__ import annotations

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
