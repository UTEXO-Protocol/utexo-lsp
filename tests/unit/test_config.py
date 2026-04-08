from __future__ import annotations

from pathlib import Path

from e2e.support.config import E2EConfig


def test_config_reads_env_overrides(monkeypatch):
    monkeypatch.setenv("RGBLN_REPO", "/tmp/rgb")
    monkeypatch.setenv("UTEXO_LSP_REPO", "/tmp/utexo")
    monkeypatch.setenv("RGBLN_HOST", "10.0.0.1")
    monkeypatch.setenv("RGBLN_LSP_DAEMON_PORT", "4001")
    monkeypatch.setenv("RGBLN_ENABLE_VIRTUAL_CHANNELS_V0", "false")
    monkeypatch.setenv("UTEXO_DEFAULT_VIRTUAL_OPEN_MODE", "trusted_no_broadcast")

    cfg = E2EConfig()

    assert cfg.rgbln_repo == Path("/tmp/rgb")
    assert cfg.utexo_repo == Path("/tmp/utexo")
    assert cfg.daemon_host == "10.0.0.1"
    assert cfg.lsp_daemon_port == 4001
    assert cfg.enable_virtual_channels_v0 is False
    assert cfg.default_virtual_open_mode == "trusted_no_broadcast"


def test_config_computes_paths_and_urls(monkeypatch):
    monkeypatch.setenv("RGBLN_HOST", "127.0.0.9")
    monkeypatch.setenv("UTEXO_LSP_HOST", "127.0.0.8")
    monkeypatch.setenv("RGBLN_LSP_DAEMON_PORT", "3101")
    monkeypatch.setenv("UTEXO_LSP_PORT", "9080")
    monkeypatch.setenv("RGBLN_LSP_CONTAINER_NAME", "lsp-test")
    monkeypatch.setenv("RGBLN_FAUCET_CONTAINER_NAME", "faucet-test")

    cfg = E2EConfig()

    assert cfg.lsp_url == "http://127.0.0.9:3101"
    assert cfg.utexo_url == "http://127.0.0.8:9080"
    assert cfg.lsp_container_name == "lsp-test"
    assert cfg.faucet_container_name == "faucet-test"
