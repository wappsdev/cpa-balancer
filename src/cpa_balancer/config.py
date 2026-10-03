"""Config file loading."""
import tomllib
from dataclasses import dataclass
from pathlib import Path

DEFAULT_CONFIG = Path.home() / ".config" / "cpa-balancer" / "config.toml"
STATE_DIR = Path.home() / ".local" / "state" / "cpa-balancer"


class ConfigError(ValueError):
    pass


@dataclass(frozen=True)
class Account:
    auth_file: str
    reserve: bool


@dataclass(frozen=True)
class Thresholds:
    reserve_cap_far: float = 85
    reserve_cap_near: float = 90
    final_cap: float = 99
    session_cap: float = 98
    min_dwell_minutes: int = 60
    stale_hours: float = 6


@dataclass(frozen=True)
class Config:
    enabled: bool
    mode: str
    base_url: str
    keychain_service: str
    auth_dir: Path
    accounts: tuple[Account, ...]
    main_models: tuple[str, ...]
    fable_models: tuple[str, ...]
    thresholds: Thresholds
    baseline: dict[str, int]
    state_path: Path
    log_path: Path


def load_config(path: Path = DEFAULT_CONFIG) -> Config:
    data = tomllib.loads(path.read_text())
    mode = data.get("mode", "apply")
    if mode not in ("apply", "shadow"):
        raise ConfigError(f"mode must be 'apply' or 'shadow', got {mode!r}")
    accounts = tuple(Account(a["auth_file"], bool(a.get("reserve", False))) for a in data.get("accounts", []))
    if not accounts:
        raise ConfigError("at least one [[accounts]] entry is required")
    buckets = data.get("buckets", {})
    main_models = tuple(buckets.get("main", {}).get("models", []))
    if not main_models:
        raise ConfigError("[buckets.main] models must not be empty")
    baseline = {name: int(priority) for name, priority in data.get("baseline", {}).items()}
    missing = {a.auth_file for a in accounts} - baseline.keys()
    if missing:
        raise ConfigError(f"[baseline] is missing: {', '.join(sorted(missing))}")
    cpa = data.get("cpa", {})
    paths = data.get("paths", {})
    return Config(
        enabled=bool(data.get("enabled", True)),
        mode=mode,
        base_url=cpa.get("base_url", "http://127.0.0.1:8317").rstrip("/"),
        keychain_service=cpa.get("keychain_service", "CLIProxyAPI Management Key"),
        auth_dir=Path(cpa.get("auth_dir", "~/.cli-proxy-api")).expanduser(),
        accounts=accounts,
        main_models=main_models,
        fable_models=tuple(buckets.get("fable", {}).get("models", [])),
        thresholds=Thresholds(**data.get("thresholds", {})),
        baseline=baseline,
        state_path=Path(paths.get("state", STATE_DIR / "state.json")).expanduser(),
        log_path=Path(paths.get("log", STATE_DIR / "balancer.log")).expanduser(),
    )
