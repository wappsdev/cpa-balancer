"""Shared value types."""
from dataclasses import dataclass
from datetime import datetime

MAIN = "main"
FABLE = "fable"
SESSION = "session"


@dataclass(frozen=True)
class Reading:
    """One quota observation: utilization in percent, window reset time, when it was seen."""

    util: float
    resets_at: datetime
    observed_at: datetime
    source: str  # "header" | "probe" | "estimate"


@dataclass(frozen=True)
class AccountSnapshot:
    auth_file: str
    readings: dict[str, Reading]
    cooling_models: frozenset[str]
    unavailable: bool


@dataclass(frozen=True)
class Choice:
    account: str | None  # None = no restriction for this bucket
    reason: str
    relaxed: bool = False  # reserves had to be released, or nothing was eligible


@dataclass(frozen=True)
class Decision:
    main: Choice
    fable: Choice
    main_order: tuple[str, ...]
    warnings: tuple[str, ...]
