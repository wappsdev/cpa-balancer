package history

import "testing"

func TestARiseWithNoUseOfTheProxyNearItIsOutsideAndNotSpreadOverTheSessions(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	level(r, 2, "u", 3)
	level(r, 3, "u", 8) // rises after the use in the slot before: the meter lags by a slot or two
	level(r, 40, "u", 8)
	level(r, 41, "u", 14) // 6 points between two readings a slot apart, with no use in the hour either side
	spend(r, 60, "u", "s", input(1*mil))
	level(r, 60, "u", 20)

	s := r.SessionsFor(at(60), ultra)
	c := s.Calibrations["u"]
	if !near(c.Outside, 6) || !near(c.PerMillion, 7) {
		t.Fatalf("14 points in the proxy over 2M: 7 per million, and 6 outside it: %+v", c)
	}
	if got := pct(row(t, s, "s").PlanShare7d); !near(got, 14) {
		t.Fatalf("the sessions add up to the meter less what came from outside: %v", got)
	}
}

func TestARiseAcrossAGapInTheHistoryIsNotCalledOutside(t *testing.T) {
	r := NewRecorder(t.TempDir())
	opened(r)
	spend(r, 2, "u", "s", input(1*mil))
	level(r, 2, "u", 5)
	level(r, 50, "u", 9) // the balancer was not running between these: use may have gone unseen
	c := r.SessionsFor(at(50), ultra).Calibrations["u"]
	if c.Outside != 0 || !near(c.PerMillion, 9) {
		t.Fatalf("%+v", c)
	}
}

func TestAMeterThatFallsFromFiveToZeroHasReset(t *testing.T) {
	r := NewRecorder(t.TempDir())
	level(r, 0, "u", 5)
	spend(r, 1, "u", "s", input(1*mil))
	level(r, 1, "u", 0)
	level(r, 2, "u", 8)
	level(r, 3, "u", 7) // a reading corrects itself by a point: not a reset
	spend(r, 3, "u", "s", input(1*mil))
	level(r, 4, "u", 10)

	c := r.SessionsFor(at(4), ultra).Calibrations["u"]
	if !c.Known || !near(c.PerMillion, 5) {
		t.Fatalf("one window of 10 points over 2M: %+v", c)
	}
}
