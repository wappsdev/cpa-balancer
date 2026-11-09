package outlook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// seat is one account of a sequence test: its weekly use, its reset in hours from now, its weight
// and, with steps, a custom reserve.
type seat struct {
	key           string
	util, resets  float64
	weight        float64
	steps         []config.ReserveStep
	unavailable   bool
	notConfigured bool
}

// world is the status and the configuration of accounts with only a weekly reading; `active` is
// the balancer's pick now, and every account reaches `final` once reserves are released.
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
		v.Accounts = append(v.Accounts, engine.AccountView{AuthFile: s.key, Name: strings.ToUpper(s.key), Weight: s.weight,
			Listed: true, Unavailable: s.unavailable, Readings: quota.Readings{quota.Main: {Util: s.util, ResetsAt: &reset, ObservedAt: now}}})
	}
	return v, cfg
}

// week is the weekly outlook at `perHour` Pro-plan points an hour.
func week(t *testing.T, perHour float64, v engine.View, cfg config.Config) Outlook {
	t.Helper()
	o, ok := weekly(v, cfg, &Pace{PerHour: perHour}, now)
	if !ok {
		t.Fatal("two accounts or more combine")
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

func TestReservesAreReleasedAndTheLastAccountExhausts(t *testing.T) {
	// Both hold 10 points back (90 to the reset) and reach the final cap, 100, once released. At
	// 2 points an hour: A 80 -> 90 at 5 h, B 80 -> 90 at 10 h; with every account at its cap the
	// reserves are released and A (the earliest reset) goes to 100 at 15 h, then B at 20 h. No
	// account has room left then: B is where work stops, and its flag is that moment.
	reserve := []config.ReserveStep{{Cap: 90, Hours: 0}}
	v, cfg := world(100, "a",
		seat{key: "a", util: 80, resets: 100, weight: 1, steps: reserve},
		seat{key: "b", util: 80, resets: 120, weight: 1, steps: reserve})
	o := week(t, 2, v, cfg)
	got, exhausts := handoffs(t, o)
	near(t, "a", got["a"], 5, 1e-6)
	near(t, "b", got["b"], 20, 1e-6)
	if exhausts != "b" {
		t.Fatalf("exhausts %q", exhausts)
	}
	// the combined run-out is the same moment, from the same simulation
	if o.RunsOut == nil || !o.RunsOut.Equal(*o.Accounts[1].At) {
		t.Fatalf("runs out %v, b's handoff %v", o.RunsOut, o.Accounts[1].At)
	}
	// dry until A's reset brings room back
	if o.Verdict != "out" || o.Dry == nil || o.Dry.Refill != "A" {
		t.Fatalf("%+v %+v", o, o.Dry)
	}
	near(t, "dry hours", o.Dry.Seconds/3600, 80, 1e-6)
}

func TestAnUnavailableOrUnconfiguredAccountTakesNoTurn(t *testing.T) {
	// B is held back by CPA and C is not in the balancer's configuration: A's 20 points go in
	// 10 h and the room is out, though B and C have room.
	v, cfg := world(100, "a",
		seat{key: "a", util: 80, resets: 100, weight: 1},
		seat{key: "b", util: 0, resets: 50, weight: 1, unavailable: true},
		seat{key: "c", util: 0, resets: 60, weight: 1, notConfigured: true})
	o := week(t, 2, v, cfg)
	got, exhausts := handoffs(t, o)
	near(t, "a", got["a"], 10, 1e-6)
	if exhausts != "a" || got["b"] != -1 || got["c"] != -1 {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
	near(t, "runs out", o.RunsOut.Sub(now).Hours(), 10, 1e-6)
}

func TestTheSequenceFollowsResetsLikeThePools(t *testing.T) {
	// The pools' case without reserves (TestFollowsResetsThroughToTheNextOne): the balancer's
	// order gives the same run-out, 101.6 h. G hands off at 94/1.91 = 49.2 h to W, refilled at
	// 14 h; the room runs out on W in its next window, so no account's current window marks it.
	v, cfg := world(100, "g", seat{key: "w", util: 100, resets: 14, weight: 1}, seat{key: "g", util: 6, resets: 137, weight: 1})
	o := week(t, 1.91, v, cfg)
	near(t, "runs out in hours", o.RunsOut.Sub(now).Hours(), 101.6, 0.05)
	near(t, "dry hours", o.Dry.Seconds/3600, 35.4, 0.05)
	got, exhausts := handoffs(t, o)
	if exhausts != "" || got["w"] != -1 {
		t.Fatalf("handoffs %v, exhausts %q", got, exhausts)
	}
	near(t, "g", got["g"], 94/1.91, 1e-6)
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
	five, _ := Compute([]Account{{Key: "a", Weight: 1, Cap: 98, Util: 10, ResetsAt: in(2)}, {Key: "b", Weight: 1, Cap: 98, Util: 10, ResetsAt: in(3)}}, Five, nil, now)
	if b, _ := json.Marshal(five); strings.Contains(string(b), "accounts") {
		t.Fatalf("%s", b)
	}
}

func TestBuildLooksAheadWithTheConfiguration(t *testing.T) {
	v, cfg := world(100, "a", seat{key: "a", util: 80, resets: 100, weight: 1}, seat{key: "b", util: 90, resets: 120, weight: 1})
	s := rising("a", 6, 1)
	h := history.Series{Step: s.Step, T: s.T, Accounts: map[string]*history.AccountSeries{"a": {Main: s.Main["a"]}}}
	o := Build(v, cfg, h, now)
	if o.Week == nil || o.Pace == nil || len(o.Week.Accounts) != 2 || o.Week.Accounts[0].Key != "a" {
		t.Fatalf("%+v %+v", o.Week, o.Pace)
	}
}
