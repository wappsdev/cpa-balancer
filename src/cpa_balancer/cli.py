"""Decision cycle and command-line entry point."""
import argparse
import fcntl
import json
import logging
import sys
import tomllib
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from logging.handlers import RotatingFileHandler
from pathlib import Path

from .apply import Patch, baseline_patches, patches
from .config import DEFAULT_CONFIG, Config, ConfigError, load_config
from .cpa_client import CpaClient, CpaError, management_key
from .models import FABLE, MAIN, SESSION, AccountSnapshot, Decision
from .anchor import anchor
from .policy import anchor_did_not_start_window, anchor_targets, decide
from .snapshot import build_snapshot, merge
from .state import ProbeState, State, load_state, save_state
from .usage_probe import due, probe

log = logging.getLogger("cpa_balancer")


@dataclass(frozen=True)
class RunResult:
    decision: Decision | None
    snapshots: dict[str, AccountSnapshot]
    patches: list[Patch]
    applied: bool


def _present(cfg: Config) -> Config:
    """Drop accounts whose auth file is gone (e.g. re-login renamed it) so one cycle cannot crash."""
    missing = {a.auth_file for a in cfg.accounts if not (cfg.auth_dir / a.auth_file).exists()}
    if not missing:
        return cfg
    log.error("auth file missing, account skipped: %s", ", ".join(sorted(missing)))
    return replace(cfg, accounts=tuple(a for a in cfg.accounts if a.auth_file not in missing),
                   baseline={k: v for k, v in cfg.baseline.items() if k not in missing})


def _disk_fields(cfg: Config) -> dict[str, dict]:
    # CPA rewrites the auth file on every PATCH, so the file is the source of truth.
    out = {}
    for account in cfg.accounts:
        data = json.loads((cfg.auth_dir / account.auth_file).read_text())
        out[account.auth_file] = {"priority": data.get("priority"), "excluded_models": data.get("excluded_models")}
    return out


def _write(client, todo: list[Patch], cfg: Config, dry_run: bool) -> bool:
    if not todo:
        return False
    if dry_run or cfg.mode == "shadow":
        for p in todo:
            log.info("would patch %s %s", p.auth_file, p.fields)
        return False
    for p in todo:
        client.patch_fields(p.auth_file, p.fields)
        log.info("patched %s %s", p.auth_file, p.fields)
    return True


def _anchor(cfg: Config, state: State, decision: Decision, snaps: dict[str, AccountSnapshot], now: datetime,
            dry_run: bool, anchorer) -> None:
    targets = anchor_targets(decision, snaps, cfg, now,
                             {a: p.last_anchor for a, p in state.probes.items()},
                             {a: p.anchor_retry_after for a, p in state.probes.items()})
    for account in targets:
        if dry_run or cfg.mode == "shadow":
            log.info("would anchor %s", account)
            continue
        ps = state.probes.setdefault(account, ProbeState())
        model = cfg.anchor.fallback_model if ps.anchor_main_model else cfg.anchor.model
        result = anchorer(cfg.auth_dir / account, now, use_main_model=ps.anchor_main_model,
                          model=cfg.anchor.model, fallback_model=cfg.anchor.fallback_model)
        ps.last_anchor = now
        ps.anchor_retry_after = result.retry_after
        if result.error:
            log.warning("anchor %s failed: %s", account, result.error)
            continue
        if result.reading is not None:
            ps.anchor_reading = result.reading
        window = (f"resets {result.reading.resets_at:%H:%M} UTC"
                  if result.reading and result.reading.resets_at else "no window header")
        log.info("anchored %s with %s (%s)", account, model, window)


def run_cycle(cfg: Config, client, now: datetime, *, dry_run: bool = False, prober=probe,
              anchorer=anchor) -> RunResult:
    cfg = _present(cfg)
    state = load_state(cfg.state_path)
    managed = [a.auth_file for a in cfg.accounts]
    disk = _disk_fields(cfg)
    if not cfg.enabled or state.disabled:
        todo = baseline_patches(cfg.baseline, cfg.fable_models, disk)
        return RunResult(None, {}, todo, _write(client, todo, cfg, dry_run))

    creds = {c["name"]: c for c in client.credentials() if c.get("name") in managed}
    weekly_buckets = (MAIN, FABLE) if cfg.fable_models else (MAIN,)

    def snapshots() -> dict[str, AccountSnapshot]:
        out = {}
        for a in managed:
            if a not in creds:
                continue
            ps = state.probes.get(a, ProbeState())
            extra = merge(ps.readings, {SESSION: ps.anchor_reading} if ps.anchor_reading else {})
            out[a] = build_snapshot(creds[a], extra, cfg.main_models, cfg.fable_models, now)
        return out

    def maybe_probe(account: str, snap: AccountSnapshot, switching_to: bool) -> bool:
        if dry_run:
            return False
        ps = state.probes.setdefault(account, ProbeState())
        # Data is only as fresh as the oldest weekly bucket; a missing bucket counts as stale.
        weekly = [snap.readings.get(b) for b in weekly_buckets]
        reference = None if None in weekly else min(r.observed_at for r in weekly)
        if not due(now=now, last_probe=ps.last_probe, retry_after=ps.retry_after,
                   newest_observed=reference,
                   reset_estimated=any(r.source == "estimate" for r in snap.readings.values()),
                   switching_to=switching_to):
            return False
        result = prober(cfg.auth_dir / account, now)
        ps.last_probe = now
        ps.retry_after = result.retry_after
        if result.error:
            log.warning("probe %s failed: %s", account, result.error)
            return False
        ps.readings = result.readings
        if not ps.anchor_main_model and anchor_did_not_start_window(
                result.readings.get(SESSION), ps.anchor_reading, ps.last_anchor):
            ps.anchor_main_model = True
            log.warning("anchor %s: Haiku did not start the 5-hour window; using %s from now on",
                        account, cfg.anchor.fallback_model)
        return True

    snaps = snapshots()
    if any([maybe_probe(a, s, False) for a, s in snaps.items()]):
        snaps = snapshots()
    decision = decide(snaps, cfg, now, state.active, state.last_main_order)
    current = {state.active.get(b, (None, None))[0] for b in (MAIN, FABLE)}
    targets = {decision.main.account, decision.fable.account} - current - {None}
    if any([maybe_probe(a, snaps[a], True) for a in sorted(targets) if a in snaps]):
        snaps = snapshots()
        decision = decide(snaps, cfg, now, state.active, state.last_main_order)

    for bucket, choice in ((MAIN, decision.main), (FABLE, decision.fable)):
        previous, since = state.active.get(bucket, (None, None))
        state.active[bucket] = (choice.account, since if choice.account == previous else now)
    state.last_main_order = decision.main_order

    todo = patches(decision, managed, cfg.fable_models, disk)
    log.info("main=%s [%s] | fable=%s [%s] | order=%s", decision.main.account, decision.main.reason,
             decision.fable.account, decision.fable.reason, ",".join(decision.main_order))
    for warning in decision.warnings:
        log.warning(warning)
    applied = _write(client, todo, cfg, dry_run)
    _anchor(cfg, state, decision, snaps, now, dry_run, anchorer)
    if not dry_run:
        save_state(state, cfg.state_path)
    return RunResult(decision, snaps, todo, applied)


def set_disabled(cfg: Config, client, disabled: bool) -> list[Patch]:
    cfg = _present(cfg)
    state = load_state(cfg.state_path)
    state.disabled = disabled
    save_state(state, cfg.state_path)
    if not disabled:
        return []
    todo = baseline_patches(cfg.baseline, cfg.fable_models, _disk_fields(cfg))
    for p in todo:
        client.patch_fields(p.auth_file, p.fields)
        log.info("baseline restored %s %s", p.auth_file, p.fields)
    return todo


def format_status(cfg: Config, result: RunResult, now: datetime, state: State | None = None) -> str:
    d = result.decision
    if d is None:
        return "balancer disabled: baseline priorities in effect"
    lines = [
        f"main  -> {d.main.account}: {d.main.reason}",
        f"fable -> {d.fable.account}: {d.fable.reason}",
        f"order : {', '.join(d.main_order)}",
    ]
    for account in cfg.accounts:
        lines.append(f"\n{account.auth_file} (reserve={'yes' if account.reserve else 'no'})")
        snap = result.snapshots.get(account.auth_file)
        if snap is None:
            lines.append("  not listed by CPA")
            continue
        for bucket in (MAIN, FABLE, SESSION):
            r = snap.readings.get(bucket)
            if r is None:
                lines.append(f"  {bucket:7} no data")
                continue
            age = int((now - r.observed_at).total_seconds() // 60)
            if r.resets_at is None:
                lines.append(f"  {bucket:7} closed  ({r.source}, {age} min old)")
                continue
            lines.append(f"  {bucket:7} {r.util:5.1f}%  resets {r.resets_at:%Y-%m-%d %H:%M} UTC"
                         f"  ({r.source}, {age} min old)")
        ps = state.probes.get(account.auth_file) if state else None
        if ps and ps.last_anchor:
            model = cfg.anchor.fallback_model if ps.anchor_main_model else cfg.anchor.model
            lines.append(f"  anchor: last {ps.last_anchor:%Y-%m-%d %H:%M} UTC with {model}")
    lines += [f"pending: {p.auth_file} {p.fields}" for p in result.patches]
    return "\n".join(lines)


def _setup_logging(cfg: Config, interactive: bool) -> None:
    cfg.log_path.parent.mkdir(parents=True, exist_ok=True)
    handler = RotatingFileHandler(cfg.log_path, maxBytes=1_000_000, backupCount=3)
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    log.addHandler(handler)
    if interactive:
        log.addHandler(logging.StreamHandler(sys.stderr))
    log.setLevel(logging.INFO)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="cpa-balancer")
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("run").add_argument("--dry-run", action="store_true")
    for name in ("status", "disable", "enable"):
        sub.add_parser(name)
    args = parser.parse_args(argv)

    try:
        cfg = load_config(args.config)
    except (ConfigError, FileNotFoundError, tomllib.TOMLDecodeError) as e:
        print(f"cpa-balancer: config error: {e}", file=sys.stderr)
        return 2
    _setup_logging(cfg, interactive=sys.stderr.isatty())
    lock_path = cfg.state_path.with_suffix(".lock")
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    with open(lock_path, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            log.info("another run holds the lock; exiting")
            return 0
        try:
            client = CpaClient(cfg.base_url, management_key(cfg.keychain_service))
            now = datetime.now(timezone.utc)
            if args.command == "run":
                run_cycle(cfg, client, now, dry_run=args.dry_run)
            elif args.command == "status":
                print(format_status(cfg, run_cycle(cfg, client, now, dry_run=True), now,
                                    load_state(cfg.state_path)))
            else:
                set_disabled(cfg, client, args.command == "disable")
        except CpaError as e:
            log.error("CPA unavailable: %s", e)
            return 1
    return 0
