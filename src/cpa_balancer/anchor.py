"""Start an account's 5-hour window with a 1-token request sent with its own OAuth token."""
import json
import urllib.error
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path

from .models import Reading

MESSAGES_URL = "https://api.anthropic.com/v1/messages"
# Main models reject OAuth requests without this prompt (a bare 429); Haiku accepts them.
CLAUDE_CODE_SYSTEM = "You are Claude Code, Anthropic's official CLI for Claude."
WINDOW_PREFIX = "anthropic-ratelimit-unified-5h-"


@dataclass(frozen=True)
class AnchorResult:
    reading: Reading | None
    retry_after: datetime | None
    error: str | None


def _body(use_main_model: bool, model: str, fallback_model: str) -> dict:
    body = {"model": fallback_model if use_main_model else model, "max_tokens": 1,
            "messages": [{"role": "user", "content": "hi"}]}
    if use_main_model:
        body["system"] = [{"type": "text", "text": CLAUDE_CODE_SYSTEM}]
    return body


def _session_reading(headers, now: datetime) -> Reading | None:
    util = headers.get(f"{WINDOW_PREFIX}utilization")
    reset = headers.get(f"{WINDOW_PREFIX}reset")
    if util is None or reset is None:
        return None
    return Reading(float(util) * 100, datetime.fromtimestamp(int(reset), tz=timezone.utc), now, "anchor")


def anchor(auth_path: Path, now: datetime, *, use_main_model: bool, model: str, fallback_model: str,
           opener=urllib.request.urlopen) -> AnchorResult:
    token = json.loads(auth_path.read_text()).get("access_token")
    if not token:
        return AnchorResult(None, None, "no access_token in auth file")
    request = urllib.request.Request(
        MESSAGES_URL,
        data=json.dumps(_body(use_main_model, model, fallback_model)).encode(),
        headers={
            "Authorization": f"Bearer {token}",
            "anthropic-version": "2023-06-01",
            "anthropic-beta": "oauth-2025-04-20",
            "content-type": "application/json",
            "User-Agent": "cpa-balancer/0.1",
        },
    )
    try:
        with opener(request, timeout=60) as response:
            return AnchorResult(_session_reading(response.headers, now), None, None)
    except urllib.error.HTTPError as e:
        retry = e.headers.get("Retry-After") if e.headers else None
        until = now + timedelta(seconds=int(retry)) if retry and retry.isdigit() else None
        return AnchorResult(None, until, f"HTTP {e.code}")
    except (urllib.error.URLError, TimeoutError) as e:
        return AnchorResult(None, None, type(e).__name__)
