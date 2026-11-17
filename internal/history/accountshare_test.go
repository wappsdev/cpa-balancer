package history

import "testing"

// twoMeters records a window that opened at slot 2 for "u" (the aggregate account) and "p" (an
// account that is priced but not in the aggregate), both inside the 7 days. u: 8 points over 4M
// = 2 per million; p: 20 points over 4M = 5 per million.
func twoMeters(r *Recorder) {
	both := func(n int, u, p float64) {
		r.Snapshot(at(n), nil, map[string]Levels{"u": {Main: f(u)}, "p": {Main: f(p)}})
	}
	both(0, 10, 10)
	both(1, 90, 90)
	spend(r, 2, "u", "a", input(1*mil))
	spend(r, 2, "p", "a", input(2*mil))
	spend(r, 2, "p", "", input(1*mil)) // no session
	both(2, 3, 2)
	spend(r, 3, "u", "b", input(3*mil))
	spend(r, 3, "p", "b", input(1*mil))
	both(3, 8, 20)
}

var priced = map[string]bool{"u": true, "p": true}

func TestARowIsSplitByAccountEachPricedAtItsOwnFactor(t *testing.T) {
	r := NewRecorder(t.TempDir())
	twoMeters(r)
	s := r.SessionsFor(at(3), ultra, priced)
	a := s.Week.Shares["a"].ByAccount
	if !near(a["u"].Points, 2) || !near(a["u"].Units, 1*mil) || !a["u"].Priced || a["u"].Unknown {
		t.Fatalf("u: %+v", a["u"])
	}
	if !near(a["p"].Points, 10) || !near(a["p"].Units, 2*mil) || !a["p"].Priced {
		t.Fatalf("p: %+v", a["p"])
	}
	if !near(s.Factors["p"].PerMillion, 5) || !s.Factors["p"].Known {
		t.Fatalf("%+v", s.Factors["p"])
	}
	if _, ok := s.Calibrations["p"]; ok {
		t.Fatal("the aggregate calibrations stay those of the aggregate accounts")
	}
}

func TestThePercentOfTheAggregateAccountsDoesNotChangeWhenAnotherAccountIsPriced(t *testing.T) {
	r := NewRecorder(t.TempDir())
	twoMeters(r)
	alone := r.SessionsFor(at(3), ultra, ultra)
	with := r.SessionsFor(at(3), ultra, priced)
	for _, id := range []string{"a", "b", ""} {
		x, y := alone.Week.Shares[id], with.Week.Shares[id]
		if !near(x.Points, y.Points) || x.Unknown != y.Unknown {
			t.Fatalf("%q: %+v vs %+v", id, x, y)
		}
	}
	if with.Week.Shares["a"].Elsewhere != 2*mil || with.Week.Shares["b"].Elsewhere != 1*mil {
		t.Fatalf("an account outside the aggregate is still tokens elsewhere: %+v", with.Week.Shares)
	}
}

func TestTheRowsOfEachAccountAddUpToItsMeterWithWhatCameFromOutside(t *testing.T) {
	r := NewRecorder(t.TempDir())
	twoMeters(r)
	// 6 points on u's meter between two readings a slot apart, with no use of the proxy near them
	r.Snapshot(at(40), nil, map[string]Levels{"u": {Main: f(8)}, "p": {Main: f(20)}})
	r.Snapshot(at(41), nil, map[string]Levels{"u": {Main: f(14)}, "p": {Main: f(20)}})
	s := r.SessionsFor(at(41), ultra, priced)
	sum := map[string]float64{}
	for _, root := range s.Week.AllRoots { // every root, project "" included, not the listed top
		for name, part := range root.Share.ByAccount {
			if part.Unknown {
				t.Fatalf("%s %+v", root.ID, part)
			}
			sum[name] += part.Points
		}
	}
	if got := sum["u"] + s.Factors["u"].Outside; !near(got, s.Factors["u"].Util) || !near(got, 14) {
		t.Fatalf("u: rows %v + outside %v = meter %v", sum["u"], s.Factors["u"].Outside, s.Factors["u"].Util)
	}
	if got := sum["p"] + s.Factors["p"].Outside; !near(got, 20) {
		t.Fatalf("p: %v", got)
	}
}

func TestAnAccountWithNoFactorOrNoMeterHasNoPercent(t *testing.T) {
	r := NewRecorder(t.TempDir())
	level(r, 0, "p", 10)
	spend(r, 1, "p", "a", input(1*mil)) // a window nobody saw open: no factor
	level(r, 1, "p", 60)
	spend(r, 1, "x", "a", input(2*mil)) // an account the balancer prices nothing of
	s := r.SessionsFor(at(1), ultra, map[string]bool{"p": true})
	parts := s.Week.Shares["a"].ByAccount
	if !parts["p"].Priced || !parts["p"].Unknown || parts["x"].Priced {
		t.Fatalf("%+v", parts)
	}
	uses := AccountUses(s.FiveHour.Shares["a"], s.Week.Shares["a"])
	if uses["p"].Share7d != nil || uses["x"].Share7d != nil || uses["x"].Tokens7d != 2*mil || uses["p"].Tokens7d != 1*mil {
		t.Fatalf("%+v", uses)
	}
}

func TestTheAccountsOfARowDoNotShareOneMapWhenRowsAreAdded(t *testing.T) {
	var a, b Share
	a.Add(Share{ByAccount: map[string]AccountPart{"u": {Points: 1, Units: 10, Priced: true}}})
	b = a // a copy, as a node's share is copied into a row
	a.Add(Share{ByAccount: map[string]AccountPart{"u": {Points: 2, Units: 20, Priced: true}}})
	if b.ByAccount["u"].Points != 1 || a.ByAccount["u"].Points != 3 || a.ByAccount["u"].Units != 30 {
		t.Fatalf("a: %+v b: %+v", a.ByAccount, b.ByAccount)
	}
}

func TestASessionRowCarriesItsAccountUseOfBothWindows(t *testing.T) {
	r := NewRecorder(t.TempDir())
	twoMeters(r)
	s := r.SessionsFor(at(3), ultra, priced)
	got := row(t, s, "b").ByAccount
	if got["u"].Tokens7d != 3*mil || got["u"].Tokens5h != 3*mil || !near(pct(got["u"].Share7d), 6) || !near(pct(got["p"].Share7d), 5) {
		t.Fatalf("%+v", got)
	}
}
