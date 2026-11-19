package engine

import (
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/probe"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

func (w *world) waste(account string) *WasteView {
	for _, a := range w.e.Status().Accounts {
		if a.AuthFile == account {
			return a.Waste
		}
	}
	return nil
}

func TestTheWasteIsRecordedAtAReset(t *testing.T) {
	w := newWorld(t, W, X)
	w.start()
	w.e.st.Account(X).Plan = probe.Plan{Name: "max_20x"} // 10 Pro plans
	w.use(X, 60, 10, 0)
	// the next request comes after the reset: its headers are already of the next week, and no
	// tick ran in between
	w.clock.t = w.clock.t.Add(11 * time.Hour)
	w.use(X, 1, 167, 0)
	v := w.waste(X)
	if v == nil || len(v.Resets) != 1 {
		t.Fatalf("one reset is recorded: %+v", v)
	}
	r := v.Resets[0]
	if !r.ResetsAt.Equal(t0.Add(10*time.Hour)) || r.Util != 60 || r.Weight != 10 || r.Wasted != 400 || !r.ObservedAt.Equal(t0) {
		t.Fatalf("the room left at the reset, in Pro plans' points: %+v", r)
	}
	if v.Week != 400 || v.Month != 400 {
		t.Fatalf("totals: %+v", v)
	}
	w.use(X, 2, 167, 0)
	w.e.Tick()
	if got := len(w.waste(X).Resets); got != 1 {
		t.Fatalf("the reset is recorded once, got %d", got)
	}
	if v := w.waste(W); v == nil || len(v.Resets) != 0 {
		t.Fatalf("an account that has not reset has no record: %+v", v)
	}
}

func TestTheWasteOfAResetMissedWhileTheEngineWasDownIsRecordedOnce(t *testing.T) {
	w := newWorld(t, W, X)
	w.start()
	w.use(X, 70, 10, 0) // the plan is unknown: it counts as one Pro plan
	w.e.Shutdown()
	w.clock.t = w.clock.t.Add(11 * time.Hour)
	w.start()
	v := w.waste(X)
	if v == nil || len(v.Resets) != 1 || v.Resets[0].Wasted != 30 || v.Resets[0].Weight != 1 {
		t.Fatalf("a new engine records the reset its state shows: %+v", v)
	}
	w.e.Shutdown()
	w.start()
	if got := len(w.waste(X).Resets); got != 1 {
		t.Fatalf("and records it once, got %d", got)
	}
}

func TestTheWasteTotalsCountTheLastSevenAndThirtyDays(t *testing.T) {
	w := newWorld(t, W, X)
	w.start()
	day := 24 * time.Hour
	for _, r := range []state.Reset{
		{ResetsAt: t0.Add(-40 * day), Wasted: 1000}, // too old for both
		{ResetsAt: t0.Add(-20 * day), Wasted: 100},
		{ResetsAt: t0.Add(-13 * day), Wasted: 20},
		{ResetsAt: t0.Add(-6 * day), Wasted: 3},
	} {
		w.e.st.Account(X).AddReset(r)
	}
	v := w.waste(X)
	if v.Week != 3 || v.Month != 123 {
		t.Fatalf("7 days %v, 30 days %v", v.Week, v.Month)
	}
	if !v.Resets[0].ResetsAt.Equal(t0.Add(-6 * day)) {
		t.Fatalf("the newest reset is listed first: %+v", v.Resets)
	}
}
