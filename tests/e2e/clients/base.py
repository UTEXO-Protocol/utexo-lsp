from __future__ import annotations

import json
import urllib.error
import urllib.request


class HttpError(RuntimeError):
    def __init__(self, method: str, url: str, status: int, body: object):
        self.method = method
        self.url = url
        self.status = status
        self.body = body
        super().__init__(f"{method} {url} failed with status {status}: {body}")


class HttpClient:
    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")

    def _request(self, method: str, path: str, payload: dict | None = None, *, allow_error: bool = False):
        url = f"{self.base_url}{path}"
        body = None
        headers = {}
        if payload is not None:
            body = json.dumps(payload).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=body, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read().decode("utf-8")
                return resp.status, self._decode(raw)
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8")
            decoded = self._decode(raw)
            if allow_error:
                return exc.code, decoded
            raise HttpError(method, url, exc.code, decoded) from exc

    @staticmethod
    def _decode(raw: str):
        if not raw:
            return {}
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return raw

    def get(self, path: str):
        _, decoded = self._request("GET", path)
        return decoded

    def post(self, path: str, payload: dict | None = None):
        _, decoded = self._request("POST", path, payload)
        return decoded

    def post_allow_error(self, path: str, payload: dict | None = None):
        return self._request("POST", path, payload, allow_error=True)
