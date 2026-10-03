"""CLIProxyAPI Management API client."""
import json
import subprocess
import urllib.error
import urllib.request


class CpaError(RuntimeError):
    pass


def management_key(service: str) -> str:
    result = subprocess.run(["security", "find-generic-password", "-s", service, "-w"],
                            capture_output=True, text=True)
    if result.returncode != 0 or not result.stdout.strip():
        raise CpaError(f"management key not found in Keychain service {service!r}")
    return result.stdout.strip()


class CpaClient:
    def __init__(self, base_url: str, key: str, opener=urllib.request.urlopen):
        self._base = f"{base_url.rstrip('/')}/v0/management"
        self._key = key
        self._opener = opener

    def _call(self, method: str, path: str, body: dict | None = None) -> dict:
        request = urllib.request.Request(
            f"{self._base}{path}",
            method=method,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Authorization": f"Bearer {self._key}", "Content-Type": "application/json"},
        )
        try:
            with self._opener(request, timeout=15) as response:
                text = response.read()
        except (urllib.error.URLError, TimeoutError) as e:
            raise CpaError(f"{method} {path}: {e}") from None
        return json.loads(text) if text else {}

    def credentials(self) -> list[dict]:
        return self._call("GET", "/auth-files").get("files", [])

    def patch_fields(self, auth_file: str, fields: dict) -> None:
        self._call("PATCH", "/auth-files/fields", {"name": auth_file, **fields})
