package history

import (
	"math"
	"testing"
	"time"
)

// at is the middle of the nth slot after t0.
func at(n int) time.Time { return t0.Add(time.Duration(n)*SlotLength + time.Minute) }

const mil = 1_000_000

// level records an account's meter (its main limit's utilization) at the end of a slot.
func level(r *Recorder, n int, account string, main float64) {
	r.Snapshot(at(n), nil, map[string]Levels{account: {Main: f(main)}})
}

func spend(r *Recorder, n int, account, session string, t Traffic) {
	r.AddTraffic(at(n), Source{Account: account, Model: "opus", Session: session}, t)
}

func input(n int64) Traffic { return Traffic{Requests: 1, InputTokens: n} }

func near(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

func pct(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

var ultra = map[string]bool{"u": true}

// row finds a root session of the 7-day window.
func row(t *testing.T, s Sessions, id string) SessionUsage {
	t.Helper()
	for _, r := range s.Week.Sessions {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", id, s.Week.Sessions)
	return SessionUsage{}
}

// A reset at slot 2 (the level falls from 90 to 3), with one earlier window nobody saw open.
func opened(r *Recorder) {
	level(r, 0, "u", 10)
	level(r, 1, "u", 90)
}

func TestAWindowOpensWhereTheMeterFallsAndItsFactorIsTheMeterOverWhatWasUsedSince(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s1", input(1*mil)) // the slot of the fall belongs to the new window
	level(r, 2, "u", 3)
	spend(r, 3, "u", "s2", input(3*mil))
	level(r, 3, "u", 8)

	s := r.SessionsFor(at(3), ultra)
	// 8 points over 4M: 2 points per million
	if c := s.Calibrations["u"]; !c.Known || c.FromPrevious || !near(c.PerMillion, 2) {
		t.Fatalf("%+v", c)
	}
	if got := pct(row(t, s, "s1").PlanShare7d); !near(got, 2) {
		t.Fatalf("1M of 4M: %v", got)
	}
	if got := pct(row(t, s, "s2").PlanShare7d); !near(got, 6) {
		t.Fatalf("3M of 4M: %v", got)
	}
}

func TestUseInAWindowNobodySawOpenCannotBePriced(t *testing.T) {
	r := NewRecorder(t.TempDir())
	level(r, 0, "u", 10)
	spend(r, 1, "u", "s1", input(1*mil)) // the first window began before the history did
	level(r, 1, "u", 60)

	s := r.SessionsFor(at(1), ultra)
	if s.Calibrations["u"].Known {
		t.Fatalf("%+v", s.Calibrations["u"])
	}
	if got := row(t, s, "s1"); got.PlanShare7d != nil {
		t.Fatalf("an unknown share is nil, not a guess: %v", *got.PlanShare7d)
	}
}

func TestEachSlotIsPricedByTheFactorOfItsOwnWindowAcrossAReset(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	level(r, 2, "u", 6)
	spend(r, 3, "u", "s", input(4*mil))
	level(r, 3, "u", 50) // window 1: 50 points over 5M = 10 per million
	spend(r, 4, "u", "s", input(1*mil))
	level(r, 4, "u", 0) // the reset: the meter falls to zero
	spend(r, 5, "u", "s", input(1*mil))
	level(r, 5, "u", 12) // window 2: 12 points over 2M = 6 per million

	s := r.SessionsFor(at(5), ultra)
	if got := pct(row(t, s, "s").PlanShare7d); !near(got, 5*10+2*6) {
		t.Fatalf("the part before the reset at 10, the part after at 6: %v", got)
	}
	if c := s.Calibrations["u"]; !near(c.PerMillion, 6) || c.FromPrevious {
		t.Fatalf("%+v", c)
	}
}

func TestAWindowWithFewerThanFivePointsUsesThePreviousWindowsFactor(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "old", input(5*mil))
	level(r, 2, "u", 50) // 10 per million
	spend(r, 3, "u", "new", input(1*mil))
	level(r, 3, "u", 4) // the new window: 4 points, too few: whole points are too coarse
	s := r.SessionsFor(at(3), ultra)
	if c := s.Calibrations["u"]; !c.Known || !c.FromPrevious || !near(c.PerMillion, 10) {
		t.Fatalf("%+v", c)
	}
	if got := pct(row(t, s, "new").PlanShare7d); !near(got, 10) {
		t.Fatalf("not 4: %v", got)
	}
}

func TestWithNoWindowToFallBackOnTheFactorIsUnknown(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	level(r, 2, "u", 4)
	s := r.SessionsFor(at(2), ultra)
	if s.Calibrations["u"].Known || row(t, s, "s").PlanShare7d != nil {
		t.Fatalf("%+v", s.Calibrations)
	}
}

func TestTheSessionsAndTheRequestsWithoutOneAddUpToTheMeterOfEveryAccount(t *testing.T) {
	r := NewRecorder(t.TempDir())
	both := map[string]bool{"u": true, "v": true}
	meters := func(n int, u, v float64) { // the slots come in time order
		r.Snapshot(at(n), nil, map[string]Levels{"u": {Main: f(u)}, "v": {Main: f(v)}})
	}
	meters(0, 10, 10)
	meters(1, 99, 99)
	spend(r, 2, "u", "s1", input(1*mil))
	spend(r, 2, "u", "", input(2*mil)) // no session
	spend(r, 2, "v", "s1", input(3*mil))
	meters(2, 3, 3)
	r.AddTraffic(at(3), Source{Account: "v", Model: "opus", Session: "s1/x", Parent: "s1"}, input(2*mil))
	spend(r, 3, "v", "s2", input(1*mil))
	meters(3, 17, 41)

	s := r.SessionsFor(at(3), both)
	var sum float64
	for _, root := range s.Week.AllRoots {
		if root.Share.Unknown {
			t.Fatalf("%+v", root)
		}
		sum += root.Share.Points
	}
	if !near(sum, 17+41) {
		t.Fatalf("the rows add up to the meters of the two accounts: %v", sum)
	}
	if got := pct(row(t, s, "s1").PlanShare7d); !near(got, 17.0/3+41.0*5/6) {
		t.Fatalf("a session on two accounts pays each at its own factor: %v", got)
	}
}

func TestTheSharesOfTheLastFiveHoursAndOfTheDaysAreEachCountedInTheirOwnWindow(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	level(r, 2, "u", 5) // 5 per million
	now := 6 * 12 * SlotLength
	spend(r, int(now/SlotLength), "u", "s", input(1*mil))
	level(r, int(now/SlotLength), "u", 10)

	s := r.SessionsFor(at(int(now/SlotLength)), ultra)
	if got := pct(row(t, s, "s").PlanShare7d); !near(got, 10) {
		t.Fatalf("%v", got)
	}
	if got := pct(s.FiveHour.Sessions[0].PlanShare5h); !near(got, 5) {
		t.Fatalf("the last 5 hours hold the later million only: %v", got)
	}
}

func TestASessionThatIsOnlyInOneWindowHasNoFigureForTheOther(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "old", input(1*mil)) // the week only
	level(r, 2, "u", 5)
	now := int(6 * 12 * SlotLength / SlotLength)
	spend(r, now, "u", "new", input(1*mil)) // both windows
	level(r, now, "u", 10)

	s := r.SessionsFor(at(now), ultra)
	if old := row(t, s, "old"); old.PlanShare5h != nil || pct(old.PlanShare7d) <= 0 {
		t.Fatalf("a session with no row in the 5 hours has no 5-hour figure, not 0: %+v", old)
	}
	if fresh := row(t, s, "new"); pct(fresh.PlanShare5h) <= 0 || pct(fresh.PlanShare7d) <= 0 {
		t.Fatalf("a session in both has both: %+v", fresh)
	}
}

func TestASessionCutFromTheWeeksListStillHasItsFigureForTheWeek(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "big", input(8*mil))
	spend(r, 2, "u", "small", input(2*mil))
	level(r, 2, "u", 10) // 1 per million
	s := r.sessionsOf(at(2), ultra, 1)
	if len(s.Week.Sessions) != 1 || s.Week.Sessions[0].ID != "big" {
		t.Fatalf("setup: %+v", s.Week.Sessions)
	}
	for _, root := range s.Week.AllRoots {
		if root.ID == "small" && !near(root.Share.Points, 2) {
			t.Fatalf("%+v", root)
		}
	}
	if got := pct(s.FiveHour.Sessions[1].PlanShare7d); !near(got, 2) { // listed in the 5 hours, not in the week's list
		t.Fatalf("%v", got)
	}
}

func TestSubagentsHaveTheirOwnShare(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	r.AddTraffic(at(2), Source{Account: "u", Model: "opus", Session: "s/x", Parent: "s"}, input(3*mil))
	level(r, 2, "u", 8) // 2 per million
	s := r.SessionsFor(at(2), ultra)
	parent := row(t, s, "s")
	if got := pct(parent.PlanShare7d); !near(got, 8) || len(parent.Subagents) != 1 || !near(pct(parent.Subagents[0].PlanShare7d), 6) {
		t.Fatalf("the parent counts its subagent: %+v", parent)
	}
}

func TestUseOnAnAccountWithoutTheMeterIsCountedApartAndNeverPriced(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	spend(r, 2, "p", "s", Traffic{Requests: 1, InputTokens: 700_000, OutputTokens: 100_000, CacheCreationTokens: 50_000, CacheReadTokens: 9 * mil})
	level(r, 2, "u", 5)
	level(r, 2, "p", 30)

	s := r.SessionsFor(at(2), ultra)
	got := row(t, s, "s")
	if !near(pct(got.PlanShare7d), 5) || got.Elsewhere7d != 850_000 {
		t.Fatalf("the other account's use stays out of the percent: %+v", got)
	}
	if _, ok := s.Calibrations["p"]; ok {
		t.Fatal("an account that is not metered learns nothing")
	}
}

func TestTheBasisCountsCacheWritesInFullAndCacheReadsByASmallFraction(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", Traffic{Requests: 1, InputTokens: 1 * mil, OutputTokens: 1 * mil, CacheCreationTokens: 1 * mil, CacheReadTokens: 100 * mil})
	level(r, 2, "u", 9)
	// 1M + 1M + 1M + 100M * 0.015 = 4.5M units for 9 points
	if c := r.SessionsFor(at(2), ultra).Calibrations["u"]; !near(c.PerMillion, 2) || CacheReadWeight != 0.015 {
		t.Fatalf("%+v", c)
	}
}

func TestTheWindowLengthIsTheMedianGapBetweenResets(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	for _, n := range []int{2, 6, 10, 16} { // fall at 2, 6, 10 and 16: gaps of 4, 4 and 6 slots
		level(r, n-1, "u", 90)
		level(r, n, "u", 1)
	}
	if got := r.SessionsFor(at(16), ultra).Calibrations["u"].Length; got != 4*SlotLength {
		t.Fatalf("%v", got)
	}
	if got := NewRecorder(t.TempDir()); got.SessionsFor(at(0), ultra).Calibrations["u"].Length != 0 {
		t.Fatal("no fall seen, no length")
	}
}

func TestARequestThatCostNothingNeedsNoFactor(t *testing.T) {
	r := NewRecorder(t.TempDir())
	level(r, 0, "u", 10)
	spend(r, 1, "u", "s", Traffic{Requests: 1}) // in a window nobody saw open
	s := r.SessionsFor(at(1), ultra)
	if got := row(t, s, "s"); got.PlanShare7d == nil || *got.PlanShare7d != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestNoMeteredAccountsIsTheCheapPath(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	level(r, 2, "u", 5)
	s := r.Sessions(at(2))
	if got := row(t, s, "s"); got.PlanShare7d != nil || got.Elsewhere7d != 0 || len(s.Calibrations) != 0 {
		t.Fatalf("%+v", got)
	}
}
