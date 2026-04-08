from __future__ import annotations

import json
import time
from pathlib import Path


def wait_until(fn, *, timeout: int, interval: int = 2, desc: str = "condition"):
    deadline = time.time() + timeout
    last_error = None
    while time.time() < deadline:
        try:
            value = fn()
            if value:
                return value
        except Exception as exc:  # noqa: BLE001
            last_error = exc
        time.sleep(interval)
    if last_error is not None:
        raise AssertionError(f"timeout waiting for {desc}: last error: {last_error}") from last_error
    raise AssertionError(f"timeout waiting for {desc}")


def write_json(path: Path, payload):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True), encoding="utf-8")
