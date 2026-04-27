from __future__ import annotations

from e2e.support.flows import run_lightning_receive_flow


def test_lightning_receive(env):
    run_lightning_receive_flow(env)
