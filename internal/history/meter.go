package history

import (
	"fmt"
	"slices"
	"time"
)

// An account's `main` limit has its own meter: a utilization in percent that opens each window at
// zero and climbs with use. The balancer cannot see what an account's use costs in percent, but it
// can learn it from the meter itself: how far the meter climbed in a window, over what the
// account served since the window opened. That is the account's factor, and a session's share is
// its use on each account times that account's factor. Nothing is shared between accounts, as
// plans and models make a token cost something different on each.
const (
	// CacheReadWeight is what a cache-read token counts against an input token. Fitted on the
	// history of two accounts: the factors of their spans (windows so far) spread by 15% on
	// input and output, by 8.7% with cache writes added, and by 5.5% with this fraction of the
	// cache reads on top.
	CacheReadWeight = 0.015
	// MinMeterPoints is the least a window's meter must show to give a factor: it moves in whole
	// points, so a lower reading is mostly rounding.
	MinMeterPoints = 5.0
	// MeterLookback is how far back the windows are read: the current window and the one before it.
	MeterLookback = 15 * 24 * time.Hour
	// resetFall is how far the meter falls between two readings for a new window to have opened.
	// Readings correct themselves by a point or so; a reset falls to about zero.
	resetFall = 5.0
)

// BasisText says what one unit of use is, for the page.
var BasisText = fmt.Sprintf("input + output + cache writes + %g%% of cache reads", CacheReadWeight*100)

// basis is the use a request counts for against the meter.
func basis(t Traffic) float64 {
	return float64(t.InputTokens+t.OutputTokens+t.CacheCreationTokens) + CacheReadWeight*float64(t.CacheReadTokens)
}

// Share is what a set of requests used of the metered accounts' limit windows.
type Share struct {
	Points    float64 // percent of one account's limit window, summed over the accounts that served it
	Unknown   bool    // some use on a metered account fell in a window with no factor, so Points falls short
	Elsewhere int64   // tokens on accounts that have no meter: not in Points
}

func (s *Share) add(o Share) {
	s.Points += o.Points
	s.Unknown = s.Unknown || o.Unknown
	s.Elsewhere += o.Elsewhere
}

// Percent is the share, or nil when it is not known.
func (s Share) Percent() *float64 {
	if s.Unknown {
		return nil
	}
	p := s.Points
	return &p
}

// Calibration is what one account's meter says now.
type Calibration struct {
	PerMillion   float64       // percent of the account's limit window per million units of use; 0 unless Known
	Known        bool          // false: no window to learn from yet, and shares on this account are unknown
	FromPrevious bool          // the current window shows too little, so the previous window's factor is in use
	Length       time.Duration // between two resets, measured from the history; 0 until two have been seen
	Util         float64       // the meter's last reading
}

type meterWindow struct {
	start       time.Time
	observed    bool // the meter was seen to fall at its start; the first window of the history began before it
	util, usage float64
}

// factor is the percent per unit of use in window i: its own when it shows enough, else the
// previous window's.
func factor(ws []meterWindow, i int) (f float64, previous, ok bool) {
	learned := func(w meterWindow) (float64, bool) {
		if !w.observed || w.util < MinMeterPoints || w.usage <= 0 {
			return 0, false
		}
		return w.util / w.usage, true
	}
	if f, ok := learned(ws[i]); ok {
		return f, false, true
	}
	if i > 0 {
		if f, ok := learned(ws[i-1]); ok {
			return f, true, true
		}
	}
	return 0, false, false
}

// Meters holds the windows of the accounts that have a meter.
type Meters struct {
	metered map[string]bool
	windows map[string][]meterWindow
}

// buildMeters walks the slots, oldest first. A window opens where the meter falls; the slot of the
// fall belongs to the new window, as the meter was at zero when it began.
func buildMeters(slots []*Slot, metered map[string]bool) *Meters {
	ms := &Meters{metered: metered, windows: map[string][]meterWindow{}}
	last := map[string]float64{}
	for _, s := range slots {
		for name, a := range s.Accounts {
			if !metered[name] {
				continue
			}
			ws := ms.windows[name]
			if len(ws) == 0 {
				ws = []meterWindow{{start: s.T}}
			}
			if a.Main != nil {
				if before, ok := last[name]; ok && *a.Main < before-resetFall {
					ws = append(ws, meterWindow{start: s.T, observed: true})
				}
				last[name] = *a.Main
			}
			w := &ws[len(ws)-1]
			for _, t := range a.Models {
				w.usage += basis(t)
			}
			if a.Main != nil {
				w.util = *a.Main
			}
			ms.windows[name] = ws
		}
	}
	return ms
}

// shareOf prices the requests of one slot, by account.
func (ms *Meters) shareOf(at time.Time, accounts map[string]Traffic) (sh Share) {
	if ms == nil {
		return sh
	}
	for name, t := range accounts {
		if !ms.metered[name] {
			sh.Elsewhere += t.Tokens()
			continue
		}
		if basis(t) == 0 {
			continue // a request that cost nothing needs no factor
		}
		ws := ms.windows[name]
		i := len(ws) - 1
		for i >= 0 && ws[i].start.After(at) {
			i--
		}
		if i < 0 {
			sh.Unknown = true
			continue
		}
		if f, _, ok := factor(ws, i); ok {
			sh.Points += f * basis(t)
		} else {
			sh.Unknown = true
		}
	}
	return sh
}

// calibrations is each metered account's current factor.
func (ms *Meters) calibrations() map[string]Calibration {
	out := map[string]Calibration{}
	for name, ws := range ms.windows {
		cur := len(ws) - 1
		f, previous, ok := factor(ws, cur)
		c := Calibration{Known: ok, FromPrevious: previous, Util: ws[cur].util, Length: resetGap(ws)}
		if ok {
			c.PerMillion = f * 1e6
		}
		out[name] = c
	}
	return out
}

// resetGap is the typical time between two resets seen: the median of the gaps between the
// windows whose opening was observed.
func resetGap(ws []meterWindow) time.Duration {
	var gaps []time.Duration
	for i := 1; i < len(ws); i++ {
		if ws[i-1].observed && ws[i].observed {
			gaps = append(gaps, ws[i].start.Sub(ws[i-1].start))
		}
	}
	if len(gaps) == 0 {
		return 0
	}
	slices.Sort(gaps)
	return gaps[len(gaps)/2]
}
