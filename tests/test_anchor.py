import email.message
import io
import json
import urllib.error
from datetime import timedelta

from cpa_balancer.anchor import CLAUDE_CODE_SYSTEM, anchor
from tests.helpers import NOW

RESET = int((NOW + timedelta(hours=5)).timestamp())


class FakeResponse:
    def __init__(self, headers):
        self.headers = headers

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def _headers(pairs: dict) -> email.message.Message:
    message = email.message.Message()
    for key, value in pairs.items():
        message[key] = value
    return message


def _auth(tmp_path, token="secret-token-123"):
    path = tmp_path / "claude-w.json"
    path.write_text(json.dumps({"access_token": token}))
    return path


WINDOW = {"anthropic-ratelimit-unified-5h-utilization": "0.0", "anthropic-ratelimit-unified-5h-reset": str(RESET)}


def test_haiku_anchor_sends_a_one_token_request_and_reads_the_window(tmp_path):
    seen = {}

    def opener(req, timeout):
        seen.update(url=req.full_url, auth=req.get_header("Authorization"),
                    beta=req.get_header("Anthropic-beta"), body=json.loads(req.data))
        return FakeResponse(_headers(WINDOW))

    result = anchor(_auth(tmp_path), NOW, use_main_model=False, model="haiku-x", fallback_model="sonnet-x",
                    opener=opener)
    assert seen["url"] == "https://api.anthropic.com/v1/messages"
    assert seen["auth"] == "Bearer secret-token-123"
    assert seen["beta"] == "oauth-2025-04-20"
    assert seen["body"] == {"model": "haiku-x", "max_tokens": 1, "messages": [{"role": "user", "content": "hi"}]}
    assert result.error is None
    assert result.reading.util == 0
    assert result.reading.resets_at == NOW + timedelta(hours=5)
    assert result.reading.source == "anchor"


def test_main_model_anchor_uses_fallback_model_and_claude_code_system_prompt(tmp_path):
    seen = {}

    def opener(req, timeout):
        seen["body"] = json.loads(req.data)
        return FakeResponse(_headers(WINDOW))

    anchor(_auth(tmp_path), NOW, use_main_model=True, model="haiku-x", fallback_model="sonnet-x", opener=opener)
    assert seen["body"]["model"] == "sonnet-x"
    assert seen["body"]["system"] == [{"type": "text", "text": CLAUDE_CODE_SYSTEM}]


def test_missing_window_headers_count_as_sent_without_reading(tmp_path):
    result = anchor(_auth(tmp_path), NOW, use_main_model=False, model="h", fallback_model="s",
                    opener=lambda req, timeout: FakeResponse(_headers({})))
    assert result.error is None
    assert result.reading is None


def test_429_records_retry_after_without_leaking_token(tmp_path):
    def opener(req, timeout):
        raise urllib.error.HTTPError(req.full_url, 429, "Too Many Requests", _headers({"Retry-After": "120"}),
                                     io.BytesIO(b"{}"))

    result = anchor(_auth(tmp_path), NOW, use_main_model=False, model="h", fallback_model="s", opener=opener)
    assert result.retry_after == NOW + timedelta(seconds=120)
    assert result.error == "HTTP 429"
    assert "secret-token-123" not in repr(result)
