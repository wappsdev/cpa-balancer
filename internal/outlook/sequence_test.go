package outlook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// seat is one account of a sequence test: its weekly use, its reset in hours from now, its weight
// and, with steps, a custom reserve; with `five`, its 5-hour window.
type seat struct {
	key           string
	util, resets  float64
	weight        float64
	steps         []config.ReserveStep
	five          *five
	unavailable   bool
	disabled      bool
	notConfigured bool
}

// five is a 5-hour window: its use and its reset in hours from now (0: closed, not started).
type five struct{ util, resets float64 }

// world is the status and the configuration of accounts; `active` is the balancer's pick now, and
// every account reaches `final` once reserves are released.
func world(final float64, active string, seats ...seat) (engine.View, config.Config) {
	cfg := config.Default()
	cfg.Thresholds.FinalCap = final
	v := engine.View{FinalCap: final, Decision: policy.Decision{Main: policy.Choice{Account: active}}}
	for _, s := range seats {
		a := config.Account{AuthFile: s.key, Reserve: config.ReserveOff}
		if len(s.steps) > 0 {
			a.Reserve, a.ReserveSteps = config.ReserveCustom, s.steps
		}
		if !s.notConfigured {
			cfg.Accounts = append(cfg.Accounts, a)
		}
		reset := in(s.resets)
		av := engine.AccountView{AuthFile: s.key, Name: strings.ToUpper(s.key), Weight: s.weight, Listed: true,
			Unavailable: s.unavailable || s.disabled, Disabled: s.disabled, FiveHour: s.five != nil,
			Readings: quota.Readings{quota.Main: {Util: s.util, ResetsAt: &reset, ObservedAt: now}}}
		if s.five != nil {
			r := quota.Reading{Util: s.five.util, ObservedAt: now}
			if s.five.resets > 0 {
				at := in(s.five.resets)
				r.ResetsAt = &at
			}
			av.Readings[quota.Session] = r
		}
		v.Accounts = append(v.Accounts, av)
	}
	return v, cfg
}

// week is the weekly outlook at `perHour` Pro-plan points an hour, with no 5-hour pace.
func week(t *testing.T, perHour float64, v engine.View, cfg config.Config) Outlook {
	t.Helper()
	return weekAt(t, perHour, 0, v, cfg)
}

// weekAt is the weekly outlook with every 5-hour window used at `sessionPerHour` percent an hour.
func weekAt(t *testing.T, perHour, sessionPerHour float64, v engine.View, cfg config.Config) Outlook {
	t.Helper()
	sessions := map[string]float64{}
	for _, a := range v.Accounts {
		if sessionPerHour > 0 {
			sessions[a.AuthFile] = sessionPerHour / 3600
		}
	}
	o, ok, err := weekly(v, cfg, &Pace{PerHour: perHour}, sessions, now)
	if err != nil || !ok {
		t.Fatalf("two accounts or more combine: %v", err)
	}
	return o
}

// handoffs is each account's handoff in hours from now (-1: none), and the one that exhausts.
func handoffs(t *testing.T, o Outlook) (map[string]float64, string) {
	t.Helper()
	out := map[string]float64{}
	exhausts := ""
	for _, a := range o.Accounts {
		out[a.Key] = -1
		if a.At != nil {
			out[a.Key] = a.At.Sub(now).Hours()
		}
		if a.Exhausts {
			if exhausts != "" {
				t.Fatalf("two accounts exhaust: %s and %s", exhausts, a.Key)
			}
			exhausts = a.Key
		}
	}
	return out, exhausts
}

func TestAnAccountThatResetsBeforeItsCapHandsNothingOn(t *testing.T) {
	// A is active at 57% of 95 and resets in 45 h; B is at 0% of 95 and resets in 61 h. At 38/54
	// points an hour A would reach 95 in 54 h, so it resets first (88.7% at 45 h): no handoff.
	// The balancer then takes B (the earliest reset), which reaches 11.3% by its own reset at
	// 61 h: no handoff either. The refilled A takes 95/(38/54) = 135 h from 61 h, to 196 h, and B
	// after it; at 229 h B resets with 23.2% and A (resets 381 h) takes over to 364 h, past the
	// two windows looked at: the room never runs out.
	v, cfg := world(95, "a", seat{key: "a", util: 57, resets: 45, weight: 1}, seat{key: "b", util: 0, resets: 61, weight: 1})
	o := week(t, 38.0/54, v, cfg)
	got, exhausts := handoffs(t, o)
	if got["a"] != -1 || got["b"] != -1 || exhausts != "" {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
	if o.RunsOut != nil {
		t.Fatalf("runs out at %v", o.RunsOut)
	}
}

func TestAReserveStepRaisingTheCapMovesTheHandoff(t *testing.T) {
	// A is at 60% and resets in 40 h, held to 85% until 24 h before its reset (16 h from now),
	// then the final cap, 99. At one point an hour A is at 76% when the cap rises, and reaches 99
	// at 39 h, before its reset. B (0%, resets in 100 h) takes over and resets at 61% first.
	v, cfg := world(99, "a",
		seat{key: "a", util: 60, resets: 40, weight: 1, steps: []config.ReserveStep{{Cap: 85, Hours: 24}}},
		seat{key: "b", util: 0, resets: 100, weight: 1})
	got, exhausts := handoffs(t, week(t, 1, v, cfg))
	near(t, "a's handoff with the step at 24 h", got["a"], 39, 1e-6)
	if got["b"] != -1 || exhausts != "" {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
	// with the 85% held until 12 h before the reset (28 h from now), A reaches 85 first, at 25 h
	cfg.Accounts[0].ReserveSteps = []config.ReserveStep{{Cap: 85, Hours: 12}}
	got, _ = handoffs(t, week(t, 1, v, cfg))
	near(t, "a's handoff with the step at 12 h", got["a"], 25, 1e-6)
}

func TestAHeldAccountTakesTheWorkBackWhenItsStepEnds(t *testing.T) {
	// B resets first (50 h) but is held at 85% until 24 h before it (26 h from now); A (40%,
	// resets in 100 h) takes the work meanwhile, to 66% at 26 h. Then B's cap is 100 and, the
	// earliest reset, B takes the work back: 85 -> 100 by 41 h. A goes on from 66% to 100 at 75 h.
	v, cfg := world(100, "a",
		seat{key: "a", util: 40, resets: 100, weight: 1},
		seat{key: "b", util: 85, resets: 50, weight: 1, steps: []config.ReserveStep{{Cap: 85, Hours: 24}}})
	got, exhausts := handoffs(t, week(t, 1, v, cfg))
	near(t, "b", got["b"], 41, 1e-6)
	near(t, "a", got["a"], 75, 1e-6)
	if exhausts != "" {
		t.Fatalf("exhausts %q", exhausts)
	}
}

func TestThreeAccountsHandOffInTurnCountedFromNow(t *testing.T) {
	// At 2 points an hour, in the balancer's order (the earliest reset first):
	//   A (weight 1) 50% -> 100 in 25 h; B (weight 2: 1% an hour) 80% -> 100 by 45 h; C (weight
	//   1) 0% -> 100 by 95 h. A reset at 70 h, so C hands off to the refilled A: room is left.
	v, cfg := world(100, "a",
		seat{key: "a", util: 50, resets: 70, weight: 1},
		seat{key: "b", util: 80, resets: 120, weight: 2},
		seat{key: "c", util: 0, resets: 150, weight: 1})
	got, exhausts := handoffs(t, week(t, 2, v, cfg))
	near(t, "a", got["a"], 25, 1e-6)
	near(t, "b", got["b"], 45, 1e-6)
	near(t, "c", got["c"], 95, 1e-6)
	if exhausts != "" {
		t.Fatalf("exhausts %q", exhausts)
	}
}

func TestAHandoffBeforeARunOutOnTheSameAccountIsWhatTheRowShows(t *testing.T) {
	// Both hold 10 points back (90 to the reset) and reach the final cap, 100, once released. At
	// 2 points an hour: A 80 -> 90 at 5 h, B 80 -> 90 at 10 h; with every account at its cap the
	// reserves are released and A (the earliest reset) goes to 100 at 15 h, then B at 20 h, where
	// no account has room left. B's next event is its handoff at 10 h: that is its flag, with no
	// run-out mark; the run-out at 20 h is Σ's.
	reserve := []config.ReserveStep{{Cap: 90, Hours: 0}}
	v, cfg := world(100, "a",
		seat{key: "a", util: 80, resets: 100, weight: 1, steps: reserve},
		seat{key: "b", util: 80, resets: 120, weight: 1, steps: reserve})
	o := week(t, 2, v, cfg)
	got, exhausts := handoffs(t, o)
	near(t, "a", got["a"], 5, 1e-6)
	near(t, "b", got["b"], 10, 1e-6)
	if exhausts != "" {
		t.Fatalf("exhausts %q", exhausts)
	}
	near(t, "runs out", o.RunsOut.Sub(now).Hours(), 20, 1e-6)
	// dry until A's reset brings room back
	if o.Verdict != "out" || o.Dry == nil || o.Dry.Refill != "A" {
		t.Fatalf("%+v %+v", o, o.Dry)
	}
	near(t, "dry hours", o.Dry.Seconds/3600, 80, 1e-6)
}

func TestACooledAccountComesBackAtItsWeeklyResetADisabledOrUnconfiguredOneNever(t *testing.T) {
	// B is cooled by CPA after its weekly 429, D is switched off by hand and C is not in the
	// balancer's configuration. A's 20 points go in 10 h and the room is out; B's reset at 50 h
	// ends its cooldown and brings room back, though D (reset at 30 h) and C have room.
	v, cfg := world(100, "a",
		seat{key: "a", util: 80, resets: 100, weight: 1},
		seat{key: "b", util: 100, resets: 50, weight: 1, unavailable: true},
		seat{key: "c", util: 0, resets: 60, weight: 1, notConfigured: true},
		seat{key: "d", util: 0, resets: 30, weight: 1, disabled: true})
	o := week(t, 2, v, cfg)
	got, exhausts := handoffs(t, o)
	near(t, "a", got["a"], 10, 1e-6)
	if exhausts != "a" || got["b"] != -1 || got["c"] != -1 || got["d"] != -1 {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
	near(t, "runs out", o.RunsOut.Sub(now).Hours(), 10, 1e-6)
	if o.Dry == nil || o.Dry.Refill != "B" {
		t.Fatalf("%+v", o.Dry)
	}
	near(t, "dry hours", o.Dry.Seconds/3600, 40, 1e-6)
}

func TestACooldownFromTheFiveHourWindowEndsAtItsReset(t *testing.T) {
	// B was cooled after a 5-hour 429 (its window full, resetting in 2 h). At one point an hour B,
	// the earlier weekly reset, then takes the work from A (82% by then) until B's weekly reset at
	// 50 h, and A goes on from 82% to 100 at 68 h. Held for good, B would leave A to fill at 20 h.
	v, cfg := world(100, "a",
		seat{key: "a", util: 80, resets: 100, weight: 1},
		seat{key: "b", util: 0, resets: 50, weight: 1, unavailable: true, five: &five{util: 100, resets: 2}})
	got, exhausts := handoffs(t, week(t, 1, v, cfg))
	near(t, "a", got["a"], 68, 1e-6)
	if got["b"] != -1 || exhausts != "" {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
}

func TestTheSimulationStartsOnTheLiveAccountAndKeepsItsDwell(t *testing.T) {
	// A has the earlier weekly reset (20 h) but its 5-hour window is full until 30 minutes from
	// now, so the balancer is on B (99.3%). At one point an hour B reaches its cap at 42 minutes.
	v, cfg := world(100, "b",
		seat{key: "a", util: 0, resets: 20, weight: 1, five: &five{util: 98, resets: 0.5}},
		seat{key: "b", util: 99.3, resets: 100, weight: 1, five: &five{util: 10, resets: 4}})
	// with no switch in the view the dwell counts as passed: A takes the work back at 30 minutes
	// (B at 99.8%), and B only gets it again after A's reset at 20 h, reaching its cap at 20.2 h
	got, _ := handoffs(t, week(t, 1, v, cfg))
	near(t, "b without a dwell", got["b"], 20.2, 1e-6)
	// the balancer moved to B 10 minutes ago, so it stays there for its dwell (to 50 minutes):
	// B reaches its cap first
	v.Changes = []state.Change{{At: now.Add(-10 * time.Minute), Bucket: quota.Main, From: "a", To: "b"}}
	got, _ = handoffs(t, week(t, 1, v, cfg))
	near(t, "b", got["b"], 0.7, 1e-6)
}

func TestTheFiveHourWindowMovesTheWorkAsLive(t *testing.T) {
	// A has the earlier weekly reset (40 h) but its 5-hour window is full until 3 h from now, so
	// the balancer is on B. Two points an hour on the week, 20% an hour on a 5-hour window:
	//   B 95 -> 100 at 2.5 h (its 5-hour window, started at 0, at 50%): B hands off. A is still
	//   held, but the week has room, so this is a wait, not a run-out.
	//   At 3 h A's window resets; A starts a new one and fills it (98%) at 7.9 h, its week at 99.8.
	//   A waits for that window's reset at 8 h, then reaches 100 at 8.1 h: no account has room left.
	v, cfg := world(100, "b",
		seat{key: "a", util: 90, resets: 40, weight: 1, five: &five{util: 98, resets: 3}},
		seat{key: "b", util: 95, resets: 100, weight: 1, five: &five{util: 0, resets: 0}})
	o := weekAt(t, 2, 20, v, cfg)
	got, exhausts := handoffs(t, o)
	near(t, "b", got["b"], 2.5, 1e-6)
	near(t, "a", got["a"], 8.1, 1e-6)
	if exhausts != "a" {
		t.Fatalf("exhausts %q", exhausts)
	}
	near(t, "runs out", o.RunsOut.Sub(now).Hours(), 8.1, 1e-6)
}

func TestTheRunOutIsMarkedInAnyWindow(t *testing.T) {
	// The pools' case without reserves (TestFollowsResetsThroughToTheNextOne): the balancer's
	// order gives the same run-out, 101.6 h. G hands off at 94/1.91 = 49.2 h to W, refilled at
	// 14 h; the room runs out on W in its next window, and W is marked there.
	v, cfg := world(100, "g", seat{key: "w", util: 100, resets: 14, weight: 1}, seat{key: "g", util: 6, resets: 137, weight: 1})
	o := week(t, 1.91, v, cfg)
	near(t, "runs out in hours", o.RunsOut.Sub(now).Hours(), 101.6, 0.05)
	near(t, "dry hours", o.Dry.Seconds/3600, 35.4, 0.05)
	got, exhausts := handoffs(t, o)
	if exhausts != "w" {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
	near(t, "w", got["w"], o.RunsOut.Sub(now).Hours(), 1e-9)
	near(t, "g", got["g"], 94/1.91, 1e-6)
}

func TestARoomAlreadyOutIsMarkedOnTheLiveAccount(t *testing.T) {
	v, cfg := world(100, "a", seat{key: "a", util: 100, resets: 50, weight: 1}, seat{key: "b", util: 100, resets: 60, weight: 1})
	o := week(t, 1, v, cfg)
	got, exhausts := handoffs(t, o)
	if exhausts != "a" || got["a"] != 0 || o.RunsOut == nil || !o.RunsOut.Equal(now) {
		t.Fatalf("handoffs %v, exhausts %q, runs out %v", got, exhausts, o.RunsOut)
	}
	if o.Verdict != "out" || o.Dry == nil || o.Dry.Refill != "A" {
		t.Fatalf("%+v %+v", o, o.Dry)
	}
}

func TestAccountsTheViewDoesNotListStillTakeTurnsAsInThePolicy(t *testing.T) {
	// The status can report every account as not listed by CPA (its credential list), while the
	// engine still gives the policy every configured account, so live work goes on. Two weight-10
	// accounts held to 90 until 24 h before their reset, no 5-hour window; a Pro held to 45 until
	// 48 h before, with a closed 5-hour window. The second is in use at 61%, resetting in 44.75 h;
	// at 5.35 Pro points an hour (0.535% an hour of it) it resets first, and nothing runs out.
	v, cfg := world(100, "second",
		seat{key: "first", util: 0, resets: 86.75, weight: 10},
		seat{key: "second", util: 61, resets: 44.75, weight: 10},
		seat{key: "pro", util: 0, resets: 60.75, weight: 1, steps: []config.ReserveStep{{Cap: 45, Hours: 48}}, five: &five{}})
	cfg.Thresholds.Reserve = []config.ReserveStep{{Cap: 90, Hours: 24}}
	cfg.Accounts[0].Reserve, cfg.Accounts[1].Reserve = config.ReserveDefault, config.ReserveDefault
	for i := range v.Accounts {
		v.Accounts[i].Listed = false
	}
	o := week(t, 5.35, v, cfg)
	got, exhausts := handoffs(t, o)
	if o.Verdict == "out" || o.RunsOut != nil || exhausts != "" || got["second"] != -1 {
		t.Fatalf("verdict %s, runs out %v, handoffs %v, exhausts %q", o.Verdict, o.RunsOut, got, exhausts)
	}
}

func TestACapReachedAtAnotherEventStillHandsOff(t *testing.T) {
	// A reaches 100 at 10 h, the very moment B resets
	v, cfg := world(100, "a", seat{key: "a", util: 80, resets: 100, weight: 1}, seat{key: "b", util: 100, resets: 10, weight: 1})
	got, _ := handoffs(t, week(t, 2, v, cfg))
	near(t, "a", got["a"], 10, 1e-6)
}

func TestAResetAlreadyPastStartsTheNextWindow(t *testing.T) {
	// A's last reading says 99% and a reset 2 h ago: it is at 0% now, resetting in 166 h, so B
	// (resets in 60 h) goes first, to 100 at 50 h, and A after it, to 100 at 100 h, inside A's
	// window. B, refilled at 60 h, runs out at 150 h, but its next event is the 50 h handoff.
	v, cfg := world(100, "a", seat{key: "a", util: 99, resets: -2, weight: 1}, seat{key: "b", util: 0, resets: 60, weight: 1})
	o := week(t, 2, v, cfg)
	got, exhausts := handoffs(t, o)
	near(t, "a", got["a"], 100, 1e-6)
	near(t, "b", got["b"], 50, 1e-6)
	if exhausts != "" {
		t.Fatalf("exhausts %q", exhausts)
	}
	near(t, "runs out", o.RunsOut.Sub(now).Hours(), 150, 1e-6)
}

func TestTheSimulationStopsWithAnErrorRatherThanRunOn(t *testing.T) {
	defer func(n int) { maxEvents = n }(maxEvents)
	maxEvents = 2
	v, cfg := world(100, "a", seat{key: "a", util: 80, resets: 100, weight: 1}, seat{key: "b", util: 90, resets: 120, weight: 1})
	if _, _, err := weekly(v, cfg, &Pace{PerHour: 2}, nil, now); err == nil {
		t.Fatal("an error expected")
	}
}

func TestSessionPacesComeFromTheHistoryElseTheFiveHourAverage(t *testing.T) {
	// A's 5-hour window rose 3 points a step for 4 steps of 30 minutes: 6% an hour while working.
	// B has no 5-hour use of its own: the 5-hour windows' average now, 10 Pro points over 2 h of
	// A's window (weight 1), is 5 an hour, 2.5% an hour of B (weight 2).
	v, _ := world(100, "a",
		seat{key: "a", util: 0, resets: 100, weight: 1, five: &five{util: 10, resets: 3}},
		seat{key: "b", util: 0, resets: 100, weight: 2, five: &five{util: 0, resets: 0}})
	s := Series{Step: 1800, Main: map[string][]*float64{}, Session: map[string][]*float64{}}
	x := func(f float64) *float64 { return &f }
	for i, a := range []float64{0, 3, 6, 9, 12, 12} {
		s.T = append(s.T, now.Add(-time.Duration(5-i)*30*time.Minute).Unix())
		s.Session["a"] = append(s.Session["a"], x(a))
	}
	got := sessionPaces(v, s, now)
	near(t, "a", got["a"]*3600, 6, 1e-9)
	near(t, "b", got["b"]*3600, 2.5, 1e-9)
}

func TestHandoffsInTheJSON(t *testing.T) {
	v, cfg := world(100, "a", seat{key: "a", util: 80, resets: 100, weight: 1}, seat{key: "b", util: 90, resets: 120, weight: 1})
	b, err := json.Marshal(week(t, 2, v, cfg))
	if err != nil {
		t.Fatal(err)
	}
	want := `"accounts":[{"key":"a","handoff":"2026-11-04T22:00:00Z"},{"key":"b","handoff":"2026-11-05T03:00:00Z","exhausts":true}]`
	if !strings.Contains(string(b), want) {
		t.Fatalf("%s", b)
	}
	// the 5-hour window has no sequence
	session, _ := Compute([]Account{{Key: "a", Weight: 1, Cap: 98, Util: 10, ResetsAt: in(2)}, {Key: "b", Weight: 1, Cap: 98, Util: 10, ResetsAt: in(3)}}, Five, nil, now)
	if b, _ := json.Marshal(session); strings.Contains(string(b), "accounts") {
		t.Fatalf("%s", b)
	}
}

func TestBuildLooksAheadWithTheConfiguration(t *testing.T) {
	v, cfg := world(100, "a", seat{key: "a", util: 80, resets: 100, weight: 1}, seat{key: "b", util: 90, resets: 120, weight: 1})
	s := rising("a", 6, 1)
	h := history.Series{Step: s.Step, T: s.T, Accounts: map[string]*history.AccountSeries{"a": {Main: s.Main["a"]}}}
	o, err := Build(v, cfg, h, now)
	if err != nil || o.Week == nil || o.Pace == nil || len(o.Week.Accounts) != 2 || o.Week.Accounts[0].Key != "a" {
		t.Fatalf("%+v %+v", o.Week, o.Pace)
	}
}
