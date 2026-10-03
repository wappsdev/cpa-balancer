import io
import json
import urllib.error
from contextlib import nullcontext

import pytest

from cpa_balancer.cpa_client import CpaClient, CpaError


def test_credentials_and_patch_requests():
    calls = []

    def opener(req, timeout):
        calls.append((req.get_method(), req.full_url, req.get_header("Authorization"),
                      json.loads(req.data) if req.data else None))
        body = {"files": [{"name": "claude-w.json"}]} if req.get_method() == "GET" else {"status": "ok"}
        return nullcontext(io.BytesIO(json.dumps(body).encode()))

    client = CpaClient("http://cpa.test", "mgmt-key", opener)
    assert client.credentials() == [{"name": "claude-w.json"}]
    client.patch_fields("claude-w.json", {"priority": 100})
    assert calls[0][:3] == ("GET", "http://cpa.test/v0/management/auth-files", "Bearer mgmt-key")
    assert calls[1] == ("PATCH", "http://cpa.test/v0/management/auth-files/fields", "Bearer mgmt-key",
                        {"name": "claude-w.json", "priority": 100})


def test_errors_become_cpa_error_without_key():
    def opener(req, timeout):
        raise urllib.error.URLError("connection refused")

    with pytest.raises(CpaError) as err:
        CpaClient("http://cpa.test", "mgmt-key", opener).credentials()
    assert "mgmt-key" not in str(err.value)
