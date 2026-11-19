package state

import "time"

// MaxResets bounds the weekly resets kept per account; the oldest goes first.
const MaxResets = 12

// sameReset is how close two reset times are to be the same reset seen from two sources.
const sameReset = time.Minute

// Reset is an account's all-models week at its reset: what was left of it, unused.
type Reset struct {
	ResetsAt   time.Time `json:"resets_at"`
	Util       float64   `json:"util"`        // the window's last reading, in percent
	ObservedAt time.Time `json:"observed_at"` // when that reading was taken
	Weight     float64   `json:"weight"`      // the plan's size in Pro plans; 1 when unknown
	Wasted     float64   `json:"wasted"`      // (100 - util) times the weight: Pro plans' points never used
	// Estimated: no real reading was taken in the week, so it counts as unused (Util 0) and
	// ObservedAt is empty. An idle account's week is all wasted.
	Estimated bool `json:"estimated,omitempty"`
}

// AddReset records a reset unless it is at or before the latest one recorded; it reports whether
// it did.
func (a *Account) AddReset(r Reset) bool {
	if n := len(a.Resets); n > 0 && r.ResetsAt.Before(a.Resets[n-1].ResetsAt.Add(sameReset)) {
		return false
	}
	a.Resets = append(a.Resets, r)
	if len(a.Resets) > MaxResets {
		a.Resets = a.Resets[len(a.Resets)-MaxResets:]
	}
	return true
}
