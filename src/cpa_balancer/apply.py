"""Translate a Decision into CPA credential field patches."""
from dataclasses import dataclass

from .models import Decision

TOP_PRIORITY = 100
STEP = 10


@dataclass(frozen=True)
class Patch:
    auth_file: str
    fields: dict


def _without_fable(excluded: list[str] | None, fable_models) -> list[str]:
    return [m for m in (excluded or []) if m not in fable_models]


def _diff(auth_file: str, target: dict, current: dict) -> Patch | None:
    changed = {}
    if current.get("priority") != target["priority"]:
        changed["priority"] = target["priority"]
    if sorted(current.get("excluded_models") or []) != sorted(target["excluded_models"] or []):
        changed["excluded_models"] = target["excluded_models"]
    return Patch(auth_file, changed) if changed else None


def patches(decision: Decision, managed: list[str], fable_models, current: dict[str, dict]) -> list[Patch]:
    priorities = {a: TOP_PRIORITY - STEP * i for i, a in enumerate(decision.main_order)}
    fable_to = decision.fable.account
    exclude_on = set() if fable_to is None or fable_to == decision.main_order[0] else set(managed) - {fable_to}
    out = []
    for account in managed:
        now = current.get(account, {})
        keep = _without_fable(now.get("excluded_models"), fable_models)
        excluded = keep + list(fable_models) if account in exclude_on else keep
        patch = _diff(account, {"priority": priorities[account], "excluded_models": excluded or None}, now)
        if patch:
            out.append(patch)
    return out


def baseline_patches(baseline: dict[str, int], fable_models, current: dict[str, dict]) -> list[Patch]:
    out = []
    for account, priority in baseline.items():
        now = current.get(account, {})
        keep = _without_fable(now.get("excluded_models"), fable_models)
        patch = _diff(account, {"priority": priority, "excluded_models": keep or None}, now)
        if patch:
            out.append(patch)
    return out
