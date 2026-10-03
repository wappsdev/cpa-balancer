import json

from cpa_balancer.cli import format_status, main, run_cycle, set_disabled
from cpa_balancer.models import MAIN
from cpa_balancer.state import load_state
from cpa_balancer.usage_probe import ProbeResult
from tests.helpers import G, NOW, W, cred, make_config, reading


class FakeClient:
    """Stands in for CPA: serves credentials and writes patches into the auth files like CPA does."""

    def __init__(self, cfg, creds):
        self.cfg, self.creds, self.patched = cfg, creds, []

    def credentials(self):
        return self.creds

    def patch_fields(self, auth_file, fields):
        self.patched.append((auth_file, fields))
        path = self.cfg.auth_dir / auth_file
        data = json.loads(path.read_text())
        for key, value in fields.items():
            if value is None:
                data.pop(key, None)
            else:
                data[key] = value
        path.write_text(json.dumps(data))


def setup(tmp_path, mode="apply", enabled=True):
    cfg = make_config(tmp_path, mode=mode, enabled=enabled)
    cfg.auth_dir.mkdir(parents=True)
    (cfg.auth_dir / W).write_text(json.dumps({"access_token": "tok-w", "priority": 10}))
    (cfg.auth_dir / G).write_text(json.dumps({"access_token": "tok-g"}))
    return cfg


def no_probe(path, now):
    raise AssertionError(f"unexpected probe of {path.name}")


FRESH = [cred(W, main=10, main_reset_h=140, fable=1, fable_reset_h=140),
         cred(G, main=52, main_reset_h=90, fable=0, fable_reset_h=90)]


def test_apply_mode_writes_priorities_and_is_idempotent(tmp_path):
    cfg = setup(tmp_path)
    client = FakeClient(cfg, FRESH)
    result = run_cycle(cfg, client, NOW, prober=no_probe)
    assert result.decision.main.account == G
    assert client.patched == [(W, {"priority": 90}), (G, {"priority": 100})]
    assert load_state(cfg.state_path).active[MAIN] == (G, NOW)
    client.patched.clear()
    run_cycle(cfg, client, NOW, prober=no_probe)
    assert client.patched == []


def test_shadow_mode_decides_but_never_writes(tmp_path):
    cfg = setup(tmp_path, mode="shadow")
    client = FakeClient(cfg, FRESH)
    result = run_cycle(cfg, client, NOW, prober=no_probe)
    assert result.decision.main.account == G
    assert result.patches and result.applied is False
    assert client.patched == []


def test_stale_account_is_probed_and_probe_data_used(tmp_path):
    cfg = setup(tmp_path)
    creds = [cred(W, main=10, main_reset_h=140, fable=1, fable_reset_h=140),
             cred(G, main=52, main_reset_h=90, fable=0, fable_reset_h=90, age_min=120)]
    probed = []

    def prober(path, now):
        probed.append(path.name)
        return ProbeResult({MAIN: reading(86, 90, age_min=0)}, None, None)

    result = run_cycle(cfg, FakeClient(cfg, creds), NOW, prober=prober)
    assert probed == [G]
    assert result.decision.main.account == W          # fresh probe shows G past its 85 % reserve cap
    assert load_state(cfg.state_path).probes[G].last_probe == NOW


def test_missing_weekly_bucket_triggers_probe_even_with_fresh_session_data(tmp_path):
    cfg = setup(tmp_path)
    w = cred(W, main=10, main_reset_h=140, fable=1, fable_reset_h=140)
    g = cred(G)                                   # only a fresh credential-level 5h reading, no weekly data
    g["quota"] = {"observed_at": NOW.isoformat(),
                  "signals": {"Anthropic-Ratelimit-Unified-5h-Utilization": "0.0",
                              "Anthropic-Ratelimit-Unified-5h-Reset": str(int(NOW.timestamp()) + 3600)}}
    probed = []

    def prober(path, now):
        probed.append(path.name)
        return ProbeResult({MAIN: reading(52, 90, age_min=0), "fable": reading(0, 90, age_min=0)}, None, None)

    result = run_cycle(cfg, FakeClient(cfg, [w, g]), NOW, prober=prober)
    assert probed == [G]
    assert result.decision.main.account == G      # resets sooner than W once its data is known


def test_dry_run_neither_probes_nor_saves(tmp_path):
    cfg = setup(tmp_path)
    creds = [cred(W, main=10, main_reset_h=140, age_min=120), cred(G, main=52, main_reset_h=90)]
    run_cycle(cfg, FakeClient(cfg, creds), NOW, dry_run=True, prober=no_probe)
    assert not cfg.state_path.exists()


def test_disable_restores_baseline_and_stops_deciding(tmp_path):
    cfg = setup(tmp_path)
    client = FakeClient(cfg, FRESH)
    run_cycle(cfg, client, NOW, prober=no_probe)
    client.patched.clear()
    set_disabled(cfg, client, True)
    assert client.patched == [(W, {"priority": 10}), (G, {"priority": 0})]
    client.patched.clear()
    result = run_cycle(cfg, client, NOW, prober=no_probe)
    assert result.decision is None and client.patched == []
    set_disabled(cfg, client, False)
    assert run_cycle(cfg, client, NOW, prober=no_probe).decision.main.account == G


def test_status_explains_choice(tmp_path):
    cfg = setup(tmp_path)
    result = run_cycle(cfg, FakeClient(cfg, FRESH), NOW, dry_run=True, prober=no_probe)
    text = format_status(cfg, result, NOW)
    assert f"main  -> {G}" in text
    assert "earliest reset" in text
    assert f"{W} (reserve=no)" in text


def test_missing_auth_file_skips_that_account_instead_of_crashing(tmp_path, caplog):
    cfg = setup(tmp_path)
    (cfg.auth_dir / G).unlink()
    client = FakeClient(cfg, FRESH)
    with caplog.at_level("ERROR", logger="cpa_balancer"):
        result = run_cycle(cfg, client, NOW, prober=no_probe)
    assert result.decision.main.account == W
    assert client.patched == [(W, {"priority": 100})]
    assert any(G in r.getMessage() for r in caplog.records)


def test_config_errors_exit_with_a_one_line_message(tmp_path, capsys):
    assert main(["--config", str(tmp_path / "missing.toml"), "status"]) == 2
    bad = tmp_path / "bad.toml"
    bad.write_text("mode = [")
    assert main(["--config", str(bad), "status"]) == 2
    err = capsys.readouterr().err
    assert err.count("cpa-balancer: config error:") == 2
    assert "Traceback" not in err
