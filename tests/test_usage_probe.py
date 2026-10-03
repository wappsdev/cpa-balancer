import email.message
import io
import json
import urllib.error
from contextlib import nullcontext
from datetime import timedelta

from cpa_balancer.models import FABLE, MAIN, SESSION
from cpa_balancer.usage_probe import due, parse_usage, probe
from tests.helpers import NOW

BODY = {
    "limits": [
        {"kind": "session", "percent": 8, "resets_at": "2026-10-03T17:59:59+00:00", "scope": None},
        {"kind": "weekly_all", "percent": 58, "resets_at": "2026-10-09T00:59:59+00:00", "scope": None},
        {"kind": "weekly_scoped", "percent": 1, "resets_at": "2026-10-09T00:59:59+00:00",
         "scope": {"model": {"id": None, "display_name": "Fable"}, "surface": None}},
        {"kind": "weekly_scoped", "percent": 40, "resets_at": "2026-10-09T00:59:59+00:00",
         "scope": {"model": {"id": None, "display_name": "Something Else"}}},
    ]
}


def test_parse_usage_maps_limits_to_buckets():
    out = parse_usage(BODY, NOW)
    assert out[SESSION].util == 8
    assert out[MAIN].util == 58
    assert out[FABLE].util == 1
    assert out[MAIN].observed_at == NOW
    assert out[MAIN].source == "probe"
    assert len(out) == 3


def test_due_rules():
    base = dict(now=NOW, last_probe=None, retry_after=None, reset_estimated=False, switching_to=False)
    fresh, stale = NOW - timedelta(minutes=5), NOW - timedelta(minutes=61)
    assert due(**base, newest_observed=fresh) is False
    assert due(**base, newest_observed=stale) is True
    assert due(**base, newest_observed=None) is True
    assert due(**(base | {"retry_after": NOW + timedelta(minutes=1)}), newest_observed=stale) is False
    assert due(**(base | {"last_probe": NOW - timedelta(minutes=9)}), newest_observed=stale) is False
    assert due(**(base | {"last_probe": NOW - timedelta(minutes=30)}), newest_observed=stale) is False
    assert due(**(base | {"last_probe": NOW - timedelta(minutes=61)}), newest_observed=stale) is True
    assert due(**(base | {"switching_to": True}), newest_observed=NOW - timedelta(minutes=11)) is True
    assert due(**(base | {"switching_to": True}), newest_observed=NOW - timedelta(minutes=9)) is False
    assert due(**(base | {"reset_estimated": True}), newest_observed=fresh) is True


def _auth(tmp_path, token="secret-token-123"):
    path = tmp_path / "claude-w.json"
    path.write_text(json.dumps({"access_token": token}))
    return path


def test_probe_success_sends_bearer_and_parses(tmp_path):
    seen = {}

    def opener(req, timeout):
        seen["auth"] = req.get_header("Authorization")
        return nullcontext(io.BytesIO(json.dumps(BODY).encode()))

    result = probe(_auth(tmp_path), NOW, opener)
    assert seen["auth"] == "Bearer secret-token-123"
    assert result.error is None
    assert result.readings[MAIN].util == 58


def test_probe_429_records_retry_after_without_leaking_token(tmp_path):
    def opener(req, timeout):
        headers = email.message.Message()
        headers["Retry-After"] = "300"
        raise urllib.error.HTTPError(req.full_url, 429, "Too Many Requests", headers, io.BytesIO(b"{}"))

    result = probe(_auth(tmp_path), NOW, opener)
    assert result.retry_after == NOW + timedelta(seconds=300)
    assert result.error == "HTTP 429"
    assert result.readings == {}
    assert "secret-token-123" not in repr(result)


def test_probe_without_token(tmp_path):
    result = probe(_auth(tmp_path, token=""), NOW, lambda req, timeout: None)
    assert result.error == "no access_token in auth file"


def test_parse_usage_reads_a_closed_session_window():
    body = {"limits": [{"kind": "session", "percent": 0, "resets_at": None, "is_active": False}]}
    r = parse_usage(body, NOW)[SESSION]
    assert r.util == 0
    assert r.resets_at is None
    assert r.source == "probe"
