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
	// Each account's own percent an hour over the calendar, by key (measured only): an account
	// with no use of its own in the history is paced by the work, in its own percent.
	Accounts map[string]float64 `json:"accounts,omitempty"`
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
}

type pool struct {
	key             string
	name            string
	room, cap, over float64 // over: resets at, seconds from now (+Inf: closed)
}

// Both is the outlook of each window kind; one with fewer than two accounts is left out. Pace is
// the weekly work pace measured from the history, with each account's own: it paces each
// account's flag too, so it stands apart from the combined outlook (nil: too little work).
type Both struct {
	Week     *Outlook `json:"week,omitempty"`
	FiveHour *Outlook `json:"five_hour,omitempty"`
	Pace     *Pace    `json:"pace,omitempty"`
}

// Compute combines the accounts over a window of length `window`, each against its own cap.
// `measured` is the work pace from the history (nil: each open window's own average). False with
// fewer than two accounts: there is nothing to combine.
func Compute(accounts []Account, window time.Duration, measured *Pace, now time.Time) (Outlook, bool) {
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
	if long := runOut(pools, speed, 0, 2*span, span); long != nil {
		at := now.Add(secs(long.seconds))
		o.RunsOut = &at
	}
	stop := runOut(pools, speed, 0, span, span)
	switch {
	case stop != nil:
		o.Verdict = "out"
		if !math.IsInf(stop.at, 1) {
			d := &Dry{Seconds: math.Max(0, stop.at-stop.seconds), Refill: stop.refill, At: now.Add(secs(stop.at))}
			d.Next = afterRefill(stop, speed, span)
			o.Dry = d
		}
	case runOut(pools, speed*1.2, 0, span, span) != nil:
		o.Verdict = "tight"
	default:
		o.Verdict = "lasts"
	}
	// the largest multiple of the pace that lasts the window (bisection)
	lo, hi := 0.0, maxMultiple
	if runOut(pools, speed*hi, 0, span, span) == nil {
		lo = hi
	}
	for i := 0; i < 30 && lo != hi && hi-lo > 1e-4; i++ {
		mid := (lo + hi) / 2
		if runOut(pools, speed*mid, 0, span, span) == nil {
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

type stop struct {
	seconds, at float64 // seconds from now it runs out; seconds from now of the refill (+Inf: none)
	refill      string
	pools       []pool
}

// runOut: when every account is out of room at `rate` points a second, from `from` to `end`
// (seconds from now), and the reset that brings room back; nil when the room lasts. Use drains the
// account that resets first (the balancer's order); a reset refills that account to its cap and
// starts its next window, `span` seconds long.
func runOut(start []pool, rate, from, end, span float64) *stop {
	pools := append([]pool(nil), start...)
	t := from
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
			empty := append([]pool(nil), pools...)
			for i := range empty {
				empty[i].room = 0
			}
			return &stop{seconds: t + room/rate, at: back.over, refill: back.name, pools: empty}
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
			return nil
		}
		t = next
		for i := range pools {
			if pools[i].over == next {
				pools[i].room = pools[i].cap
				pools[i].over += span
			}
		}
	}
}

// afterRefill: the stretch after the refill at the same pace, up to two windows from now.
func afterRefill(s *stop, rate, span float64) *Next {
	pools := append([]pool(nil), s.pools...)
	for i := range pools {
		if pools[i].over == s.at {
			pools[i].room = pools[i].cap
			pools[i].over += span
		}
	}
	again := runOut(pools, rate, s.at, 2*span, span)
	switch {
	case again == nil:
		return &Next{Lasts: true}
	case math.IsInf(again.at, 1):
		return nil
	}
	return &Next{Seconds: math.Max(0, again.at-again.seconds)}
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
	risen := map[string]float64{}
	worked := map[string]int{}
	for i := 1; i < len(h.T); i++ {
		var rise float64
		for name, series := range h.Main {
			if i >= len(series) || series[i] == nil || series[i-1] == nil || *series[i] <= *series[i-1] {
				continue
			}
			risen[name] += *series[i] - *series[i-1]
			worked[name]++
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
	accounts := map[string]float64{}
	for name := range h.Main {
		own := activePerSecond / weightOf(weights, name)
		if worked[name] > 0 {
			own = risen[name] / (float64(worked[name]) * step)
		}
		accounts[name] = own * 3600 * share
	}

	return &Pace{PerHour: activePerSecond * 3600 * share, ActivePerHour: activePerSecond * 3600, HoursPerDay: hoursPerDay, Accounts: accounts}
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
