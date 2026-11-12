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

// build is Build with no configured accounts, which must not fail.
func build(t *testing.T, v engine.View, h history.Series) Both {
	t.Helper()
	o, err := Build(v, config.Config{}, h, now)
	if err != nil {
		t.Fatal(err)
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
	// to keep working until B's reset at 20 h, the 10 left must last 20 h: half the pace
	near(t, "avoids the dry spell", o.Dry.Lasts, 0.5, 1e-3)
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
	// the same 10 over the same 20 h: a sixth of 3 an hour
	near(t, "avoids the dry spell", o.Dry.Lasts, 0.5/3, 1e-3)
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
	o := build(t, v, history.Series{Step: 1800})
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
	if o := build(t, v, history.Series{Step: 1800}); o.FiveHour != nil || o.Week == nil {
		t.Fatalf("%+v", o)
	}
}

func TestFiveHourClosedWindowIsFullRoomAndDefaultCap(t *testing.T) {
	session := in(2)
	v := engine.View{FinalCap: 99, Accounts: []engine.AccountView{
		{AuthFile: "a", Weight: 1, FiveHour: true, Readings: quota.Readings{quota.Session: {Util: 49, ResetsAt: &session}}},
		{AuthFile: "b", Weight: 1, FiveHour: true, Readings: quota.Readings{quota.Session: {Util: 30}}},
	}}
	o := build(t, v, history.Series{Step: 1800}).FiveHour
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

// first has used 40 and resets in 48 h; second has used 98 and resets in 120 h; both are weight 1, cap
// 100, and the week is 168 h.
func firstAndSecond() []Account {
	return []Account{
		{Key: "first", Name: "first", Weight: 1, Cap: 100, Util: 40, ResetsAt: in(48)},
		{Key: "second", Name: "second", Weight: 1, Cap: 100, Util: 98, ResetsAt: in(120)},
	}
}

func TestRunsOutCountsTheResetsAndTheStretchAfterTheLastOne(t *testing.T) {
	// At 1.25 points an hour:
	//   0-48 h: 60 needed, first's 60 go; second keeps its 2. At 48 h first refills to 100 (not 60).
	//   48-120 h: 90 needed; second's 2, then 88 of first's 100: first 12 left. At 120 h second refills to 100.
	//   120 h on, no reset until first's next one at 216 h: 112 left (12 + 100) last 89.6 h, so the room
	//   runs out at 209.6 h, 6.4 h before it. The room open now alone (62) would last 49.6 h.
	o := compute(t, firstAndSecond(), &Pace{PerHour: 1.25})
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
	// 0.5 an hour: every stretch holds through 336 h (first ends 18 at 216 h, second 64 at 288 h)
	o := compute(t, firstAndSecond(), &Pace{PerHour: 0.5})
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
	o := build(t, v, h)
	// nothing to combine, but the pane still has the measured pace
	if o.Week != nil || o.Pace == nil || o.Pace.Source != "measured" {
		t.Fatalf("%+v %+v", o.Week, o.Pace)
	}
	// each account's flag is its handoff now, so no account has a pace of its own
	if b, _ := json.Marshal(o.Pace); strings.Contains(string(b), "accounts") {
		t.Fatalf("%s", b)
	}
}

// work is `rise` points in each of the half-hour steps from k half hours before now back to `to`.
// Step k ends k half hours before now: the last 5 h are k 0 to 9, day d is k 48d to 48d+47.
type work struct {
	from, to int
	rise     float64
}

// weekOf is a week of half-hour history ending now for one account "a" of weight 1.
func weekOf(spans ...work) Series {
	const n = 7 * 48
	at := make([]float64, n)
	for _, w := range spans {
		for k := w.from; k <= w.to; k++ {
			at[k] += w.rise
		}
	}
	s := Series{Step: 1800, Main: map[string][]*float64{}}
	var v float64
	for k := n; k >= 0; k-- {
		if k < n {
			v += at[k]
		}
		x := v
		s.T = append(s.T, now.Add(-time.Duration(k)*30*time.Minute).Unix())
		s.Main["a"] = append(s.Main["a"], &x)
	}
	return s
}

var one = []Account{{Key: "a", Weight: 1}}

// methods is the pace's methods by name.
func methods(p *Pace) map[string]Method {
	out := map[string]Method{}
	for _, m := range p.Methods {
		out[m.Name] = m
	}
	return out
}

func TestPeakWinsOnASpikyDay(t *testing.T) {
	// 80 points in 4 h three days ago; light days around it, and an hour of work in the last 5 h
	p := MeasurePace(weekOf(
		work{1, 2, 1}, work{20, 23, 1}, // the last day: 6 points in 3 h, 1 h of it in the last 5 h
		work{68, 71, 0.5}, work{116, 119, 0.5}, // the two days before: 2 points in 2 h each
		work{154, 161, 10}, // 80 points in 4 h
	), one, now)
	if p == nil {
		t.Fatal("pace expected")
	}
	m := methods(p)
	if len(m) != 3 {
		t.Fatalf("every method is present: %+v", p.Methods)
	}
	// the 24 h from k 163 to 116 hold the spike and the 2 points after it: 82 in 6 h
	near(t, "peak", m["peak"].PerHour, 82.0/24, 1e-9)
	near(t, "peak active", m["peak"].ActivePerHour, 82.0/6, 1e-9)
	near(t, "peak hours", m["peak"].HoursPerDay, 6, 1e-9)
	// the last day 6/24, the last 3 days 10/72
	near(t, "trend", m["trend"].PerHour, 1.5*6.0/24-0.5*10.0/72, 1e-9)
	// 2 points in the last 5 h's hour, 11 h worked over 4 days
	near(t, "now", m["now"].PerHour, 2*(11.0/4)/24, 1e-9)
	if p.Basis != "peak" {
		t.Fatalf("basis %s", p.Basis)
	}
	near(t, "pace", p.PerHour, 82.0/24, 1e-9)
	near(t, "active", p.ActivePerHour, 82.0/6, 1e-9)
}

func TestTrendWinsWhenTheUseSpeedsUp(t *testing.T) {
	p := MeasurePace(weekOf(
		work{0, 1, 0.5}, work{11, 18, 2}, // the last day: 17 points in 5 h, 1 h of it in the last 5 h
		work{68, 71, 0.5}, work{116, 119, 0.5}, // the two days before: 2 points in 2 h each
	), one, now)
	m := methods(p)
	if len(m) != 3 {
		t.Fatalf("every method is present: %+v", p.Methods)
	}
	// the last day 17/24, the last 3 days 21/72: the day's pace goes on rising by half the difference
	near(t, "trend", m["trend"].PerHour, 1.5*17.0/24-0.5*21.0/72, 1e-9)
	near(t, "peak", m["peak"].PerHour, 17.0/24, 1e-9)
	near(t, "now", m["now"].PerHour, 1*(9.0/3)/24, 1e-9)
	if p.Basis != "trend" {
		t.Fatalf("basis %s", p.Basis)
	}
	// the trend measures no worked time of its own
	near(t, "pace", p.PerHour, 1.5*17.0/24-0.5*21.0/72, 1e-9)
	if p.ActivePerHour != 0 || p.HoursPerDay != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestNowWinsOnAFastSession(t *testing.T) {
	p := MeasurePace(weekOf(
		work{0, 3, 4}, work{20, 39, 1}, // the last day: 16 points in the last 2 h, 20 in 10 h before
		work{48, 71, 1}, work{96, 119, 1}, // the two days before: 24 points in 12 h each
	), one, now)
	m := methods(p)
	if len(m) != 3 {
		t.Fatalf("every method is present: %+v", p.Methods)
	}
	// 8 points an hour while working, 36 h worked over 3 days
	near(t, "now", m["now"].PerHour, 8*12.0/24, 1e-9)
	near(t, "now active", m["now"].ActivePerHour, 8, 1e-9)
	near(t, "now hours", m["now"].HoursPerDay, 12, 1e-9)
	near(t, "trend", m["trend"].PerHour, 1.5*36.0/24-0.5*84.0/72, 1e-9)
	if p.Basis != "now" || m["peak"].PerHour >= m["now"].PerHour {
		t.Fatalf("basis %s, %+v", p.Basis, p.Methods)
	}
	near(t, "pace", p.PerHour, 8*12.0/24, 1e-9)
}

func TestTrendIsAbsentWhenTheUseSlowsOrTheDayHasTooLittleWork(t *testing.T) {
	// slower: the last day 4 points, the 3 days 12
	slow := MeasurePace(weekOf(work{20, 23, 1}, work{68, 71, 1}, work{116, 119, 1}), one, now)
	if _, ok := methods(slow)["trend"]; ok || slow.Basis != "peak" {
		t.Fatalf("%+v", slow)
	}
	// the last day has 1 h of work
	short := MeasurePace(weekOf(work{20, 21, 9}, work{68, 71, 0.5}), one, now)
	if _, ok := methods(short)["trend"]; ok {
		t.Fatalf("%+v", short.Methods)
	}
}

// absent is the pace's absent methods' reasons by name.
func absent(p *Pace) map[string]string {
	out := map[string]string{}
	for _, a := range p.Absent {
		out[a.Name] = a.Reason
	}
	return out
}

func TestAnAbsentMethodSaysWhy(t *testing.T) {
	slow := MeasurePace(weekOf(work{20, 23, 1}, work{68, 71, 1}, work{116, 119, 1}), one, now)
	if got := absent(slow); len(got) != 2 || got["trend"] != "the last 24 h are not faster than the last 3 days" ||
		got["now"] != "less than 30 min of work in the last 5 h" {
		t.Fatalf("%+v", slow.Absent)
	}
	short := MeasurePace(weekOf(work{20, 21, 9}, work{68, 71, 0.5}), one, now)
	if got := absent(short)["trend"]; got != "less than 2 h of work in the last 24 h" {
		t.Fatalf("%+v", short.Absent)
	}
	// every method present: none absent
	if p := MeasurePace(weekOf(work{1, 2, 1}, work{20, 23, 1}, work{68, 71, 0.5}, work{116, 119, 0.5}, work{154, 161, 10}), one, now); len(p.Absent) != 0 {
		t.Fatalf("%+v", p.Absent)
	}
	// the week's own pace (7d): all three absent, in the methods' order
	var spans []work
	for d := 0; d < 7; d++ {
		spans = append(spans, work{48*d + 20, 48*d + 21, 1})
	}
	p := MeasurePace(weekOf(spans...), one, now)
	if len(p.Absent) != 3 || p.Absent[0].Name != "peak" || p.Absent[1].Name != "trend" || p.Absent[2].Name != "now" ||
		p.Absent[0].Reason != "no 24 h with 2 h of work in the last 7 days" {
		t.Fatalf("%+v", p.Absent)
	}
}

func TestNowIsAbsentWithLessThanHalfAnHourInTheLast5Hours(t *testing.T) {
	// the work starts 5 h ago (step 10 ends then): nothing in the last 5 h
	p := MeasurePace(weekOf(work{10, 13, 1}), one, now)
	if _, ok := methods(p)["now"]; ok {
		t.Fatalf("%+v", p.Methods)
	}
	// one half-hour step inside them is enough
	if p := MeasurePace(weekOf(work{9, 13, 1}), one, now); methods(p)["now"].ActivePerHour != 2 {
		t.Fatalf("a step in the last 5 h: %+v", p.Methods)
	}
}

func TestEqualMethodsPreferNow(t *testing.T) {
	// 2 h a day at 2 an hour, on 4 days: now and peak are 4/24, no trend
	p := MeasurePace(weekOf(work{0, 3, 1}, work{48, 51, 1}, work{96, 99, 1}, work{144, 147, 1}), one, now)
	m := methods(p)
	near(t, "now", m["now"].PerHour, m["peak"].PerHour, 1e-12)
	if p.Basis != "now" {
		t.Fatalf("%+v", p)
	}
}

func TestWithNoMethodTheWeeksPaceIsTheFallback(t *testing.T) {
	// 1 h a day, never in the last 5 h: no 24 h with 2 h of work, but 7 h in the week
	var spans []work
	for d := 0; d < 7; d++ {
		spans = append(spans, work{48*d + 20, 48*d + 21, 1})
	}
	p := MeasurePace(weekOf(spans...), one, now)
	if p == nil || p.Basis != "7d" || len(p.Methods) != 0 {
		t.Fatalf("%+v", p)
	}
	near(t, "pace", p.PerHour, 2*1.0/24, 1e-9)
	// under 2 h in the week: no pace at all
	if p := MeasurePace(weekOf(work{20, 21, 1}, work{68, 68, 1}), one, now); p != nil {
		t.Fatalf("%+v", p)
	}
}

func TestPaceJSONNamesItsBasisAndMethods(t *testing.T) {
	p := MeasurePace(weekOf(work{0, 1, 0.5}, work{11, 18, 2}, work{68, 71, 0.5}, work{116, 119, 0.5}), one, now)
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"basis":"trend"`, `"methods":[{"name":"peak","per_hour":`, `{"name":"trend","per_hour":`, `"active_per_hour":0,"hours_per_day":0}`, `{"name":"now"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s not in %s", want, b)
		}
	}
}
