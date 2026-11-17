package history

import (
	"fmt"
	"math"
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
	// Readings correct themselves by a point (the history shows no larger correction); a reset
	// falls to about zero.
	resetFall = 3.0
	// outsideHour is how far from a rise of the meter the account must have served nothing, in the
	// history, for the rise to be use outside the proxy.
	outsideHour = time.Hour
)

// BasisText says what one unit of use is, for the page.
var BasisText = fmt.Sprintf("input + output + cache writes + %g%% of cache reads", CacheReadWeight*100)

// basis is the use a request counts for against the meter.
func basis(t Traffic) float64 {
	return float64(t.InputTokens+t.OutputTokens+t.CacheCreationTokens) + CacheReadWeight*float64(t.CacheReadTokens)
}

// AccountPart is what a set of requests used of one account.
type AccountPart struct {
	Units   float64 // use on the meter's basis (see BasisText)
	Points  float64 // percent of that account's limit window, at the account's own factor
	Unknown bool    // the account has a meter, but some of this use fell in a window with no factor
	Priced  bool    // false: the account has no meter in the history, so there is no percent
}

// Share is what a set of requests used of the metered accounts' limit windows. Points, Unknown and
// Elsewhere count the aggregate accounts alone (the Ultra accounts: one percent of one window);
// ByAccount splits the use of every priced account.
type Share struct {
	Points    float64 // percent of one account's limit window, summed over the aggregate accounts that served it
	Unknown   bool    // some use on an aggregate account fell in a window with no factor, so Points falls short
	Elsewhere int64   // tokens on accounts outside the aggregate: not in Points
	ByAccount map[string]AccountPart
}

// Add adds another share to this one. The map of accounts is rebuilt, never changed in place: a
// Share is copied by value, and a copy must not see a later Add.
func (s *Share) Add(o Share) {
	s.Points += o.Points
	s.Unknown = s.Unknown || o.Unknown
	s.Elsewhere += o.Elsewhere
	if len(o.ByAccount) == 0 {
		return
	}
	merged := make(map[string]AccountPart, len(s.ByAccount)+len(o.ByAccount))
	for name, p := range s.ByAccount {
		merged[name] = p
	}
	for name, p := range o.ByAccount {
		x := merged[name]
		x.Units += p.Units
		x.Points += p.Points
		x.Unknown = x.Unknown || p.Unknown
		x.Priced = x.Priced || p.Priced
		merged[name] = x
	}
	s.ByAccount = merged
}

// AccountUse is what a row used of one account in both windows, for the page: the tokens on the
// meter's basis, and the percent of that account's own limit window (nil: it cannot be priced).
type AccountUse struct {
	Tokens5h int64    `json:"tokens5h"`
	Tokens7d int64    `json:"tokens7d"`
	Share5h  *float64 `json:"share5h"`
	Share7d  *float64 `json:"share7d"`
}

// AccountUses is the figures of a row for each account that served it in either window.
func AccountUses(five, week Share) map[string]AccountUse {
	out := map[string]AccountUse{}
	for name, p := range five.ByAccount {
		u := out[name]
		u.Tokens5h, u.Share5h = int64(math.Round(p.Units)), p.percent()
		out[name] = u
	}
	for name, p := range week.ByAccount {
		u := out[name]
		u.Tokens7d, u.Share7d = int64(math.Round(p.Units)), p.percent()
		out[name] = u
	}
	return out
}

func (p AccountPart) percent() *float64 {
	if !p.Priced || p.Unknown {
		return nil
	}
	v := p.Points
	return &v
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
	Outside      float64       // points of Util that rose with no use of the proxy near them: not in the factor
}

type meterWindow struct {
	start       time.Time
	observed    bool // the meter was seen to fall at its start; the first window of the history began before it
	util, usage float64
	outside     float64 // points of util that rose while the account served nothing through the proxy
}

// factor is the percent per unit of use in window i: its own when it shows enough, else the
// previous window's. What rose outside the proxy is not use the balancer counted, so it is taken
// off the meter first.
func factor(ws []meterWindow, i int) (f float64, previous, ok bool) {
	learned := func(w meterWindow) (float64, bool) {
		if !w.observed || w.util < MinMeterPoints || w.util <= w.outside || w.usage <= 0 {
			return 0, false
		}
		return (w.util - w.outside) / w.usage, true
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

// Meters holds the windows of the accounts that have a meter. aggregate, a subset of metered, are
// the accounts whose use adds up in Share.Points.
type Meters struct {
	metered   map[string]bool
	aggregate map[string]bool
	windows   map[string][]meterWindow
}

// reading is a meter reading, with the window it fell in.
type reading struct {
	at     time.Time
	value  float64
	window int
}

// buildMeters walks the slots, oldest first. A window opens where the meter falls; the slot of the
// fall belongs to the new window, as the meter was at zero when it began. A rise of the meter
// between two readings a slot apart, with no use of the proxy in the hour either side, is use
// outside the proxy: it is counted apart and is not part of the factor.
func buildMeters(slots []*Slot, aggregate, metered map[string]bool) *Meters {
	ms := &Meters{metered: metered, aggregate: aggregate, windows: map[string][]meterWindow{}}
	last := map[string]float64{}
	reads := map[string][]reading{}
	served := map[string][]time.Time{} // when each account served anything, oldest first
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
			var used float64
			for _, t := range a.Models {
				used += basis(t)
			}
			w.usage += used
			if used > 0 {
				served[name] = append(served[name], s.T)
			}
			if a.Main != nil {
				w.util = *a.Main
				reads[name] = append(reads[name], reading{s.T, *a.Main, len(ws) - 1})
			}
			ms.windows[name] = ws
		}
	}
	for name, rs := range reads {
		ws := ms.windows[name]
		for k := 1; k < len(rs); k++ {
			prev, cur := rs[k-1], rs[k]
			rise := cur.value - prev.value
			if rise <= 0 || cur.window != prev.window || cur.at.Sub(prev.at) != SlotLength {
				continue // not a rise, a reset, or readings with a gap between them
			}
			// an hour either side, from the earlier reading to the later one
			i, _ := slices.BinarySearchFunc(served[name], prev.at.Add(-outsideHour), func(t, at time.Time) int { return t.Compare(at) })
			if i < len(served[name]) && !served[name][i].After(cur.at.Add(outsideHour)) {
				continue // the proxy served something near it: the meter lags by a slot or two
			}
			ws[cur.window].outside += rise
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
		part := AccountPart{Units: basis(t), Priced: ms.metered[name]}
		if part.Priced && part.Units > 0 { // a request that cost nothing needs no factor
			part.Points, part.Unknown = ms.price(name, at, part.Units)
		}
		if sh.ByAccount == nil {
			sh.ByAccount = map[string]AccountPart{}
		}
		sh.ByAccount[name] = part
		if !ms.aggregate[name] {
			sh.Elsewhere += t.Tokens()
			continue
		}
		sh.Points += part.Points
		sh.Unknown = sh.Unknown || part.Unknown
	}
	return sh
}

// price is units of use on one account at a time, in percent of the account's limit window at the
// factor of the window the time falls in; unknown when that window has no factor.
func (ms *Meters) price(name string, at time.Time, units float64) (points float64, unknown bool) {
	ws := ms.windows[name]
	i := len(ws) - 1
	for i >= 0 && ws[i].start.After(at) {
		i--
	}
	if i < 0 {
		return 0, true
	}
	f, _, ok := factor(ws, i)
	return f * units, !ok
}

// calibrations is each metered account's current factor.
func (ms *Meters) calibrations() map[string]Calibration {
	out := map[string]Calibration{}
	for name, ws := range ms.windows {
		cur := len(ws) - 1
		f, previous, ok := factor(ws, cur)
		c := Calibration{Known: ok, FromPrevious: previous, Util: ws[cur].util, Outside: ws[cur].outside, Length: resetGap(ws)}
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
