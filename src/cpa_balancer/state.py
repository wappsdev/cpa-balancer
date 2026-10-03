"""Persistent state between runs (JSON)."""
import json
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path

from .models import Reading


@dataclass
class ProbeState:
    last_probe: datetime | None = None
    retry_after: datetime | None = None
    readings: dict[str, Reading] = field(default_factory=dict)
    last_anchor: datetime | None = None
    anchor_retry_after: datetime | None = None
    anchor_main_model: bool = False
    anchor_reading: Reading | None = None


@dataclass
class State:
    active: dict[str, tuple[str | None, datetime | None]] = field(default_factory=dict)
    probes: dict[str, ProbeState] = field(default_factory=dict)
    last_main_order: tuple[str, ...] = ()
    disabled: bool = False


def _dt(value: str | None) -> datetime | None:
    return datetime.fromisoformat(value) if value else None


def _iso(value: datetime | None) -> str | None:
    return value.isoformat() if value else None


def _reading_to_json(r: Reading) -> dict:
    return {"util": r.util, "resets_at": _iso(r.resets_at), "observed_at": _iso(r.observed_at), "source": r.source}


def _reading_from_json(raw: dict) -> Reading:
    return Reading(raw["util"], _dt(raw["resets_at"]), _dt(raw["observed_at"]), raw["source"])


def load_state(path: Path) -> State:
    if not path.exists():
        return State()
    raw = json.loads(path.read_text())
    return State(
        active={b: (v["account"], _dt(v["since"])) for b, v in raw.get("active", {}).items()},
        probes={
            account: ProbeState(
                last_probe=_dt(p.get("last_probe")),
                retry_after=_dt(p.get("retry_after")),
                readings={b: _reading_from_json(r) for b, r in p.get("readings", {}).items()},
                last_anchor=_dt(p.get("last_anchor")),
                anchor_retry_after=_dt(p.get("anchor_retry_after")),
                anchor_main_model=bool(p.get("anchor_main_model", False)),
                anchor_reading=_reading_from_json(p["anchor_reading"]) if p.get("anchor_reading") else None,
            )
            for account, p in raw.get("probes", {}).items()
        },
        last_main_order=tuple(raw.get("last_main_order", [])),
        disabled=bool(raw.get("disabled", False)),
    )


def save_state(state: State, path: Path) -> None:
    raw = {
        "active": {b: {"account": a, "since": _iso(s)} for b, (a, s) in state.active.items()},
        "probes": {
            account: {
                "last_probe": _iso(p.last_probe),
                "retry_after": _iso(p.retry_after),
                "readings": {b: _reading_to_json(r) for b, r in p.readings.items()},
                "last_anchor": _iso(p.last_anchor),
                "anchor_retry_after": _iso(p.anchor_retry_after),
                "anchor_main_model": p.anchor_main_model,
                "anchor_reading": _reading_to_json(p.anchor_reading) if p.anchor_reading else None,
            }
            for account, p in state.probes.items()
        },
        "last_main_order": list(state.last_main_order),
        "disabled": state.disabled,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(raw, indent=2))
    tmp.replace(path)
