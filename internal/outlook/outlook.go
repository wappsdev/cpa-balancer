// Package outlook looks ahead at the quota of all accounts together, the weekly window and the
// 5-hour one: at the pace the work has gone, does the room last until the resets bring more? Pure:
// it takes readings and history and answers numbers; the panel and the HUD draw them.
package outlook

import (
	"math"
	"sort"
	"time"
)

const (
	Week = 7 * 24 * time.Hour
	Five = 5 * time.Hour
	// the work pace needs this much worked time in the history, else each window's own average is used
	MinWork = 2 * time.Hour
	// the largest multiple of the pace the headroom search tries
	maxMultiple = 10.0
)

// Account is one account's reading in one window kind. Weight is its weekly limit in Pro plans;
// Cap is the use it can reach in that window.
type Account struct {
	Key      string
	Name     string
	Weight   float64
	Cap      float64
	Util     float64
	ResetsAt time.Time // zero: no reading, the window counts as closed (full room)
}

// Series is the history's weekly utilization per account and step.
type Series struct {
	Step int64
	T    []int64
	Main map[string][]*float64
}

// Pace is how fast quota goes, in Pro-plan points an hour.
type Pace struct {
	PerHour       float64 `json:"per_hour"`        // over the calendar, idle time included
	ActivePerHour float64 `json:"active_per_hour"` // while working
	HoursPerDay   float64 `json:"hours_per_day"`   // worked on a day with any work
	Source        string  `json:"source"`          // measured (history) or average (each window's own)
}

// Next is the stretch after the refill at the same pace.
type Next struct {
	Lasts   bool    `json:"lasts"`
	Seconds float64 `json:"seconds,omitempty"` // the room is out again for this long
}

// Dry is the time with no room: from the stop to the refill.
type Dry struct {
	Seconds float64   `json:"seconds"`
	Refill  string    `json:"refill"` // the account whose reset brings room back
	At      time.Time `json:"at"`
	Next    *Next     `json:"next,omitempty"`
}

// Outlook answers whether the room lasts until the resets bring more.
type Outlook struct {
	Percent float64 `json:"percent"` // use of all accounts against all caps
	Pace    *Pace   `json:"pace,omitempty"`
	// lasts, tight (lasts, but not at 1.2 times the pace), out (runs out before a reset) or unknown
	Verdict string `json:"verdict"`
	// The largest multiple of the pace the room still lasts (1.14: 14% faster is the limit; 0.8:
	// 20% slower is needed).
	Lasts float64 `json:"lasts,omitempty"`
	// When the room runs out at the pace, counting every reset before it: each refills its account
	// to the cap and starts its next window. Absent: the room lasts to Horizon.
	RunsOut *time.Time `json:"runs_out,omitempty"`
	// The end of the stretch RunsOut looks at: two windows from now.
	Horizon *time.Time `json:"horizon,omitempty"`
	Dry     *Dry       `json:"dry,omitempty"`
	// Each account's turn in the balancer's order at the pace (the weekly window only), from the
	// same run as RunsOut.
	Accounts []Handoff `json:"accounts,omitempty"`
}

// Handoff is when the balancer leaves an account at its cap, counted from now through the turns
// of the accounts before it: the first time in its current window, or the moment the room runs
// out when that is on this account (Exhausts). At is nil when the account resets first or the
// two windows end.
type Handoff struct {
	Key      string     `json:"key"`
	At       *time.Time `json:"handoff,omitempty"`
	Exhausts bool       `json:"exhausts,omitempty"`
}

type pool struct {
	key             string
	name            string
	room, cap, over float64 // over: resets at, seconds from now (+Inf: closed)
}

// Both is the outlook of each window kind; one with fewer than two accounts is left out. Pace is
// the weekly work pace measured from the history; it stands apart from the combined outlook, so
// one account still has it (nil: too little work).
type Both struct {
	Week     *Outlook `json:"week,omitempty"`
	FiveHour *Outlook `json:"five_hour,omitempty"`
	Pace     *Pace    `json:"pace,omitempty"`
}

// Compute combines the accounts over a window of length `window`, each against its own cap; use
// drains the account that resets first. `measured` is the work pace from the history (nil: each
// open window's own average). False with fewer than two accounts: there is nothing to combine.
func Compute(accounts []Account, window time.Duration, measured *Pace, now time.Time) (Outlook, bool) {
	return look(accounts, window, measured, now, nil)
}

// spell is a stretch with no room, in seconds from now: from the stop to the refill (+Inf: none
// comes), and the account whose reset brings room back.
type spell struct {
	from, to float64
	refill   string
}

// run is how the room goes at one pace: its dry spells, and each account's handoff when the run
// follows the balancer's order.
type run struct {
	spells   []spell
	accounts []Handoff
}

// drain runs the use at `rate` points a second through the accounts, up to `end` seconds from
// now: every dry spell that starts before it.
type drain func(rate, end float64) run

// look is Compute with the use moved by `d` (nil: the account that resets first takes it).
func look(accounts []Account, window time.Duration, measured *Pace, now time.Time, d drain) (Outlook, bool) {
	w := weightsOf(accounts)
	var used, total, average float64
	soonest := math.Inf(1)
	var pools []pool
	for i, a := range accounts {
		open := !a.ResetsAt.IsZero() && a.ResetsAt.After(now)
		util := 0.0
		if open {
			util = a.Util
		}
		used += math.Min(util, a.Cap) * w[i]
		total += a.Cap * w[i]
		p := pool{key: a.Key, name: a.Name, room: math.Max(0, a.Cap-util) * w[i], cap: a.Cap * w[i], over: math.Inf(1)}
		if open {
			p.over = a.ResetsAt.Sub(now).Seconds()
			elapsed := window.Seconds() - p.over
			soonest = math.Min(soonest, p.over)
			if util > 0 && elapsed >= window.Seconds()/20 {
				average += util * w[i] / elapsed
			}
		}
		pools = append(pools, p)
	}
	if len(pools) < 2 || total == 0 {
		return Outlook{}, false
	}
	span := window.Seconds()
	o := Outlook{Percent: used / total * 100, Verdict: "unknown"}
	var speed float64 // points a second
	switch {
	case measured != nil:
		speed = measured.PerHour / 3600
		pc := *measured
		pc.Source = "measured"
		o.Pace = &pc
	case average > 0:
		speed = average
		o.Pace = &Pace{PerHour: average * 3600, Source: "average"}
	}
	var room float64
	for _, p := range pools {
		room += p.room
	}
	if speed <= 0 || room <= 0 || math.IsInf(soonest, 1) {
		return o, true
	}
	// the verdict looks one window ahead; the moment the room runs out, two, so the stretch after
	// the last reset of the first window counts
	horizon := now.Add(secs(2 * span))
	o.Horizon = &horizon
	if d == nil {
		d = poolsDrain(pools, span)
	}
	ahead := d(speed, 2*span)
	o.Accounts = ahead.accounts
	if len(ahead.spells) > 0 {
		at := now.Add(secs(ahead.spells[0].from))
		o.RunsOut = &at
	}
	// out within the window looked at for the verdict
	out := func(r run) bool { return len(r.spells) > 0 && r.spells[0].from < span }
	switch {
	case out(ahead):
		o.Verdict = "out"
		if stop := ahead.spells[0]; !math.IsInf(stop.to, 1) {
			o.Dry = &Dry{Seconds: math.Max(0, stop.to-stop.from), Refill: stop.refill, At: now.Add(secs(stop.to))}
			// the stretch after that refill at the same pace, up to two windows from now
			switch {
			case len(ahead.spells) == 1:
				o.Dry.Next = &Next{Lasts: true}
			case !math.IsInf(ahead.spells[1].to, 1):
				o.Dry.Next = &Next{Seconds: math.Max(0, ahead.spells[1].to-ahead.spells[1].from)}
			}
		}
	case out(d(speed*1.2, span)):
		o.Verdict = "tight"
	default:
		o.Verdict = "lasts"
	}
	// the largest multiple of the pace that lasts the window (bisection)
	lo, hi := 0.0, maxMultiple
	if !out(d(speed*hi, span)) {
		lo = hi
	}
	for i := 0; i < 30 && lo != hi && hi-lo > 1e-4; i++ {
		mid := (lo + hi) / 2
		if !out(d(speed*mid, span)) {
			lo = mid
		} else {
			hi = mid
		}
	}
	o.Lasts = lo

	return o, true
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// weightsOf: each account's weekly limit in Pro plans; with any unknown every account counts alike.
func weightsOf(accounts []Account) []float64 {
	out := make([]float64, len(accounts))
	for i, a := range accounts {
		if a.Weight <= 0 {
			for j := range out {
				out[j] = 1
			}
			return out
		}
		out[i] = a.Weight
	}
	return out
}

// poolsDrain: use drains the account that resets first; a reset refills that account to its cap
// and starts its next window, `span` seconds long. With every account out of room, the next reset
// brings room back.
func poolsDrain(start []pool, span float64) drain {
	return func(rate, end float64) run {
		pools := append([]pool(nil), start...)
		refill := func(at float64) {
			for i := range pools {
				if pools[i].over == at {
					pools[i].room = pools[i].cap
					pools[i].over += span
				}
			}
		}
		var out run
		t := 0.0
		for {
			next := end
			for _, p := range pools {
				next = math.Min(next, p.over)
			}
			need := rate * (next - t)
			var room float64
			for _, p := range pools {
				room += p.room
			}
			if room < need {
				back := pools[0]
				for _, p := range pools {
					if p.over < back.over {
						back = p
					}
				}
				out.spells = append(out.spells, spell{from: t + room/rate, to: back.over, refill: back.name})
				if back.over >= end {
					return out
				}
				for i := range pools {
					pools[i].room = 0
				}
				t = back.over
				refill(t)
				continue
			}
			order := append([]pool(nil), pools...)
			sort.SliceStable(order, func(i, j int) bool { return order[i].over < order[j].over })
			left := need
			for _, o := range order {
				take := math.Min(o.room, left)
				for i := range pools {
					if pools[i].key == o.key {
						pools[i].room -= take
					}
				}
				left -= take
			}
			if next >= end {
				return out
			}
			t = next
			refill(next)
		}
	}
}

// workPace reads the pace from the history: each rise of an account's weekly utilization is use (a
// drop is a reset); a step with any rise is worked time. Nil with less than `minActive` seconds of work.
func workPace(h Series, weights map[string]float64, minActive float64) *Pace {
	step := float64(h.Step)
	if len(h.T) == 0 {
		return nil
	}
	last := h.T[len(h.T)-1]
	var total float64
	active := 0
	days := map[int64]bool{}
	for i := 1; i < len(h.T); i++ {
		var rise float64
		for name, series := range h.Main {
			if i >= len(series) || series[i] == nil || series[i-1] == nil || *series[i] <= *series[i-1] {
				continue
			}
			rise += (*series[i] - *series[i-1]) * weightOf(weights, name)
		}
		if rise <= 0 {
			continue
		}
		total += rise
		active++
		days[(last-h.T[i])/86400] = true
	}
	if float64(active)*step < minActive || len(days) == 0 {
		return nil
	}
	activePerSecond := total / (float64(active) * step)
	hoursPerDay := float64(active) * step / 3600 / float64(len(days))
	share := hoursPerDay / 24

	return &Pace{PerHour: activePerSecond * 3600 * share, ActivePerHour: activePerSecond * 3600, HoursPerDay: hoursPerDay}
}

func weightOf(weights map[string]float64, name string) float64 {
	if wt, ok := weights[name]; ok {
		return wt
	}
	return 1
}

// MeasurePace reads the work pace from a history with the accounts' weights.
func MeasurePace(h Series, accounts []Account) *Pace {
	w := weightsOf(accounts)
	byKey := map[string]float64{}
	for i, a := range accounts {
		byKey[a.Key] = w[i]
	}
	return workPace(h, byKey, MinWork.Seconds())
}
