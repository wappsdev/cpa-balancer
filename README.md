# cpa-balancer

Quota-aware Claude account balancing for a local CLIProxyAPI. Every 2 minutes it reads each
account's weekly, Fable and 5-hour usage, picks the account whose weekly window resets
soonest (keeping reserves on accounts that have one), and applies the choice to CPA through
the Management API (`priority` for the main bucket, `excluded_models` for Fable).

While the chosen account's 5-hour window is running, every other usable account's 5-hour
window is started with a 1-token Haiku request (`[anchor]` in the config), so a switch lands
on a window that resets sooner. If a probe shows Haiku did not start the shared window, that
account switches to anchoring with `fallback_model`. Design:
`docs/superpowers/specs/2026-10-03-session-anchoring-design.md`.

Design: `docs/superpowers/specs/2026-10-03-cpa-balancer-design.md`

## Commands

    cpa-balancer status          # current choice, readings and reasons (no writes)
    cpa-balancer run --dry-run   # one decision cycle without probing or writing
    cpa-balancer run             # one decision cycle (what launchd runs)
    cpa-balancer disable         # restore baseline priorities and stop deciding
    cpa-balancer enable          # resume

Config: `~/.config/cpa-balancer/config.toml` (see `config.example.toml`).
Log: `~/.local/state/cpa-balancer/balancer.log`. State: `~/.local/state/cpa-balancer/state.json`.

## Install

    uv tool install --force .
    cp deploy/dev.wapps.cpa-balancer.plist ~/Library/LaunchAgents/
    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.wapps.cpa-balancer.plist

Do not use `uv tool install --editable`: the repository lives under `~/Documents`, which
macOS privacy protection (TCC) blocks for launchd agents, so the scheduled run hangs on
import waiting for a permission prompt. A regular install copies the package out of
`~/Documents`. After changing the code, run `uv tool install --force .` again.

## Remove

    cpa-balancer disable
    launchctl bootout gui/$(id -u)/dev.wapps.cpa-balancer
    rm ~/Library/LaunchAgents/dev.wapps.cpa-balancer.plist

## Tests

    uv run pytest -q
