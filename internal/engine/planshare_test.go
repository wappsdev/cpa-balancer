package engine

import (
	"math"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/probe"
)

func near(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

func pointer(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

// metered is a world of an Ultra account (W) and a Pro account (X), with a history in which W's
// meter fell at slot 3 (a new window) and then climbed to 20 points over 5M units of use.
func metered(t *testing.T) (*world, history.Sessions, View) {
	t.Helper()
	w, rec := balanced(t)
	w.e.st.Account(W).Plan = probe.Plan{Name: "max_20x", Weight: 10}
	w.e.st.Account(X).Plan = probe.Plan{Name: "pro", Weight: 1}
	w.folder("-work-a", 1, 2)
	w.folder("-work-b", 3)
	at := func(n int) time.Time { return w.clock.t.Add(time.Duration(n)*history.SlotLength + time.Minute) }
	levels := func(n int, main float64) {
		rec.Snapshot(at(n), nil, map[string]history.Levels{W: {Main: &main}})
	}
	use := func(n int, account string, session int, in int64) {
		src := history.Source{Account: account, Model: Opus}
		if session > 0 {
			src.Session = sidN(session)
		}
		rec.AddTraffic(at(n), src, history.Traffic{Requests: 1, InputTokens: in})
	}
	levels(1, 10)
	levels(2, 99)
	use(3, W, 1, 2_000_000)
	use(3, W, 3, 2_000_000)
	use(3, W, 0, 1_000_000) // a request without a session id
	use(3, X, 1, 5_000_000) // on the Pro account
	levels(3, 4)
	levels(4, 20)
	now := at(4)
	s := w.e.ReadSessions(now)
	v := w.e.Status()
	w.e.AddSessions(&v, s)
	return w, s, v
}

func TestEveryUltraAccountIsCalibratedOnItsOwnMeterAndThePageIsToldHow(t *testing.T) {
	_, _, v := metered(t)
	if v.Meter == nil || v.Meter.Label != "Ultra" || v.Meter.Basis != history.BasisText {
		t.Fatalf("%+v", v.Meter)
	}
	byName := map[string]AccountView{}
	for _, a := range v.Accounts {
		byName[a.AuthFile] = a
	}
	// 20 points over 5M units of use
	if m := byName[W].Meter; m == nil || !m.Known || m.FromPrevious || !near(m.PerMillion, 4) {
		t.Fatalf("%+v", m)
	}
	if byName[W].PlanLabel != "Ultra" || byName[X].PlanLabel != "Pro" {
		t.Fatalf("%q %q", byName[W].PlanLabel, byName[X].PlanLabel)
	}
	if byName[X].Meter != nil {
		t.Fatalf("a Pro account learns nothing: %+v", byName[X].Meter)
	}
}

func TestTheProjectsAndTheRequestsWithoutOneAddUpToTheMeterAndTheProAccountStaysApart(t *testing.T) {
	_, s, v := metered(t)
	got := map[string]ProjectUsage{}
	var sum float64
	for _, p := range v.Affinity.Projects {
		got[p.Project] = p
		sum += pointer(p.PlanShare7d)
	}
	if !near(sum, 20) {
		t.Fatalf("every project and the group of no project add up to the meter: %v", sum)
	}
	a := got["-work-a"]
	if !near(pointer(a.PlanShare5h), 8) || !near(pointer(a.PlanShare7d), 8) || a.Elsewhere7d != 5_000_000 {
		t.Fatalf("2M of 5M units at 4 per million; the Pro account's 5M stays out: %+v", a)
	}
	if !near(pointer(got["-work-b"].PlanShare7d), 8) || !near(pointer(got[""].PlanShare7d), 4) {
		t.Fatalf("%+v", got)
	}
	for _, row := range s.Week.Sessions {
		if row.ID == sidN(1) && (!near(pointer(row.PlanShare5h), 8) || row.Elsewhere5h != 5_000_000) {
			t.Fatalf("a session row carries both windows' figures: %+v", row)
		}
	}
}

func TestWithoutAnyHistoryNothingIsMeteredAndEveryShareIsUnknownOrZero(t *testing.T) {
	w, _ := balanced(t)
	w.e.st.Account(W).Plan = probe.Plan{Name: "max_20x", Weight: 10}
	v := w.e.Status()
	w.e.AddSessions(&v, w.e.ReadSessions(w.clock.t))
	for _, a := range v.Accounts {
		if a.Meter != nil && a.Meter.Known {
			t.Fatalf("nothing to learn from: %+v", a)
		}
	}
}
