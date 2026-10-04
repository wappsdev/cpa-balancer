# cpa-balancer

A CLIProxyAPI v8 native plugin that balances Claude accounts by quota. For every Claude
request it picks the account whose weekly window resets soonest, keeping reserves on accounts
that have one, with the Fable bucket decided separately. It reads usage from the rate-limit
headers of each response, probes stale accounts, and starts idle accounts' 5-hour window with a
one-token Haiku request so a switch lands on a window that resets sooner.

Designs: `docs/superpowers/specs/2026-10-04-cpa-balancer-plugin-design.md` (plugin) and
`docs/superpowers/specs/2026-10-04-panel-ui-design.md` (page).

## Build and install

    make test       # go vet + go test
    make install    # builds dist/cpa-balancer.dylib and copies it to ~/.cli-proxy-api/plugins/darwin/arm64/
    brew services restart cliproxyapi

CPA loads the library at startup, so restart it after every install.

## Configure

The config lives in CPA's config file under `plugins.configs.cpa-balancer`:

    plugins:
      configs:
        cpa-balancer:
          enabled: true
          mode: apply            # shadow: decide and record, but let CPA pick
          paused: false          # true: every pick goes to CPA's fill-first; no probes or anchors
          state_path: ~/.cli-proxy-api/cpa-balancer/state.json
          accounts:
            - auth_file: claude-xxxxxxxx-first@example.com.json
              reserve: false
            - auth_file: claude-xxxxxxxx-second@example.com.json
              reserve: true
          buckets:
            main: [claude-opus-5-5, claude-sonnet-5-5]
            fable: [claude-fable-5-1]
          thresholds:
            reserve_cap_far: 85    # reserve accounts, reset 48 h or more away
            reserve_cap_near: 90   # reserve accounts, reset 12–48 h away
            final_cap: 99          # every account
            session_cap: 98        # 5-hour window
            min_dwell_minutes: 60
            stale_hours: 6
          anchor:
            enabled: true
            model: claude-haiku-4-5-20251001
            fallback_model: claude-sonnet-5-5

An invalid config is not applied: the plugin keeps the previous one and reports
`config_error` on its page.

## Page

Management Center → **Account Balancer** (sign in with "Remember password"):

- header: balancing On / Paused, mode apply / shadow, Run now
- Overview: chosen account per bucket with the reason, accounts, recent changes
- History: 24h · 7d · 30d · 90d of weekly and 5-hour utilization, active account and tokens
- Settings: accounts, thresholds, buckets and anchoring, saved through CPA after a review

State: `~/.cli-proxy-api/cpa-balancer/state.json`. History (90 days of 5-minute slots):
`~/.cli-proxy-api/cpa-balancer/history/`. Log lines go to CPA's log with the prefix
`cpa-balancer:`.

## Roll back

Pause it on the page (or set `paused: true`) to hand every pick back to CPA's fill-first
order. To unload it entirely, disable the plugin in the Management Center's plugin list or set
`plugins.configs.cpa-balancer.enabled: false` and restart CPA.
