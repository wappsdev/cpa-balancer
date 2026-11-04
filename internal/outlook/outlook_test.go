package outlook

import (
	"math"
	"testing"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/engine"
	"github.com/adnankurt/cpa-balancer/internal/history"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func in(h float64) time.Time { return now.Add(time.Duration(h * float64(time.Hour))) }

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
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
		{Key: "a", Name: "A", Weight: 1, Util: 90, ResetsAt: in(100)},
		{Key: "b", Name: "B", Weight: 1, Util: 100, ResetsAt: in(20)},
	}
	pace := func(perHour float64) *Pace { return &Pace{PerHour: perHour} }
	// 0.4 points an hour: the 10 left last 25 h, past B's reset at 20 h
	o := Compute(accounts, 100, pace(0.4), now)
	if o.Verdict != "lasts" || o.Dry != nil {
		t.Fatalf("1/h: %+v", o)
	}
	// how much faster still lasts: the largest multiple of the pace, 0.5 an hour is the limit
	near(t, "headroom", o.Lasts, 1.25, 1e-3)
	// one point an hour is out at 10 h, but the refill after it lasts
	o = Compute(accounts, 100, pace(1), now)
	if o.Verdict != "out" || o.Dry == nil || o.Dry.Next == nil || !o.Dry.Next.Lasts {
		t.Fatalf("1/h: %+v %+v", o, o.Dry)
	}
	// 3 an hour: out at 10 h, B's reset at 20 h refills, G... the stretch after is dry again
	o = Compute(accounts, 100, pace(3), now)
	if o.Verdict != "out" || o.Dry == nil {
		t.Fatalf("3/h: %+v", o)
	}
	near(t, "stops in hours", o.Stops.Sub(now).Hours(), 10.0/3, 1e-6)
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
	o := Compute([]Account{{Key: "a", Weight: 1, Util: 10, ResetsAt: in(50)}, {Key: "b", Weight: 1, Util: 20, ResetsAt: in(60)}}, 100, nil, now)
	// each window's own average: 10 points in 118 h and 20 in 108 h
	if o.Pace == nil || o.Pace.Source != "average" {
		t.Fatalf("pace: %+v", o.Pace)
	}
}

func TestBuildReadsStatusAndHistory(t *testing.T) {
	reset := in(100)
	r := func(util float64) quota.Readings {
		return quota.Readings{quota.Main: {Util: util, ResetsAt: &reset}}
	}
	v := engine.View{FinalCap: 100, Accounts: []engine.AccountView{
		{AuthFile: "a", Name: "A", Weight: 10, Readings: r(20)},
		{AuthFile: "b", Name: "B", Weight: 1, Readings: r(0)},
	}}
	o := Build(v, history.Series{Step: 1800}, now)
	// no history: each window's own average; A has used 20 of its 10 points' week
	if o.Pace == nil || o.Pace.Source != "average" || o.Percent <= 0 {
		t.Fatalf("%+v", o)
	}
}
