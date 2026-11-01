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
	// steps in any order: 50% until 5 days, 70% until 2 days before the reset
	custom := config.Account{Reserve: config.ReserveCustom, ReserveSteps: []config.ReserveStep{{Cap: 70, Hours: 48}, {Cap: 50, Hours: 120}}}
	off := config.Account{Reserve: config.ReserveOff}
	cases := []struct {
		account config.Account
		left    time.Duration
		want    float64
	}{
		{def, 48 * time.Hour, 85}, {def, 48*time.Hour - time.Second, 90},
		{def, 12 * time.Hour, 90}, {def, 12*time.Hour - time.Second, 99}, {off, 150 * time.Hour, 99},
		// a custom reserve steps through its own caps, then gives way to the final cap
		{custom, 150 * time.Hour, 50}, {custom, 120 * time.Hour, 50}, {custom, 120*time.Hour - time.Second, 70},
		{custom, 48 * time.Hour, 70}, {custom, 48*time.Hour - time.Second, 99},
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

func TestTheOrderPutsAccountsThatCanTakeWorkFirst(t *testing.T) {
	// three accounts: X is chosen; G resets sooner than W but is at its cap, so W comes next
	c := testConfig()
	c.Accounts = append(c.Accounts, config.Account{AuthFile: "claude-x.json"})
	snaps := map[string]Snapshot{
		W:               snap(quota.Readings{quota.Main: rd(66, 100)}),
		G:               snap(quota.Readings{quota.Main: rd(98, 40)}),
		"claude-x.json": snap(quota.Readings{quota.Main: rd(10, 20)}),
	}
	d := Decide(snaps, c, now, nil)
	if d.Main.Account != "claude-x.json" || strings.Join(d.MainOrder, ",") != "claude-x.json,"+W+","+G {
		t.Fatalf("main %q order %v", d.Main.Account, d.MainOrder)
	}
}

func TestAnAccountsOwnFiveHourSteps(t *testing.T) {
	c := testConfig()
	// W: 60% until 2 hours before its 5-hour reset, then 90%
	c.Accounts[0].SessionSteps = []config.ReserveStep{{Cap: 60, Hours: 2}, {Cap: 90, Hours: 0}}
	early := map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 20), quota.Session: rd(70, 3)})}
	if ok, why := Eligibility(c.Accounts[0], early[W], true, quota.Main, now, c.Thresholds, false); ok || !strings.Contains(why, "60") {
		t.Fatalf("3 h before the reset W is held at 60%%: %v %q", ok, why)
	}
	late := map[string]Snapshot{W: snap(quota.Readings{quota.Main: rd(10, 20), quota.Session: rd(70, 1)})}
	if ok, _ := Eligibility(c.Accounts[0], late[W], true, quota.Main, now, c.Thresholds, false); !ok {
		t.Fatal("1 h before the reset W may go to 90%")
	}
	g := snap(quota.Readings{quota.Main: rd(10, 100), quota.Session: rd(75, 3)})
	if ok, _ := Eligibility(c.Accounts[1], g, true, quota.Main, now, c.Thresholds, false); !ok {
		t.Fatal("G uses the default 5-hour cap (98) and stays in")
	}
	// a closed window (no reset time) has the last step's cap
	if got := SessionCap(c.Accounts[0], nil, now, c.Thresholds); got != 90 {
		t.Fatalf("closed window cap %v", got)
	}
}

func TestNextCapIsTheFirstChangeBeforeTheReset(t *testing.T) {
	th := config.Default().Thresholds
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	custom := config.Account{Reserve: config.ReserveCustom, ReserveSteps: []config.ReserveStep{{Cap: 10, Hours: 72}, {Cap: 10, Hours: 48}, {Cap: 60, Hours: 24}}}
	// 10% until 3 days and again until 2 days: the cap first changes 2 days before the reset
	c, at, ok := NextCap(custom, false, now.Add(100*time.Hour), now, th)
	if !ok || c != 60 || !at.Equal(now.Add(52*time.Hour)) {
		t.Fatalf("%v %v %v", c, at, ok)
	}
	// the last step gives way to the final cap
	if c, at, ok := NextCap(custom, false, now.Add(10*time.Hour), now, th); ok {
		t.Fatalf("after the last step the final cap holds to the reset: %v %v", c, at)
	} else if c, at, ok := NextCap(custom, false, now.Add(30*time.Hour), now, th); !ok || c != th.FinalCap || !at.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("%v %v %v", c, at, ok)
	}
	// a 5-hour step that runs to the reset never changes; one that ends before it does
	five := config.Account{SessionSteps: []config.ReserveStep{{Cap: 60, Hours: 0}}}
	if _, _, ok := NextCap(five, true, now.Add(3*time.Hour), now, th); ok {
		t.Fatal("a step to the reset has no next cap")
	}
	five.SessionSteps = []config.ReserveStep{{Cap: 40, Hours: 2}, {Cap: 80, Hours: 0}}
	if c, at, ok := NextCap(five, true, now.Add(3*time.Hour), now, th); !ok || c != 80 || !at.Equal(now.Add(time.Hour)) {
		t.Fatalf("%v %v %v", c, at, ok)
	}
	// without a reserve the final cap holds all week
	if _, _, ok := NextCap(config.Account{Reserve: config.ReserveOff}, false, now.Add(100*time.Hour), now, th); ok {
		t.Fatal("no reserve, no next cap")
	}
}
