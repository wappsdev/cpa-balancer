# cpa-balancer

A native plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) v8 that balances
several Claude accounts by their quota.

For every Claude request it picks the account whose weekly window resets soonest, so quota that
is about to reset is used before quota that would carry over. Accounts can hold back a reserve,
and the Fable bucket is decided separately from the main one. The plugin:

- reads each account's usage from the rate-limit headers of every response (no extra requests);
- probes an account whose readings are stale or which CPA holds back, through Anthropic's usage
  endpoint (no model request, no quota spent), and releases it when its window has reopened;
- starts an idle account's 5-hour window with a one-token request, so a switch lands on a window
  that resets sooner;
- weighs accounts by plan (Pro, Max, Ultra) so their percentages add up;
- looks ahead at all accounts together: at the pace the work has gone, does the room last until
  the coming resets refill it, and if not, when does it run out and for how long;
- keeps 90 days of history and serves a page in CPA's Management Center.

## Requirements

- CLIProxyAPI v8 with native plugins enabled.
- Go 1.26 or later to build. The plugin is a `c-shared` library; the Makefile installs it for
  macOS on Apple silicon (`darwin/arm64`). Other platforms need their own CPA plugin directory.

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

## Management API

Under CPA's management API, at `/v0/management/plugins/cpa-balancer`, with CPA's management key:

| Method | Path | |
|---|---|---|
| GET | `/status` | accounts, readings, caps, the decision per bucket and recent changes |
| GET | `/history?range=24h\|7d\|30d\|90d` | utilization, active account and tokens over time |
| GET | `/outlook` | the look-ahead for the weekly and 5-hour windows, each account's weekly handoff in the balancer's order (`week.accounts`), and the measured work pace |
| POST | `/run` | one balancer tick now |
| POST | `/recheck?auth_file=…` | probe one account (or every account) now |
| GET | `/credentials` | the Claude credentials CPA lists |

## License

MIT, see [LICENSE](LICENSE). The page bundles [uPlot](https://github.com/leeoniya/uPlot)
(MIT, its license is `internal/web/assets/LICENSE`). The plugin builds against
CLIProxyAPI's plugin API (MIT) and uses `gopkg.in/yaml.v3` (MIT and Apache-2.0).
