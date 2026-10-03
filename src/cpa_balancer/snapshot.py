"""Turn CPA header data and probe data into per-bucket readings."""
from collections.abc import Iterable
from datetime import datetime, timedelta, timezone

from .models import FABLE, MAIN, SESSION, AccountSnapshot, Reading

PREFIX = "Anthropic-Ratelimit-Unified-"
PERIODS = {MAIN: timedelta(days=7), FABLE: timedelta(days=7)}


def _reading(signals: dict, window: str, observed_at: datetime) -> Reading | None:
    util = signals.get(f"{PREFIX}{window}-Utilization")
    reset = signals.get(f"{PREFIX}{window}-Reset")
    if util is None or reset is None:
        return None
    return Reading(
        util=float(util) * 100,
        resets_at=datetime.fromtimestamp(int(reset), tz=timezone.utc),
        observed_at=observed_at,
        source="header",
    )


def _newest(readings: Iterable[Reading | None]) -> Reading | None:
    return max((r for r in readings if r is not None), key=lambda r: r.observed_at, default=None)


def header_readings(credential: dict, main_models, fable_models) -> dict[str, Reading]:
    # The credential-level entry mirrors whichever model answered last, so weekly buckets
    # are read only from their own models; the 5-hour window is shared by all of them.
    entries: list[tuple[str | None, dict, datetime]] = []
    quota = credential.get("quota") or {}
    if quota.get("observed_at"):
        entries.append((None, quota.get("signals") or {}, datetime.fromisoformat(quota["observed_at"])))
    for model, entry in (credential.get("model_quotas") or {}).items():
        if entry.get("observed_at"):
            entries.append((model, entry.get("signals") or {}, datetime.fromisoformat(entry["observed_at"])))
    found = {
        MAIN: _newest(_reading(s, "7d", t) for m, s, t in entries if m in main_models),
        FABLE: _newest(_reading(s, "7d", t) for m, s, t in entries if m in fable_models),
        SESSION: _newest(_reading(s, "5h", t) for _, s, t in entries),
    }
    return {bucket: r for bucket, r in found.items() if r is not None}


def merge(*sources: dict[str, Reading]) -> dict[str, Reading]:
    out: dict[str, Reading] = {}
    for source in sources:
        for bucket, r in source.items():
            if bucket not in out or r.observed_at > out[bucket].observed_at:
                out[bucket] = r
    return out


def roll_forward(readings: dict[str, Reading], now: datetime) -> dict[str, Reading]:
    """Weekly windows run on a fixed schedule; a 5-hour window only runs after use."""
    out: dict[str, Reading] = {}
    for bucket, r in readings.items():
        if r.resets_at is None or r.resets_at > now:
            out[bucket] = r
        elif bucket == SESSION:
            out[bucket] = Reading(util=0.0, resets_at=None, observed_at=now, source="estimate")
        else:
            resets_at = r.resets_at
            while resets_at <= now:
                resets_at += PERIODS[bucket]
            out[bucket] = Reading(util=0.0, resets_at=resets_at, observed_at=now, source="estimate")
    return out


def build_snapshot(credential: dict, probe_readings: dict[str, Reading], main_models, fable_models,
                   now: datetime) -> AccountSnapshot:
    readings = merge(header_readings(credential, main_models, fable_models), probe_readings)
    return AccountSnapshot(
        auth_file=credential["name"],
        readings=roll_forward(readings, now),
        cooling_models=frozenset(c.get("model_key", "") for c in credential.get("cooldowns") or []),
        unavailable=bool(credential.get("disabled")),
    )
