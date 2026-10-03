"""Anthropic OAuth usage endpoint: rare, rate-limited quota probes."""
import json
import urllib.error
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path

from .models import FABLE, MAIN, SESSION, Reading

USAGE_URL = "https://api.anthropic.com/api/oauth/usage"
MIN_INTERVAL = timedelta(minutes=10)
STALE_AFTER = timedelta(minutes=60)
SWITCH_FRESHNESS = timedelta(minutes=10)
KIND_TO_BUCKET = {"session": SESSION, "weekly_all": MAIN}


@dataclass(frozen=True)
class ProbeResult:
    readings: dict[str, Reading]
    retry_after: datetime | None
    error: str | None


def parse_usage(body: dict, now: datetime) -> dict[str, Reading]:
    out: dict[str, Reading] = {}
    for limit in body.get("limits") or []:
        kind = limit.get("kind")
        model = ((limit.get("scope") or {}).get("model") or {}).get("display_name") or ""
        bucket = FABLE if kind == "weekly_scoped" and model.lower() == "fable" else KIND_TO_BUCKET.get(kind)
        resets = limit.get("resets_at")
        if bucket is None or limit.get("percent") is None or (not resets and bucket != SESSION):
            continue
        out[bucket] = Reading(
            util=float(limit["percent"]),
            resets_at=datetime.fromisoformat(resets) if resets else None,  # None: no running session window
            observed_at=now,
            source="probe",
        )
    return out


def due(*, now: datetime, last_probe: datetime | None, retry_after: datetime | None,
        newest_observed: datetime | None, reset_estimated: bool, switching_to: bool) -> bool:
    if retry_after is not None and now < retry_after:
        return False
    if last_probe is not None and now - last_probe < MIN_INTERVAL:
        return False
    age = None if newest_observed is None else now - newest_observed
    if switching_to:
        return age is None or age > SWITCH_FRESHNESS
    if reset_estimated:
        return True
    stale = age is None or age > STALE_AFTER
    probed_recently = last_probe is not None and now - last_probe < STALE_AFTER
    return stale and not probed_recently


def probe(auth_path: Path, now: datetime, opener=urllib.request.urlopen) -> ProbeResult:
    token = json.loads(auth_path.read_text()).get("access_token")
    if not token:
        return ProbeResult({}, None, "no access_token in auth file")
    request = urllib.request.Request(USAGE_URL, headers={
        "Authorization": f"Bearer {token}",
        "anthropic-beta": "oauth-2025-04-20",
        "Accept": "application/json",
        "User-Agent": "cpa-balancer/0.1",
    })
    try:
        with opener(request, timeout=20) as response:
            return ProbeResult(parse_usage(json.load(response), now), None, None)
    except urllib.error.HTTPError as e:
        retry = e.headers.get("Retry-After") if e.headers else None
        until = now + timedelta(seconds=int(retry)) if retry and retry.isdigit() else None
        return ProbeResult({}, until, f"HTTP {e.code}")
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as e:
        return ProbeResult({}, None, type(e).__name__)
