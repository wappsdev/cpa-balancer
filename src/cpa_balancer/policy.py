"""Pure decision logic: which Claude account each quota bucket should use."""
from datetime import datetime, timedelta

from .config import Account, Config, Thresholds
from .models import FABLE, MAIN, SESSION, AccountSnapshot, Choice, Decision

FAR = timedelta(hours=48)
NEAR = timedelta(hours=12)


def cap(reserve: bool, resets_at: datetime, now: datetime, th: Thresholds) -> float:
    """Highest weekly utilization (%) the account may reach in this bucket right now."""
    if not reserve:
        return th.final_cap
    left = resets_at - now
    if left >= FAR:
        return th.reserve_cap_far
    if left >= NEAR:
        return th.reserve_cap_near
    return th.final_cap


def eligibility(account: Account, snap: AccountSnapshot | None, bucket: str, bucket_models: tuple[str, ...],
                now: datetime, th: Thresholds, release_reserve: bool = False) -> tuple[bool, str]:
    if snap is None:
        return False, "not listed by CPA"
    stale = timedelta(hours=th.stale_hours)
    weekly = snap.readings.get(bucket)
    if weekly is None or now - weekly.observed_at > stale:
        return False, "no recent quota data"
    if snap.unavailable:
        return False, "disabled in CPA"
    if snap.cooling_models & set(bucket_models):
        return False, "cooling down in CPA"
    limit = th.final_cap if release_reserve else cap(account.reserve, weekly.resets_at, now, th)
    if weekly.util >= limit:
        return False, f"weekly {weekly.util:.0f}% >= cap {limit:.0f}%"
    session = snap.readings.get(SESSION)
    if session is not None and now - session.observed_at <= stale and session.util >= th.session_cap:
        return False, f"5h {session.util:.0f}% >= {th.session_cap:.0f}%"
    return True, f"weekly {weekly.util:.0f}% < cap {limit:.0f}%, resets {weekly.resets_at:%Y-%m-%d %H:%M} UTC"


def choose(bucket: str, bucket_models: tuple[str, ...], cfg: Config, snaps: dict[str, AccountSnapshot],
           now: datetime, current: str | None, active_since: datetime | None) -> Choice:
    th = cfg.thresholds
    for release in (False, True):
        notes: dict[str, str] = {}
        ranked: list[str] = []
        for account in cfg.accounts:
            ok, why = eligibility(account, snaps.get(account.auth_file), bucket, bucket_models, now, th, release)
            notes[account.auth_file] = why
            if ok:
                ranked.append(account.auth_file)
        if not ranked:
            continue
        ranked.sort(key=lambda a: (snaps[a].readings[bucket].resets_at, a != current))
        best = ranked[0]
        if current is None or current == best:
            return Choice(best, f"earliest reset; {notes[best]}", release)
        if current not in ranked:
            return Choice(best, f"forced switch from {current} ({notes.get(current, 'not configured')}); {notes[best]}",
                          release)
        if active_since is None or now - active_since >= timedelta(minutes=th.min_dwell_minutes):
            return Choice(best, f"preferred switch after dwell; {notes[best]}", release)
        return Choice(current, f"staying until the {th.min_dwell_minutes} min dwell passes; {notes[current]}", release)
    return Choice(None, "no eligible account even with reserves released; no restriction", True)


def _reset_key(snaps: dict[str, AccountSnapshot], account: str) -> tuple[int, float]:
    snap = snaps.get(account)
    reading = snap.readings.get(MAIN) if snap else None
    return (0, reading.resets_at.timestamp()) if reading else (1, 0.0)


def decide(snaps: dict[str, AccountSnapshot], cfg: Config, now: datetime,
           active: dict[str, tuple[str | None, datetime | None]], last_main_order: tuple[str, ...]) -> Decision:
    main = choose(MAIN, cfg.main_models, cfg, snaps, now, *active.get(MAIN, (None, None)))
    if cfg.fable_models:
        fable = choose(FABLE, cfg.fable_models, cfg, snaps, now, *active.get(FABLE, (None, None)))
    else:
        fable = Choice(None, "fable bucket not configured")
    names = [a.auth_file for a in cfg.accounts]
    if main.account is None:
        order = tuple(last_main_order) if sorted(last_main_order) == sorted(names) else tuple(names)
    else:
        rest = sorted((a for a in names if a != main.account), key=lambda a: _reset_key(snaps, a))
        order = (main.account, *rest)
    warnings = tuple(f"{bucket}: {c.reason}" for bucket, c in ((MAIN, main), (FABLE, fable)) if c.relaxed)
    return Decision(main, fable, order, warnings)
