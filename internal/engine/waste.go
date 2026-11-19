package engine

import (
	"slices"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/probe"
	"github.com/wappsdev/cpa-balancer/internal/quota"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// The waste meter: at each weekly reset of the all-models week, the room an account still had
// (100 - its last reading) times its plan's size; a week with no real reading counts as all of it,
// marked estimated. It only measures; nothing is placed by it.

// WasteView is an account's last resets, the newest first, and the points wasted in the last 7
// and 30 days.
type WasteView struct {
	Resets []state.Reset `json:"resets"`
	Week   float64       `json:"week"`
	Month  float64       `json:"month"`
}

func wasteView(acc *state.Account, now time.Time) *WasteView {
	v := &WasteView{Resets: []state.Reset{}}
	if acc == nil {
		return v
	}
	v.Resets = slices.Clone(acc.Resets)
	slices.Reverse(v.Resets)
	for _, r := range v.Resets {
		age := now.Sub(r.ResetsAt)
		if age <= 7*24*time.Hour {
			v.Week += r.Wasted
		}
		if age <= 30*24*time.Hour {
			v.Month += r.Wasted
		}
	}
	return v
}

// noteResets records the weekly resets an account went through since the last look. The window's
// last reading is the previous snapshot's, or one still stored from before the reset (after a
// restart there is no snapshot, and a request's headers replace the stored one before this runs).
// Callers hold e.mu.
func (e *Engine) noteResets(name string, acc *state.Account, now time.Time) {
	var closed []quota.Reading
	for _, r := range []quota.Reading{e.snaps[name].Readings[quota.Main], acc.Headers[quota.Main], acc.Probe[quota.Main]} {
		if r.ResetsAt != nil && !r.ResetsAt.After(now) && r.Source != "estimate" && !r.ObservedAt.IsZero() {
			closed = append(closed, r)
		}
	}
	slices.SortFunc(closed, func(x, y quota.Reading) int { return x.ResetsAt.Compare(*y.ResetsAt) })
	weight := probe.WeightOf(acc.Plan.Name)
	if weight == 0 {
		weight = 1 // as Headroom counts an unknown plan
	}
	// Sources give one reset at slightly different times (whole seconds, fractions): readings within
	// sameReset of a group's first are one window, and its newest reading is the last one.
	for i := 0; i < len(closed); {
		last, j := closed[i], i+1
		for ; j < len(closed) && closed[j].ResetsAt.Sub(*closed[i].ResetsAt) < sameReset; j++ {
			if closed[j].ObservedAt.After(last.ObservedAt) {
				last = closed[j]
			}
		}
		at := *closed[i].ResetsAt
		if acc.AddReset(state.Reset{ResetsAt: at, Util: last.Util, ObservedAt: last.ObservedAt, Weight: weight, Wasted: max(0, 100-last.Util) * weight}) {
			e.dirty = true
		}
		i = j
	}
	if len(closed) == 0 {
		return
	}
	// The weeks after the last one with a real reading that have closed since had none: the account
	// was idle, so all of each week is wasted. Only as many as the state keeps are looked at.
	next := closed[len(closed)-1].ResetsAt.Add(week)
	if weeks := int(now.Sub(next) / week); weeks >= state.MaxResets {
		next = next.Add(time.Duration(weeks-state.MaxResets+1) * week)
	}
	for ; !next.After(now); next = next.Add(week) {
		if acc.AddReset(state.Reset{ResetsAt: next, Weight: weight, Wasted: 100 * weight, Estimated: true}) {
			e.dirty = true
		}
	}
}
