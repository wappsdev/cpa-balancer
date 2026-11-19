package engine

import (
	"maps"
	"slices"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/probe"
	"github.com/wappsdev/cpa-balancer/internal/quota"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// The waste meter: at each weekly reset of the all-models week, the room an account still had
// (100 - its last reading) times its plan's size. It only measures; nothing is placed by it.

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
	last := map[time.Time]quota.Reading{}
	for _, r := range []quota.Reading{e.snaps[name].Readings[quota.Main], acc.Headers[quota.Main], acc.Probe[quota.Main]} {
		if r.ResetsAt == nil || r.ResetsAt.After(now) || r.Source == "estimate" || r.ObservedAt.IsZero() {
			continue
		}
		if seen, ok := last[*r.ResetsAt]; !ok || r.ObservedAt.After(seen.ObservedAt) {
			last[*r.ResetsAt] = r
		}
	}
	weight := probe.WeightOf(acc.Plan.Name)
	if weight == 0 {
		weight = 1 // as Headroom counts an unknown plan
	}
	for _, at := range slices.SortedFunc(maps.Keys(last), time.Time.Compare) {
		r := last[at]
		if acc.AddReset(state.Reset{ResetsAt: at, Util: r.Util, ObservedAt: r.ObservedAt, Weight: weight, Wasted: max(0, 100-r.Util) * weight}) {
			e.dirty = true
		}
	}
}
