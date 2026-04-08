from __future__ import annotations

import pytest

from e2e.support import utils


def test_wait_until_retries_until_truthy(monkeypatch):
    state = {"now": 0, "calls": 0}

    def fake_time():
        return state["now"]

    def fake_sleep(seconds: int):
        state["now"] += seconds

    def eventually_true():
        state["calls"] += 1
        return state["calls"] >= 3

    monkeypatch.setattr(utils.time, "time", fake_time)
    monkeypatch.setattr(utils.time, "sleep", fake_sleep)

    assert utils.wait_until(eventually_true, timeout=5, interval=2, desc="eventually true") is True


def test_wait_until_reports_last_error(monkeypatch):
    state = {"now": 0}

    def fake_time():
        return state["now"]

    def fake_sleep(seconds: int):
        state["now"] += seconds

    def always_fails():
        raise ValueError("boom")

    monkeypatch.setattr(utils.time, "time", fake_time)
    monkeypatch.setattr(utils.time, "sleep", fake_sleep)

    with pytest.raises(AssertionError, match="last error: boom"):
        utils.wait_until(always_fails, timeout=3, interval=1, desc="failing condition")
