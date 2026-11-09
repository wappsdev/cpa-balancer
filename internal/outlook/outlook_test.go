package outlook

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func in(h float64) time.Time { return now.Add(time.Duration(h * float64(time.Hour))) }

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

// compute is the weekly outlook, which must have two accounts or more to combine.
func compute(t *testing.T, accounts []Account, measured *Pace) Outlook {
	t.Helper()
	o, ok := Compute(accounts, Week, measured, now)
	if !ok {
		t.Fatal("two accounts or more combine")
	}
	return o
}

// History with one rise per account: `rises` points in each of `n` half-hour steps ending now.
func rising(key string, n int, perStep float64) Series {
	s := Series{Step: 1800, Main: map[string][]*float64{}}
	var v float64
	for i := 0; i < n+1; i++ {
		s.T = append(s.T, now.Add(-time.Duration(n-i)*30*time.Minute).Unix())
		x := v
		s.Main[key] = append(s.Main[key], &x)
		v += perStep
	}
	return s
}

func TestWorkPace(t *testing.T) {
	// 6 steps of 1 point on an Ultra (weight 10) = 10 Pro points per half hour of work
	p := workPace(rising("a", 6, 1), map[string]float64{"a": 10}, 1800)
	if p == nil {
		t.Fatal("pace expected")
	}
	near(t, "active rate per hour", p.ActivePerHour, 20, 1e-6)
	// all hours of one day are 3 worked: 3h a day
	near(t, "hours per day", p.HoursPerDay, 3, 1e-6)
	// too little work: no pace
	if workPace(rising("a", 6, 1), map[string]float64{"a": 10}, 4*3600) != nil {
		t.Fatal("less than the minimum of work has no pace")
	}
}

func TestOutlookLastsWithMarginAndRunsOut(t *testing.T) {
	// A: 90 used, resets in 100 h. B: 100 used, resets in 20 h. Weights 1, cap 100.
	accounts := []Account{
		{Key: "a", Name: "A", Weight: 1, Cap: 100, Util: 90, ResetsAt: in(100)},
		{Key: "b", Name: "B", Weight: 1, Cap: 100, Util: 100, ResetsAt: in(20)},
	}
	pace := func(perHour float64) *Pace { return &Pace{PerHour: perHour} }
	// 0.4 points an hour: the 10 left last 25 h, past B's reset at 20 h
	o := compute(t, accounts, pace(0.4))
	if o.Verdict != "lasts" || o.Dry != nil {
		t.Fatalf("1/h: %+v", o)
	}
	// how much faster still lasts: the largest multiple of the pace, 0.5 an hour is the limit
	near(t, "headroom", o.Lasts, 1.25, 1e-3)
	// one point an hour is out at 10 h, but the refill after it lasts
	o = compute(t, accounts, pace(1))
	if o.Verdict != "out" || o.Dry == nil || o.Dry.Next == nil || !o.Dry.Next.Lasts {
		t.Fatalf("1/h: %+v %+v", o, o.Dry)
	}
	// 3 an hour: out at 10 h, B's reset at 20 h refills, G... the stretch after is dry again
	o = compute(t, accounts, pace(3))
	if o.Verdict != "out" || o.Dry == nil {
		t.Fatalf("3/h: %+v", o)
	}
	near(t, "runs out in hours", o.RunsOut.Sub(now).Hours(), 10.0/3, 1e-6)
	if o.Dry.Refill != "B" {
		t.Fatalf("refill: %s", o.Dry.Refill)
	}
	// the dry spell runs from the stop to the refill
	near(t, "dry hours", o.Dry.Seconds/3600, 20-10.0/3, 1e-6)
	// B's 100 from 20 h last 33.3 h; A's reset at 100 h ends that dry 46.7 h
	if o.Dry.Next == nil || o.Dry.Next.Lasts {
		t.Fatalf("next stretch: %+v", o.Dry.Next)
	}
	near(t, "next dry hours", o.Dry.Next.Seconds/3600, 140.0/3, 1e-6)
	// what lasts is below the pace: 0.5 an hour does
	if o.Lasts >= 1 || o.Lasts < 0.1 {
		t.Fatalf("lasts: %v", o.Lasts)
	}
}

func TestOutlookWithoutPaceUsesNothing(t *testing.T) {
	o := compute(t, []Account{{Key: "a", Weight: 1, Cap: 100, Util: 10, ResetsAt: in(50)}, {Key: "b", Weight: 1, Cap: 100, Util: 20, ResetsAt: in(60)}}, nil)
	// each window's own average: 10 points in 118 h and 20 in 108 h
	if o.Pace == nil || o.Pace.Source != "average" {
		t.Fatalf("pace: %+v", o.Pace)
	}
}

func TestBuildReadsStatusAndHistory(t *testing.T) {
	weekly, session := in(100), in(3)
	r := func(util float64) quota.Readings {
		return quota.Readings{quota.Main: {Util: util, ResetsAt: &weekly}, quota.Session: {Util: util / 2, ResetsAt: &session}}
	}
	caps := map[quota.Bucket]float64{quota.Main: 85, quota.Session: 90}
	v := engine.View{FinalCap: 100, Accounts: []engine.AccountView{
		{AuthFile: "a", Name: "A", Weight: 10, FiveHour: true, Caps: caps, Readings: r(20)},
		{AuthFile: "b", Name: "B", Weight: 1, FiveHour: true, Caps: caps, Readings: r(0)},
	}}
	o := Build(v, config.Config{}, history.Series{Step: 1800}, now)
	// no history: each window's own average; weekly against the final cap, not the account's
	if o.Week == nil || o.Week.Pace == nil || o.Week.Pace.Source != "average" {
		t.Fatalf("%+v", o.Week)
	}
	near(t, "weekly percent", o.Week.Percent, 200.0/1100*100, 1e-9)
	// 5-hour against each account's own cap: 10 of A's 900 Pro units
	if o.FiveHour == nil {
		t.Fatal("two accounts with a 5-hour limit combine")
	}
	near(t, "5-hour percent", o.FiveHour.Percent, 100.0/990*100, 1e-9)
	// too little history: no measured pace
	if o.Pace != nil {
		t.Fatalf("pace: %+v", o.Pace)
	}
	// an account without a 5-hour limit has no 5-hour room: one left, nothing to combine
	v.Accounts[1].FiveHour = false
	if o := Build(v, config.Config{}, history.Series{Step: 1800}, now); o.FiveHour != nil || o.Week == nil {
		t.Fatalf("%+v", o)
	}
}

func TestFiveHourClosedWindowIsFullRoomAndDefaultCap(t *testing.T) {
	session := in(2)
	v := engine.View{FinalCap: 99, Accounts: []engine.AccountView{
		{AuthFile: "a", Weight: 1, FiveHour: true, Readings: quota.Readings{quota.Session: {Util: 49, ResetsAt: &session}}},
		{AuthFile: "b", Weight: 1, FiveHour: true, Readings: quota.Readings{quota.Session: {Util: 30}}},
	}}
	o := Build(v, config.Config{}, history.Series{Step: 1800}, now).FiveHour
	// 98 a window when the balancer reports no cap; b's window is not running: 0 used of 98
	if o == nil {
		t.Fatal("five-hour outlook expected")
	}
	near(t, "percent", o.Percent, 49.0/196*100, 1e-9)
	// a's average: 49 in 3 h of its window, 16.3 an hour, so 49 left last 3 h > 2 h to its reset,
	// and b's full 98 after: the room lasts
	if o.Verdict == "out" {
		t.Fatalf("%+v", o)
	}
}

func TestOneAccountHasNothingToCombine(t *testing.T) {
	if _, ok := Compute([]Account{{Key: "a", Weight: 1, Cap: 100, Util: 10, ResetsAt: in(50)}}, Week, nil, now); ok {
		t.Fatal("one account combines with nothing")
	}
}

func TestWorkPaceWeighsEachAccount(t *testing.T) {
	// only the Ultra (weight 10) works, 1 point a step for 4 steps: 40 units over 2 h
	s := Series{Step: 1800, Main: map[string][]*float64{}}
	v := func(x float64) *float64 { return &x }
	for i, big := range []float64{10, 11, 12, 13, 14, 14} {
		s.T = append(s.T, now.Add(-time.Duration(5-i)*30*time.Minute).Unix())
		s.Main["big"] = append(s.Main["big"], v(big))
		s.Main["small"] = append(s.Main["small"], v(0))
	}
	p := workPace(s, map[string]float64{"big": 10, "small": 1}, 7200)
	if p == nil {
		t.Fatal("pace expected")
	}
	near(t, "active rate per hour", p.ActivePerHour, 20, 1e-9)
}

func TestFollowsResetsThroughToTheNextOne(t *testing.T) {
	// W is full and resets in 14 h; G is at 6% and resets in 137 h. At 1.91 points an hour
	// G's 94 and W's fresh 100 run out about 101.6 h from now, 35.4 h before G resets.
	accounts := []Account{
		{Key: "w", Name: "W", Weight: 1, Cap: 100, Util: 100, ResetsAt: in(14)},
		{Key: "g", Name: "G", Weight: 1, Cap: 100, Util: 6, ResetsAt: in(137)},
	}
	o := compute(t, accounts, &Pace{PerHour: 1.91})
	if o.Verdict != "out" || o.Dry == nil || o.Dry.Refill != "G" {
		t.Fatalf("%+v", o)
	}
	near(t, "runs out in hours", o.RunsOut.Sub(now).Hours(), 101.6, 0.05)
	near(t, "dry hours", o.Dry.Seconds/3600, 35.4, 0.05)
	// to last: G's 94 and W's refill (194) by G's reset at 137 h, so 1.416 an hour: 26% slower
	near(t, "slower", 1-o.Lasts, 0.26, 0.005)
	// at a third of that pace the refills keep up for the whole week
	if o := compute(t, accounts, &Pace{PerHour: 0.6}); o.Verdict == "out" {
		t.Fatalf("%+v", o)
	}
}

// gmail has used 40 and resets in 48 h; wapps has used 98 and resets in 120 h; both are weight 1, cap
// 100, and the week is 168 h.
func gmailAndWapps() []Account {
	return []Account{
		{Key: "gmail", Name: "gmail", Weight: 1, Cap: 100, Util: 40, ResetsAt: in(48)},
		{Key: "wapps", Name: "wapps", Weight: 1, Cap: 100, Util: 98, ResetsAt: in(120)},
	}
}

func TestRunsOutCountsTheResetsAndTheStretchAfterTheLastOne(t *testing.T) {
	// At 1.25 points an hour:
	//   0-48 h: 60 needed, gmail's 60 go; wapps keeps its 2. At 48 h gmail refills to 100 (not 60).
	//   48-120 h: 90 needed; wapps's 2, then 88 of gmail's 100: gmail 12 left. At 120 h wapps refills to 100.
	//   120 h on, no reset until gmail's second at 216 h: 112 left (12 + 100) last 89.6 h, so the room
	//   runs out at 209.6 h, 6.4 h before it. The room open now alone (62) would last 49.6 h.
	o := compute(t, gmailAndWapps(), &Pace{PerHour: 1.25})
	// one window (168 h) still holds: the verdict keeps its meaning, 1.5 an hour does not last
	if o.Verdict != "tight" || o.Dry != nil {
		t.Fatalf("%+v", o)
	}
	if o.RunsOut == nil {
		t.Fatal("the room runs out at 209.6 h, inside the two windows looked at")
	}
	near(t, "runs out in hours", o.RunsOut.Sub(now).Hours(), 209.6, 1e-6)
	if o.Horizon == nil || !o.Horizon.Equal(in(336)) {
		t.Fatalf("horizon: %v", o.Horizon)
	}
}

func TestRunsOutIsAbsentWhenTheRoomLastsBothWindows(t *testing.T) {
	// 0.5 an hour: every stretch holds through 336 h (gmail ends 18 at 216 h, wapps 64 at 288 h)
	o := compute(t, gmailAndWapps(), &Pace{PerHour: 0.5})
	if o.Verdict != "lasts" || o.RunsOut != nil {
		t.Fatalf("%+v", o)
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"runs_out", "room_seconds", "stops"} {
		if strings.Contains(string(b), gone) {
			t.Fatalf("%s in json: %s", gone, b)
		}
	}
}

func TestRunsOutForOutIsTheStopBeforeTheFirstRefill(t *testing.T) {
	// B (resets in 20 h) is empty; A has 10 left. At 3 an hour A's 10 last 10/3 h, the stop the
	// verdict "out" reports; the two windows looked at do not move it.
	accounts := []Account{
		{Key: "a", Name: "A", Weight: 1, Cap: 100, Util: 90, ResetsAt: in(100)},
		{Key: "b", Name: "B", Weight: 1, Cap: 100, Util: 100, ResetsAt: in(20)},
	}
	o := compute(t, accounts, &Pace{PerHour: 3})
	if o.Verdict != "out" || o.RunsOut == nil || o.Dry == nil {
		t.Fatalf("%+v", o)
	}
	near(t, "runs out in hours", o.RunsOut.Sub(now).Hours(), 10.0/3, 1e-6)
	// the dry spell starts where the room runs out
	near(t, "dry starts", o.Dry.At.Sub(*o.RunsOut).Hours(), o.Dry.Seconds/3600, 1e-6)
}

func TestOneAccountStillHasItsMeasuredPace(t *testing.T) {
	reset := in(100)
	v := engine.View{FinalCap: 100, Accounts: []engine.AccountView{
		{AuthFile: "a", Weight: 1, Readings: quota.Readings{quota.Main: {Util: 10, ResetsAt: &reset}}},
	}}
	s := rising("a", 6, 1)
	h := history.Series{Step: s.Step, T: s.T, Accounts: map[string]*history.AccountSeries{"a": {Main: s.Main["a"]}}}
	o := Build(v, config.Config{}, h, now)
	// nothing to combine, but the pane still has the measured pace
	if o.Week != nil || o.Pace == nil || o.Pace.Source != "measured" {
		t.Fatalf("%+v %+v", o.Week, o.Pace)
	}
	// each account's flag is its handoff now, so no account has a pace of its own
	if b, _ := json.Marshal(o.Pace); strings.Contains(string(b), "accounts") {
		t.Fatalf("%s", b)
	}
}
