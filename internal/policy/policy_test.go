package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/config"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

const (
	W = "claude-w.json" // no reserve
	G = "claude-g.json" // keeps a reserve
)

func testConfig() config.Config {
	c := config.Default()
	c.Accounts = []config.Account{{AuthFile: W}, {AuthFile: G, Reserve: config.ReserveDefault}}
	c.Buckets.Main = []string{"claude-opus-5-5"}
	c.Buckets.Fable = []string{"claude-fable-5-1"}
	return c
}

func rd(util, resetsInH float64, ageMin ...float64) quota.Reading {
	age := 1.0
	if len(ageMin) > 0 {
		age = ageMin[0]
	}
	at := now.Add(time.Duration(resetsInH * float64(time.Hour)))
	return quota.Reading{Util: util, ResetsAt: &at, ObservedAt: now.Add(-time.Duration(age * float64(time.Minute))), Source: "probe"}
}

var closed = quota.Reading{ObservedAt: now, Source: "probe"}

func snap(r quota.Readings) Snapshot { return Snapshot{Readings: r} }

func decide(snaps map[string]Snapshot, active map[quota.Bucket]Active) Decision {
	return Decide(snaps, testConfig(), now, active)
}

func act(account string, minutesAgo float64) map[quota.Bucket]Active {
	return map[quota.Bucket]Active{quota.Main: {Account: account, Since: now.Add(-time.Duration(minutesAgo * float64(time.Minute)))}}
}

func TestCapTiersAtExactBoundaries(t *testing.T) {
	th := config.Default().Thresholds
	def := config.Account{Reserve: config.ReserveDefault}
	custom := config.Account{Reserve: config.ReserveCustom, ReserveCap: 50, ReserveHours: 120}
	off := config.Account{Reserve: config.ReserveOff}
	cases := []struct {
		account config.Account
		left    time.Duration
		want    float64
	}{
		{def, 48 * time.Hour, 85}, {def, 48*time.Hour - time.Second, 90},
		{def, 12 * time.Hour, 90}, {def, 12*time.Hour - time.Second, 99}, {off, 150 * time.Hour, 99},
		// a custom reserve holds its own cap until its release time, 5 days before the reset here
		{custom, 150 * time.Hour, 50}, {custom, 120 * time.Hour, 50}, {custom, 120*time.Hour - time.Second, 99},
	}
	for _, c := range cases {
		if got := Cap(c.account, now.Add(c.left), now, th); got != c.want {
			t.Errorf("Cap(%+v, %v) = %v, want %v", c.account, c.left, got, c.want)
		}
	}
}

func TestEarliestResetWinsAndOthersFollow(t *testing.T) {
	d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: snap(quota.Readings{quota.Main: rd(52, 90)})}, nil)
	if d.Main.Account != G || strings.Join(d.MainOrder, ",") != G+","+W || d.Main.Relaxed {
		t.Fatalf("%+v", d)
	}
}

func TestReserveCapForcesSwitch(t *testing.T) {
	d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: snap(quota.Readings{quota.Main: rd(86, 90)})}, act(G, 5))
	if d.Main.Account != W || !strings.Contains(d.Main.Reason, "forced switch") {
		t.Fatalf("%+v", d.Main)
	}
}

func TestNearResetRaisesReserveCap(t *testing.T) {
	for _, g := range []quota.Reading{rd(86, 30), rd(98, 6)} {
		d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: snap(quota.Readings{quota.Main: g})}, nil)
		if d.Main.Account != G {
			t.Fatalf("%+v", d.Main)
		}
	}
}

func TestSessionCapSwitchesAndReturnsAfterReset(t *testing.T) {
	w := snap(quota.Readings{quota.Main: rd(10, 140)})
	d := decide(map[string]Snapshot{W: w, G: snap(quota.Readings{quota.Main: rd(52, 90), quota.Session: rd(98, 1)})}, act(G, 5))
	if d.Main.Account != W {
		t.Fatalf("%+v", d.Main)
	}
	back := decide(map[string]Snapshot{W: w, G: snap(quota.Readings{quota.Main: rd(52, 90), quota.Session: closed})}, act(W, 61))
	if back.Main.Account != G {
		t.Fatalf("%+v", back.Main)
	}
}

func TestPreferredSwitchWaitsForDwell(t *testing.T) {
	snaps := map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: snap(quota.Readings{quota.Main: rd(52, 90)})}
	if d := decide(snaps, act(W, 30)); d.Main.Account != W || !strings.Contains(d.Main.Reason, "dwell") {
		t.Fatalf("%+v", d.Main)
	}
	if d := decide(snaps, act(W, 60)); d.Main.Account != G {
		t.Fatalf("%+v", d.Main)
	}
}

func TestReservesReleasedWhenNothingElseIsEligible(t *testing.T) {
	d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(99.5, 140)}), G: snap(quota.Readings{quota.Main: rd(86, 90)})}, nil)
	if d.Main.Account != G || !d.Main.Relaxed || len(d.Warnings) == 0 || !strings.HasPrefix(d.Warnings[0], "main:") {
		t.Fatalf("%+v", d)
	}
}

func TestNoRestrictionWhenEverythingIsExhausted(t *testing.T) {
	d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(99.5, 140)}), G: snap(quota.Readings{quota.Main: rd(99.5, 90)})}, nil)
	if d.Main.Account != "" || d.MainOrder != nil || len(d.Warnings) == 0 {
		t.Fatalf("%+v", d)
	}
}

func TestFableIsDecidedIndependently(t *testing.T) {
	d := decide(map[string]Snapshot{
		W: snap(quota.Readings{quota.Main: rd(10, 140), quota.Fable: rd(1, 140)}),
		G: snap(quota.Readings{quota.Main: rd(52, 90), quota.Fable: rd(99.5, 90)}),
	}, nil)
	if d.Main.Account != G || d.Fable.Account != W || d.FableOrder[0] != W {
		t.Fatalf("%+v", d)
	}
}

func TestStaleDataIsNeverChosenButKeptAsFallback(t *testing.T) {
	d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: snap(quota.Readings{quota.Main: rd(52, 90, 7*60)})}, nil)
	if d.Main.Account != W || strings.Join(d.MainOrder, ",") != W+","+G {
		t.Fatalf("%+v", d)
	}
}

func TestDisabledAccountIsNotChosen(t *testing.T) {
	g := snap(quota.Readings{quota.Main: rd(52, 90)})
	g.Unavailable = true
	if d := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: g}, nil); d.Main.Account != W {
		t.Fatalf("%+v", d.Main)
	}
}

var activeG = snap(quota.Readings{quota.Main: rd(52, 90), quota.Session: rd(30, 3)})

func targets(w Snapshot, last, retry map[string]time.Time, cfg config.Config) []string {
	snaps := map[string]Snapshot{W: w, G: activeG}
	return AnchorTargets(Decide(snaps, cfg, now, nil), snaps, cfg, now, last, retry)
}

func TestAnchorTargets(t *testing.T) {
	idle := snap(quota.Readings{quota.Main: rd(10, 140)})
	cfg := testConfig()
	check := func(name string, got []string, want string) {
		if strings.Join(got, ",") != want {
			t.Errorf("%s: got %v want %q", name, got, want)
		}
	}
	check("idle", targets(idle, nil, nil, cfg), W)
	check("closed", targets(snap(quota.Readings{quota.Main: rd(10, 140), quota.Session: closed}), nil, nil, cfg), W)
	check("already running", targets(snap(quota.Readings{quota.Main: rd(10, 140), quota.Session: rd(5, 2)}), nil, nil, cfg), "")
	check("exhausted", targets(snap(quota.Readings{quota.Main: rd(99.5, 140)}), nil, nil, cfg), "")
	check("spacing 9m", targets(idle, map[string]time.Time{W: now.Add(-9 * time.Minute)}, nil, cfg), "")
	check("spacing 10m", targets(idle, map[string]time.Time{W: now.Add(-10 * time.Minute)}, nil, cfg), W)
	check("retry-after", targets(idle, nil, map[string]time.Time{W: now.Add(time.Minute)}, cfg), "")
	off := testConfig()
	off.Anchor.Enabled = false
	check("disabled", targets(idle, nil, nil, off), "")
	noWork := map[string]Snapshot{W: idle, G: snap(quota.Readings{quota.Main: rd(52, 90), quota.Session: closed})}
	check("no work", AnchorTargets(Decide(noWork, cfg, now, nil), noWork, cfg, now, nil, nil), "")
}

func TestAnchorDidNotStartWindow(t *testing.T) {
	anchored := quota.Reading{ResetsAt: func() *time.Time { x := now.Add(3 * time.Hour); return &x }(), ObservedAt: now.Add(-2 * time.Hour), Source: "anchor"}
	if !AnchorDidNotStartWindow(closed, true, &anchored, now.Add(-2*time.Hour)) {
		t.Error("closed after a running anchor must be detected")
	}
	if AnchorDidNotStartWindow(rd(1, 3, 0), true, &anchored, now.Add(-2*time.Hour)) {
		t.Error("running window is fine")
	}
	expired := quota.Reading{ResetsAt: func() *time.Time { x := now.Add(-time.Minute); return &x }(), ObservedAt: now.Add(-5 * time.Hour)}
	if AnchorDidNotStartWindow(closed, true, &expired, now.Add(-5*time.Hour)) {
		t.Error("expired anchor window is fine")
	}
	if AnchorDidNotStartWindow(closed, true, nil, time.Time{}) || AnchorDidNotStartWindow(closed, false, &anchored, now.Add(-time.Hour)) {
		t.Error("needs an anchor and a probe")
	}
}

func TestProvisionalChoiceIsLeftWithoutDwell(t *testing.T) {
	snaps := map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)}), G: snap(quota.Readings{quota.Main: rd(52, 90)})}
	firm := act(W, 5)
	if d := decide(snaps, firm); d.Main.Account != W {
		t.Fatalf("a firm choice keeps its dwell: %+v", d.Main)
	}
	provisional := map[quota.Bucket]Active{quota.Main: {Account: W, Since: now.Add(-5 * time.Minute), Provisional: true}}
	if d := decide(snaps, provisional); d.Main.Account != G || d.Main.Provisional {
		t.Fatalf("a provisional choice must give way at once: %+v", d.Main)
	}
	onlyW := decide(map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 140)})}, nil)
	if onlyW.Main.Account != W || !onlyW.Main.Provisional {
		t.Fatalf("a choice made without G's data is provisional: %+v", onlyW.Main)
	}
}

func extraConfig(w, g bool) config.Config {
	c := testConfig()
	c.Accounts = []config.Account{{AuthFile: W, ExtraUsage: w}, {AuthFile: G, Reserve: config.ReserveDefault, ExtraUsage: g}}
	return c
}

func full() map[string]Snapshot {
	return map[string]Snapshot{
		W: snap(quota.Readings{quota.Main: rd(100, 70), quota.Session: rd(10, 3)}),
		G: snap(quota.Readings{quota.Main: rd(100, 20), quota.Session: rd(10, 3)}),
	}
}

func TestPlanQuotaGoesFirstAndCreditsAreTheLastResort(t *testing.T) {
	snaps := full()
	snaps[W] = snap(quota.Readings{quota.Main: rd(40, 70), quota.Session: rd(10, 3)})
	d := Decide(snaps, extraConfig(false, true), now, nil)
	if d.Main.Account != W || d.Main.Credits {
		t.Fatalf("an account with plan quota left must be used before credits, got %+v", d.Main)
	}
}

func TestSpendsCreditsOnTheAccountThatHasThem(t *testing.T) {
	d := Decide(full(), extraConfig(false, true), now, nil)
	if d.Main.Account != G || !d.Main.Credits || !d.Main.Relaxed {
		t.Fatalf("want G on credits, got %+v", d.Main)
	}
	if !strings.Contains(d.Main.Reason, "credits") {
		t.Fatalf("reason should say credits: %q", d.Main.Reason)
	}
}

func TestWithoutExtraUsageNothingIsRestricted(t *testing.T) {
	d := Decide(full(), extraConfig(false, false), now, nil)
	if d.Main.Account != "" || d.Main.Credits {
		t.Fatalf("want no restriction, got %+v", d.Main)
	}
}

func TestCreditsGoToTheSoonestResetWhenBothHaveThem(t *testing.T) {
	d := Decide(full(), extraConfig(true, true), now, nil)
	if d.Main.Account != G || !d.Main.Credits {
		t.Fatalf("G resets first (20h vs 70h), got %+v", d.Main)
	}
}

func TestFableNeedsRoomInTheAllModelsWeekToo(t *testing.T) {
	// Fable use counts toward the all-models week: W has Fable room but its week is full
	d := decide(map[string]Snapshot{
		W: snap(quota.Readings{quota.Main: rd(100, 13), quota.Fable: rd(47, 13)}),
		G: snap(quota.Readings{quota.Main: rd(33, 140), quota.Fable: rd(2, 140)}),
	}, nil)
	if d.Fable.Account != G {
		t.Fatalf("Fable must go where the week has room, got %+v", d.Fable)
	}
}
