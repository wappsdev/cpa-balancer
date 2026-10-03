from pathlib import Path

import pytest

from cpa_balancer.config import ConfigError, load_config

VALID = """
mode = "apply"

[cpa]
auth_dir = "~/.cli-proxy-api"

[[accounts]]
auth_file = "claude-w.json"
reserve = false

[[accounts]]
auth_file = "claude-g.json"
reserve = true

[buckets.main]
models = ["claude-opus-5-5"]

[buckets.fable]
models = ["claude-fable-5-1"]

[thresholds]
session_cap = 98

[baseline]
"claude-w.json" = 10
"claude-g.json" = 0
"""


def write(tmp_path: Path, text: str) -> Path:
    path = tmp_path / "config.toml"
    path.write_text(text)
    return path


def test_loads_valid_config(tmp_path):
    cfg = load_config(write(tmp_path, VALID))
    assert cfg.enabled is True
    assert cfg.mode == "apply"
    assert [a.auth_file for a in cfg.accounts] == ["claude-w.json", "claude-g.json"]
    assert cfg.accounts[1].reserve is True
    assert cfg.thresholds.session_cap == 98
    assert cfg.thresholds.reserve_cap_far == 85
    assert cfg.thresholds.min_dwell_minutes == 60
    assert cfg.auth_dir == Path.home() / ".cli-proxy-api"
    assert cfg.base_url == "http://127.0.0.1:8317"
    assert cfg.main_models == ("claude-opus-5-5",)
    assert cfg.fable_models == ("claude-fable-5-1",)
    assert cfg.baseline == {"claude-w.json": 10, "claude-g.json": 0}


def test_rejects_unknown_mode(tmp_path):
    with pytest.raises(ConfigError, match="mode"):
        load_config(write(tmp_path, VALID.replace('mode = "apply"', 'mode = "yolo"')))


def test_requires_baseline_for_every_account(tmp_path):
    with pytest.raises(ConfigError, match="baseline"):
        load_config(write(tmp_path, VALID.replace('"claude-g.json" = 0', "")))


def test_requires_main_models(tmp_path):
    with pytest.raises(ConfigError, match="main"):
        load_config(write(tmp_path, VALID.replace('models = ["claude-opus-5-5"]', "models = []")))


def test_anchor_section_defaults_and_overrides(tmp_path):
    cfg = load_config(write(tmp_path, VALID))
    assert cfg.anchor.enabled is True
    assert cfg.anchor.model == "claude-haiku-4-5-20251001"
    assert cfg.anchor.fallback_model == "claude-sonnet-5-5"
    cfg = load_config(write(tmp_path, VALID + '\n[anchor]\nenabled = false\nmodel = "m"\n'))
    assert cfg.anchor.enabled is False
    assert cfg.anchor.model == "m"
