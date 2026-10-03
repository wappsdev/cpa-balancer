"""Shared test constants and builders."""
from datetime import datetime, timedelta, timezone
from pathlib import Path

from cpa_balancer.config import Account, Config, Thresholds
from cpa_balancer.models import FABLE, MAIN, SESSION, AccountSnapshot, Reading

NOW = datetime(2026, 10, 3, 12, 0, tzinfo=timezone.utc)
W = "claude-w.json"  # no reserve
G = "claude-g.json"  # keeps a reserve
MAIN_MODEL = "claude-opus-5-5"
FABLE_MODEL = "claude-fable-5-1"
P = "Anthropic-Ratelimit-Unified-"


def make_config(tmp: Path, mode: str = "apply", enabled: bool = True) -> Config:
    return Config(
        enabled=enabled,
        mode=mode,
        base_url="http://cpa.test",
        keychain_service="test",
        auth_dir=tmp / "auth",
        accounts=(Account(W, False), Account(G, True)),
        main_models=(MAIN_MODEL,),
        fable_models=(FABLE_MODEL,),
        thresholds=Thresholds(),
        baseline={W: 10, G: 0},
        state_path=tmp / "state" / "state.json",
        log_path=tmp / "state" / "balancer.log",
    )


def reading(util: float, resets_in_h: float, age_min: float = 1, source: str = "probe") -> Reading:
    return Reading(util, NOW + timedelta(hours=resets_in_h), NOW - timedelta(minutes=age_min), source)


def snap(name, main=None, fable=None, session=None, cooling=(), unavailable=False) -> AccountSnapshot:
    readings = {b: r for b, r in ((MAIN, main), (FABLE, fable), (SESSION, session)) if r is not None}
    return AccountSnapshot(name, readings, frozenset(cooling), unavailable)


def _signals(window: str, util: float, reset_h: float) -> dict:
    reset = int((NOW + timedelta(hours=reset_h)).timestamp())
    return {f"{P}{window}-Utilization": str(util / 100), f"{P}{window}-Reset": str(reset)}


def cred(name, *, main=None, main_reset_h=100, fable=None, fable_reset_h=100,
         session=None, session_reset_h=3, age_min=1, cooldowns=(), disabled=False) -> dict:
    """A CPA /auth-files entry with captured rate-limit headers."""
    observed = (NOW - timedelta(minutes=age_min)).isoformat()
    model_quotas = {}
    if main is not None:
        signals = _signals("7d", main, main_reset_h)
        if session is not None:
            signals |= _signals("5h", session, session_reset_h)
        model_quotas[MAIN_MODEL] = {"observed_at": observed, "signals": signals}
    if fable is not None:
        model_quotas[FABLE_MODEL] = {"observed_at": observed, "signals": _signals("7d", fable, fable_reset_h)}
    return {
        "name": name,
        "model_quotas": model_quotas,
        "cooldowns": [{"model_key": m} for m in cooldowns],
        "disabled": disabled,
    }
