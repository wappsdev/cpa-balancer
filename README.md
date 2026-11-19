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
          affinity:
            projects_dir: ~/.claude/projects   # Claude Code's projects directory
            folder_pins: {my-folder: claude-xxxxxxxx-first@example.com.json}   # optional
            session_pins: {<main session id>: claude-xxxxxxxx-second@example.com.json}   # optional

An invalid config is not applied: the plugin keeps the previous one and reports
`config_error` on its page.

## Extra models

A Claude Mythos model that CPA's catalog does not have can be served through chosen accounts only
(for example a model that only one account is approved for):

          extra_models:
            - id: claude-mythos-5-2
              display_name: Claude Mythos 5.2
              accounts: [claude-xxxxxxxx-second@example.com.json]   # tried in this order
              context_length: 1000000
              max_completion_tokens: 128000
              thinking_levels: [low, medium, high, xhigh, max]       # adaptive thinking; omit for none

- The plugin registers the model with CPA for the `claude` provider (CPA's model registrar), so the
  built-in claude executor serves it. A plugin cannot read CPA's model registry, so the context,
  output limit and thinking levels are set here.
- CPA then offers every claude account for the model. The balancer's scheduler sends a request to
  the first of the model's accounts that CPA offers, and refuses it (`extra_model_unavailable`)
  when none is offered. It never falls back to another account. The pin holds while paused, in
  shadow mode, and over folder, session and CPA's own pins.
- The scheduler matches the model name the way CPA routes it: spaces around it, letter case, a
  thinking suffix such as `(high)`, an auth prefix and a `[1m]` marker do not escape the pin.
- An id must start with `claude-mythos-` (lower case). Every request for an extra model is pinned,
  so an id that is also a catalog model would pin all of that model's traffic. The plugin cannot
  read CPA's catalog, and CPA updates the catalog from the network, so the family name is the rule.
- Each account must be one of `accounts`. An extra model cannot also be in a bucket.
- CPA asks for the models when it loads the plugin and after a config change. The model stays in
  `/v1/models` while the plugin is loaded, even while none of its accounts is ready; a request
  then gets the refusal. The page lists each extra model with the state of its accounts.
- After an upstream error CPA cools the account down; requests for the model are refused until
  the cooldown ends.

**Limits: in these cases the pin does not hold and a request can reach any claude account.**
The plugin cannot see any of them, so neither the config check nor the page can warn about them.

- **Never alias an extra model.** An `oauth-model-alias` entry, global or on one auth, whose
  upstream name is an extra model lets a request for the alias name pass the scheduler unmatched.
  A plugin's model registrar gets no view of CPA's alias table.
- **CPA's Home mode.** With Home mode on, CPA picks accounts without the plugin scheduler.
- **A fused plugin.** When CPA recovers a panic inside its plugin adapter it stops calling the
  plugin's scheduler (it "fuses" it), but the extra models stay registered until the next config
  change or restart. Restart CPA, or remove `extra_models`, after a fuse is logged.

## Page

Management Center → **Account Balancer** (sign in with "Remember password"):


- header: balancing On / Paused, mode apply / shadow, Run now
- Overview: chosen account per bucket with the reason, accounts, sessions grouped by project (with pins and the Dengele rebalance), recent changes
- History: 24h · 7d · 30d · 90d of weekly and 5-hour utilization, active account and tokens
- Settings: accounts, thresholds, buckets and anchoring, saved through CPA after a review

State: `~/.cli-proxy-api/cpa-balancer/state.json`. History (90 days of 5-minute slots):
`~/.cli-proxy-api/cpa-balancer/history/`. Log lines go to CPA's log with the prefix
`cpa-balancer:`.

## Sessions

Claude Code sends its session id with every request, and CPA hands it to the plugin. The balancer
counts each session's requests, failures and input, output and cache tokens per account and per
model, in the same 5-minute slots as the rest of the history, with a subagent's use also rolled up
into its parent. Requests without a session id are counted as `no session`.

The Overview shows them for the last 5 hours and the last 7 days (rolling windows, not an
account's own quota window), grouped by project folder (see "Project affinity"). When the page
opens, it shows one row per project, the largest first, all closed: requests and tokens (input,
output and cache writes) of the window in view with the other window beside them (`5 h` or `7 d`),
its share of the window in view and its account. A project is a repository: its totals and its
`~% Ultra` include its git worktrees. Open a project to see its own sessions and one sub-row per
worktree (named by its directory, the full path in the tooltip, with its own totals and its own pin
choice), a worktree to see its sessions, and a session to see its subagents; the open ones are
remembered in the browser. A session
row has its id (first 8 characters; the full id is in the tooltip), when it was last seen, requests,
tokens (cache reads and the accounts it used are in the tooltip) and its share of the window.
Sessions with no known folder, and requests without a session id, are one group, `no project`. A
project's totals are summed from every session of the window, so they are complete even when the
week lists only its 30 largest sessions; what the list leaves out is one line under the project.
`/status` carries the same data as `sessions.five_hour` and `sessions.week`:
`total` and `accounts` are the window's own counts, each row has `own` (itself alone) and `total`
(with its subagents), and the listed root sessions' `total` plus `other.total` always equal the
window's `total`. The week lists its 30 largest roots; the 5 hours are not cut.

CPA names a Claude Code session `claude:<sid>` and a subagent `claude:<sid>:agent:<agentId>`. The
balancer stores the main session as `<sid>`, the name of its transcript file, and a subagent as
`<sid>/<agentId>` under its parent (nested subagents the same way), the same keys the transcript
store uses. An id in another form is kept as it came.

A session id comes from a client header, so only a plain id (up to 128 of `A-Za-z0-9._:-/`) is
stored as given. Anything else, and every session past 200 in one slot, is counted under
`(other)`. Subagents are listed at most 8 levels deep; anything deeper is folded into its ancestor at the
last level. Model names are checked the same way and each map in a slot keeps at most 8 keys, so a
client cannot grow the history without bound. Slots written before sessions were recorded count
as `no session`.

Advisor models' calls are not seen live: CPA reports only the top-level `usage` of a response,
which leaves the advisor iterations out. Claude Code's transcripts hold them; see the transcript
section. The percent below does not count them either.

### Use of an Ultra account's limit window (`~% Ultra`)

Every project, session and subagent row shows what it used of **one Ultra account's limit window**
(plan `max_20x`, which the page calls Ultra) in both windows, whichever one is in view, such as
`5h ~3.2% · 7d ~12.0%`; a row that is in only one window shows `—` for the other. A line under the
Sessions header says what the percent is based on, from the window length and the unit the status
serves. It is an estimate, and it is learned, not assumed.

- **Each Ultra account is its own meter.** An Ultra account reports one `main` limit with a
  `resets_at`, and no 5-hour limit. Its utilization opens each window at zero. The balancer
  reads that utilization from the history, finds each window where the utilization falls by 3
  points or more (a reading corrects itself by a point at most in the history), and divides what the meter shows by what the account served since its window
  opened. That is the account's factor (percent per one million units of use). A row's share is
  its use on each Ultra account times that account's factor. Nothing is averaged across
  accounts, and no constant is built in.
- **The rows add up to the meter.** For each Ultra account, every session, subagent and the
  requests without a session add up to the account's utilization in the current window, less
  what rose outside the proxy (below). Checked on the real history: 29.00 of 29.0 and 89.00 of
  89.0 points on the two Ultra accounts.
- **One unit of use** is input tokens + output tokens + cache writes + 1.5% of cache reads. The
  basis was chosen on the history of two Ultra accounts: the factors of their spans spread by
  15% (standard deviation over the mean) on input and output alone, by 8.7% with cache writes
  added, and by 5.5% with the cache-read fraction on top (the fraction was fitted; the spread
  is flat between 1% and 2%). Cache writes were recorded from 2026-11-01 on, so a window opened
  before that is not a valid sample.
- **Windows.** The window length is measured from the gaps between the resets seen in the
  history (the median), and shown in the tooltip; nothing assumes it. A window needs 5 points on
  the meter to give a factor, because the meter moves in whole points; below that, the previous
  window's factor is used, and when there is none, or the account's window was already running
  when the history began, the row shows `—`. A rolling 7-day figure crosses resets: each slot of
  use is priced with the factor of the window it fell in, so the part before a reset uses the
  previous window's factor. The history is read back 15 days for this, so the previous window
  is there.
- **Other plans stay apart.** An account on any other plan (for example Pro) learns nothing and
  is not in the percent. Its tokens are listed in the cell's tooltip (`On Pro: 1.2M tokens`).
- **Models.** Every model draws on the same meter, and the balancer does not weight models
  apart. Per-model weights are not learned yet: a per-model fit over the intervals between
  readings (opus and sonnet, whose mix varied from 1% to 72% of the use) did not beat a single
  factor on windows held out (an error of 0.62 points per interval against 0.61), and the two
  weights it found were close (0.26 and 0.23 points per million units). Nothing is built in
  between models. Fable use also counts toward the same meter, so it is inside the percent; its
  models are marked `Fable limit` on the model lines. Only one interval had Fable use, too
  little to fit it apart. The fit grouped the models by family, which is coarser than the
  folding of the model lines below.
- **Model lines.** A project or session row lists each model it used, by first seen, with first
  and last seen (to the 5-minute slot) its tokens (input, output, cache writes) and its share of the row, the
  subagents' use apart (`2.5k in subagents`), so a session whose subagents run another model has
  not switched. Variants of one model, a context size (`name[1m]`) or a date (`name-20251001`,
  `name@20251001`), fold into one line; the row's `models` keeps the names as stored, and the
  lines add up to the row's `total`. A name that is not plain stays under `(other)`.
- **Outside the proxy.** The meter counts all use of the account, including use that never went
  through the proxy, which the balancer cannot see. A rise of the meter between two readings one
  slot apart, with no use of the proxy in the hour either side, is counted as outside the proxy:
  it is taken off the meter before the factor is worked out, so it does not make the sessions
  look dearer, and it is shown apart in the header line (`outside the proxy: 6 points of
  <account>`), in no row. Readings with a gap between them are not counted (the balancer may
  not have been running). Use outside the proxy that happens next to proxy use cannot be told
  apart, and stays in the factor. On the history measured (two Ultra accounts, 44 days) one
  account had no such rise; the other had 23 of 683 points, one run of four hourly steps (8
  points) and single points besides, none since 2026-11-03.

`/status` carries `meter` (the plan label, the unit, the models of the Fable limit), for each
Ultra account `accounts[].meter` (`known`, `per_million`, `from_previous`, `window_hours`, `outside`) and
`plan_label`, and on every project (`affinity.projects[]`) and every session row (the
`sessions` rows and their `subagents`) `planShare5h` and `planShare7d` (percent of one account's
limit window, `null` when it cannot be priced, or, on a session, when it has no row in that window) and `elsewhere5h` and `elsewhere7d` (tokens on
accounts with no meter). Rows also carry `model_lines`; projects carry them as `models` in each of
their two windows.

## Project affinity

Every Claude Code session is bound to an account through its project folder, so all sessions of one
folder use one account (and one prompt cache). Each rule below applies to requests of the configured
buckets, in `apply` mode and not paused; `shadow` and paused hand every pick to CPA as before.

- **Session and folder.** CPA gives the scheduler plugin the client's request headers, not its own
  `claude:<sid>` ids (it makes those later). The balancer reads `X-Claude-Code-Session-Id` and
  `X-Claude-Code-Agent-Id`, and builds the same key the history uses (`<sid>`, or `<sid>/<agentId>` for a
  subagent). A session's folder is the directory under `affinity.projects_dir` that holds
  `<sid>.jsonl`; a subagent uses its parent's folder. When a resumed session has a file in two
  folders, the one written last is its folder. The balancer lists directory names and stats the file
  (its modification time). It never opens a transcript. The lookup is cached (a hit for 60 seconds,
  then looked up again; a miss for 5 seconds, because Claude Code writes the transcript after the
  first request). A session with no header, no folder yet or
  an id that is not a plain one has no project: its pick is CPA's, as before.
- **Worktrees.** The git worktrees of one repository are one project. A folder name is a working
  directory with every character that is not a letter or a digit written as `-`, so it is ambiguous.
  The balancer finds the directory by walking the file system from `/` and matching the encoded names
  (bounded work, cached for 10 minutes; a re-check that finds nothing keeps the earlier answer). There
  it reads `.git` and nothing else: a directory means a main repository; a file `gitdir: <path>` that
  names `<common>/worktrees/<name>` means a linked worktree, and the main repository is the directory
  that holds the common git directory (`<gitdir>/commondir` says where it is, `../..` by default). It
  never runs git. A submodule, a folder it cannot find, and a subdirectory of a repository stay
  projects of their own. Homes, folder pins, balancing and a rebalance use the main repository's
  folder as the project, so a folder pin on it holds for its worktrees; a folder pin on a worktree's
  own folder holds for that worktree alone. Keys stay the folder names Claude Code made. A home stored
  under a worktree's folder before this existed is ignored.
- **Stay until full.** A project stays on its home account until that account reaches its weekly cap,
  the cap in force from `thresholds` and the account's reserve (the same cap the balancer switches on).
  Only a fresh weekly reading at or over the cap is "full". A 5-hour cap, a CPA cooldown, stale data
  or an account CPA does not offer for a request is a detour for that request: the project keeps its
  home and the request goes to another account (reason `fallback`). When the home is full the project
  moves to another account and stays there. A project never moves only to balance.
- **Balanced start.** A new project, and a project that moves, goes to the usable account that is
  furthest below its fair share. An account's share of the room is its weekly headroom: room under
  the cap in percent, times the plan's size (a plan not read yet counts as one Pro), as a part of the
  usable accounts' total. Its share of the load is the weight of the projects homed there, plus the
  new one, as a part of the total. The score is the first minus the second, so equal accounts split
  three new projects 2 and 1, and a Max account takes more than a Pro one. A project's weight is half a unit
  for being a project plus up to as much again by its tokens of the last 7 days (input, output and
  cache writes, read from the history and refreshed every 5 minutes) relative to the average used
  project; one that has used nothing yet weighs 1. Tokens alone would make many small projects
  weigh nothing next to two big ones, and every one of them would stay where the room is. A folder-pinned project counts on its pin's account.
  A tie goes to CPA's existing order. If no account is usable the project keeps its home and CPA's
  existing order decides (it releases reserves and uses credits as before). A project that has room
  never moves only to balance; "Rebalance" below is the owner's action for that.
- **Rebalance.** The `Dengele` button on the Sessions card (rebalance now) places every project without a
  folder pin again by the same score. It shows the plan first (project, from, to) and changes nothing
  until Apply. Projects with a folder pin stay, and count as load on their account. The plan starts
  from nothing, with the heaviest project first, so it does not depend on where projects are now:
  applying it twice changes nothing the second time, and a project whose account ties keeps it. A
  project may stay on any account that is not full, but it moves only onto a usable one: an account
  in a detour (5-hour cap, CPA cooldown, disabled) keeps its projects and is never filled, since
  every request of a project placed there would be a fallback. An account with no fresh weekly
  reading counts as having no room. With no account to go to, the projects stay. The log says how
  many moved, never which.
- **Pins.** A folder pin and a session pin (a main session's id; its subagents follow it) hold until the
  pinned account is full or held back. Then the request falls back like any other, and the page says
  so; the pin returns when the account has room. A session pin outranks a folder pin, and either outranks
  the home. Pins come from the config (`affinity.folder_pins`, `affinity.session_pins`, read only on the
  page) and from the page (kept in the state file). Limits: 100 folder pins and 200 session pins in
  each place; the state keeps at most 200 project homes (the least recently used one is dropped).
- **Fable.** A project moves only when its all-models week is full. A full Fable week alone sends that
  Fable request to another account and leaves the project where it is.

The Sessions card shows a project's account and why (its `folder pin`, else its `project home`, and
whether that account is full), and a session's account and why: `project home`, `folder pin`,
`session pin`, `fallback after cap`, `folder pin: account full, fallback`, `session pin: account
full, fallback` or `no project`. Each project row and each main-session row has an account choice:
`no pin` or one account. Choosing an account sets the folder pin or the session pin (the `/pin`
route) at once, and `no pin` clears it, so the choice always shows which pin is active. A pin set in
the config is shown and cannot be changed there. A pin on a folder or a session the window does not
show is listed under the table ("Pins outside this window"), with "Unpin". A folder name is user
data, so the page escapes it and the plugin keeps it out of its log, its recent changes and its
history. The picks behind the card are kept in memory (500 sessions); homes and pins are in the
state file, which is written at once when a pin or a rebalance changes it. The page finds a session's
folder through `affinity.session_projects` and a project's use through `affinity.projects` in
`/status`, both read from the history and the folder names, so they survive a restart.

## Transcript history

Claude Code writes every session to a transcript file, with the exact token counts of each
message. The balancer reads them into a store of its own, apart from the live history. The two
never mix: a transcript names no account and sees none of the proxy's quota readings, so its
numbers are not added to an account's figures, to the live sessions or to the charts, and nothing in
them is estimated.

`cpa-balancer-import` (`make build-import`, then `dist/cpa-balancer-import`) writes the store. It
reads only the usage fields of assistant messages, never their text, and prints counts and dates
only:

```
cpa-balancer-import --projects <transcripts dir> --state <balancer data dir> --dry-run
cpa-balancer-import --projects <transcripts dir> --state <balancer data dir>
```

The store is `<state>/transcripts/<YYYY-MM-DD>.jsonl` (UTC days), next to `history/`. Claude Code
deletes old transcripts (after about 30 days), so a run does not rebuild the store from scratch: it
replaces the day files of the last 28 days that it found messages for (Claude Code deletes a
transcript by its age, after about 30 days, so those days are complete). A day older than 28 days
is written only when it has no file yet and is never replaced, so a day whose transcripts are
partly deleted is not shrunk. The store therefore keeps history after the transcripts are gone, up
to 90 days, when a day file is removed. Running it again on the same transcripts leaves the same
bytes. A run that finds no message at all (a wrong or empty `--projects`, with `--dry-run` too)
fails with a non-zero exit code and writes nothing. Each file is replaced atomically, but the
store as a whole is not: a crash in the middle leaves every day file self-consistent, some of them
possibly stale, and the next run puts it right. Error messages never name a path. The importer
never opens `history/`. Each line is one session, UTC hour and model with `requests`, `input_tokens`,
`output_tokens`, `cache_read_tokens` and `cache_creation_tokens`:

- A message that spans several lines counts once, and a message id counts once whichever
  transcript holds it (a resumed session copies earlier messages); a message with no id counts
  once per transcript line.
- A main transcript is the session `<uuid>`, its file name. A subagent transcript
  (`<uuid>/subagents/**/agent-<id>.jsonl`, workflow agents included) is kept under `<uuid>/<id>`,
  the form the live path uses (see above), and a transcript whose name holds no agent id counts
  under the session. A row is never counted twice: a session's figure with its subagents is the sum of
  its own row and its children's, which the plugin works out when it reads.
- An advisor model's iteration (`usage.iterations`, type `advisor_message`) is its own row, with
  the advisor model and `"advisor": true`. The top-level usage leaves it out, and so do the session
  totals and the window's `total`; `advisor` carries it.
- A session's name is stored once, in `titles.jsonl`: the name set with `/rename`
  (`custom-title` line), else the agent's name (`agent-name`), else the one Claude Code makes up
  (`ai-title`), with its kind. Each kind can repeat in a transcript and the last one wins. Names
  are merged across runs: a name from this run replaces the session's old one, and a session this
  run did not see keeps its name for as long as it has rows in the store. Sessions whose id is
  not a plain one are all stored as `(other)` and get no name. Only the
  session's own main transcript names it: a line that says it belongs to another session, and a
  subagent's file, are not read for names. Control and format characters are removed and a name is
  cut at 120 characters. These three line types are read for that one field each, never any
  message text. A name is user text and is escaped on the page like every other value.
- Ids and model names have the same bounds as the live store: a name that is not plain (letters,
  digits and `._:-/`, up to 128 for a session, `[]@` and 64 for a model) is stored as `(other)`;
  a line past 4 MB is skipped.

The Management Center shows it on the History tab as "Transcript history", with a 7-day and a
30-day switch: the largest sessions (30 listed, the rest summed), each with its tokens by model
and the advisor models' tokens apart, its 10 largest subagents nested (the others on one line). A named session shows its name with the
short id beside it. It is labelled "From Claude Code
transcripts — not proxy quota; no account". It is served by `GET /transcripts?range=7d|30d` (below).
The plugin only reads the store, and only the day files of the range, at most 200,000 rows a
request (`truncated` says so when more were there).

## Look-ahead

The weekly look-ahead runs the use through the accounts at the work pace, in Pro-plan points an
hour, idle time included. The balancer measures it four ways, each built to catch a different way
the use goes (`pace.methods` lists the ones present, `pace.absent` the others with a short
`reason`, `pace.basis` names the pick). The pace is `typical`'s, a heavy but normal day. When
`typical` cannot be measured, it is the fastest of the other three (the worst case; a tie goes to
`now`, then `trend`, then `peak`):

- `peak`: spiky days. The heaviest 24 h ending in the last 7 days, as if it came every day
  (needs 2 h of work in those 24 h).
- `typical`: a heavy but normal day. The 75th percentile of the last 7 days' working days: the 7
  non-overlapping 24 h windows ending now, a day ago and so on, each kept when it has 2 h of work,
  at its pace, by linear interpolation between the closest ranks (needs 2 such days).
- `trend`: where the use is heading. The last day's pace plus half of the change from the last 3 days'
  pace (lower when use slows down, never below 0; needs 2 h of work in the last 24 h).
- `now`: the session in progress. The last 5 h's pace while working, at the hours a day worked over
  the 7 days (needs 30 min of work in the 5 h).

With no method present the pace is the 7 days' (basis `7d`, needs 2 h of work); with less, each
open window's own average (`source: average`).

Each account in `week.accounts` has its next event at that pace: `handoff` (when the balancer
leaves it at its cap) and `exhausts` (the room runs out on it). `capped` is the first moment in
the whole look ahead (two windows), in any window, at which the account reaches its cap at that
pace; where `handoff` is set it is the same moment, and it is absent when the account never caps in
the look ahead. `margin` is the smallest of 1, 1.2
and 1.4 times the pace at which the account reaches its cap within its current window (absent: not
even at 1.4). `lasts` is the same scale for all accounts together: the largest multiple of the pace
at which the room lasts.

`week.by_method` runs the same week at each method's pace, always `peak`, `typical`, `trend` and
`now` in that order: its `per_hour`, `verdict`, `lasts`, `runs_out`, `dry` and `accounts`, with the same meaning
as the fields above. The method the pace comes from is marked `used: true`, and its entry is the
same as those fields. A method not present has only its `name` and `reason` (`less than 30 min of
work in the last 5 h`). With no measured pace (each window's average) `by_method` is absent. The
page shows them in a table under the outlook, a method not present as `—` with its reason.

## Roll back

Pause it on the page (or set `paused: true`) to hand every pick back to CPA's fill-first
order. To unload it entirely, disable the plugin in the Management Center's plugin list or set
`plugins.configs.cpa-balancer.enabled: false` and restart CPA.

## Management API

Under CPA's management API, at `/v0/management/plugins/cpa-balancer`, with CPA's management key:

| Method | Path | |
|---|---|---|
| GET | `/status` | accounts, readings, caps, the decision per bucket, recent changes, the sessions of the last 5 hours and 7 days, and `affinity`: the project homes, the pins, each recent session's account and reason by session key, each project's use in both windows (`projects`) and the folder of each listed root session (`session_projects`); `project_of` maps every listed folder to its project (a git worktree to its main repository's folder), and a project in `projects` carries its worktrees' own figures in `worktrees` |
| GET | `/history?range=24h\|7d\|30d\|90d` | utilization, active account and tokens over time |
| GET | `/transcripts?range=7d\|30d` | the transcript store over the window: root sessions with their subagents, tokens by model and advisor tokens; no account, never part of `/status` or `/history` |
| GET | `/outlook` | the look-ahead for the weekly and 5-hour windows, each account's weekly handoff in the balancer's order (`week.accounts`), the same at each method's pace (`week.by_method`), and the measured work pace |
| POST | `/run` | one balancer tick now |
| POST | `/recheck?auth_file=…` | probe one account (or every account) now |
| POST | `/pin` | body `{"kind": "folder"\|"session", "key": …, "account": …}` pins a folder or a main session to an account; an empty `account` removes a pin set on the page. The names are in the body, never in the URL. Answers with the status |
| POST | `/rebalance` | body `{"apply": false}` (the default) answers with the plan, `{"apply": true}` carries it out: `{"applied", "pinned", "plan": [{"project", "from", "to"}], "accounts": [{"account", "state", "share", "projects"}]}`: every project without a folder pin, `from == to` for one that stays; `accounts` says how each account took part (`usable`, `detour` or `full`, its share of the room, the projects left on it) |
| GET | `/credentials` | the Claude credentials CPA lists |

## License

MIT, see [LICENSE](LICENSE). The page bundles [uPlot](https://github.com/leeoniya/uPlot)
(MIT, its license is `internal/web/assets/LICENSE`). The plugin builds against
CLIProxyAPI's plugin API (MIT) and uses `gopkg.in/yaml.v3` (MIT and Apache-2.0).
