package outlook

import (
	"fmt"
	"math"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// maxEvents bounds one run of the sequence; a run that needs more stops with an error.
var maxEvents = 100000

// never is a hold no reset ends.
var never = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// sequence moves the weekly use through the accounts the way the balancer does, from where it is
// now: at every moment the account its policy picks takes the work, the week at the pace in its
// own percent and its 5-hour window at that window's pace, until it reaches a cap in force; the
// policy then picks the next. Reserve and 5-hour steps change caps when they end, a weekly reset
// refills its account, a 5-hour window closes at its reset and starts again with the account's
// next use, a CPA cooldown ends at the reset of the window that caused it, and with every account
// at its weekly cap the policy releases the reserves up to the final cap. The room is out when
// the policy has no account left for the week; with only 5-hour windows full, work waits.
type sequence struct {
	cfg    config.Config
	now    time.Time
	active string    // the balancer's pick now
	since  time.Time // when it switched to it (zero: unknown, its dwell counts as passed)
	turns  []turn
}

// turn is one account as the sequence moves it.
type turn struct {
	account   config.Account // zero when not configured: the policy never picks it
	key, name string
	weight    float64 // its weekly limit in Pro plans
	util      float64
	resets    time.Time // zero: no weekly reading
	fiveHour  bool      // the plan has a 5-hour limit
	session   float64   // the 5-hour window's use
	closes    time.Time // the 5-hour window's reset (zero: closed, it starts with the next use)
	pace      float64   // the 5-hour window's percent a second while used (0: it never fills)
	off       time.Time // CPA holds the credential back until then (zero: it does not)
}

// sequenceOf reads where the balancer is from the status: each account's windows with its weight
// and 5-hour pace (by key, percent a second), and the account in use since its last switch.
func sequenceOf(v engine.View, cfg config.Config, weights []float64, paces map[string]float64, now time.Time) sequence {
	configured := map[string]config.Account{}
	for _, a := range cfg.Accounts {
		configured[a.AuthFile] = a
	}
	s := sequence{cfg: cfg, now: now, active: v.Decision.Main.Account}
	for _, c := range v.Changes {
		if c.Bucket == quota.Main && c.To == s.active && s.active != "" && c.At.After(s.since) {
			s.since = c.At
		}
	}
	for i, a := range v.Accounts {
		t := turn{account: configured[a.AuthFile], key: a.AuthFile, name: a.Name, weight: weights[i],
			fiveHour: a.FiveHour, pace: paces[a.AuthFile]}
		if r, ok := a.Readings[quota.Main]; ok && r.ResetsAt != nil {
			t.util, t.resets = r.Util, *r.ResetsAt
			// a reset already past (an old reading) started the next window
			for !t.resets.After(now) {
				t.util, t.resets = 0, t.resets.Add(Week)
			}
		}
		r, running := a.Readings[quota.Session]
		running = running && t.fiveHour && r.Running(now)
		if running {
			t.session, t.closes = r.Util, *r.ResetsAt
		}
		switch {
		case a.Disabled:
			t.off = never
		case !a.Unavailable:
		case running && r.Util >= 100:
			t.off = t.closes // a 5-hour 429 cools the credential until that window resets
		case !t.resets.IsZero():
			t.off = t.resets // a weekly 429 cools it until the week resets
		default:
			t.off = never
		}
		s.turns = append(s.turns, t)
	}
	return s
}

// drain runs the sequence at `rate` Pro-plan points a second for `end` seconds: the dry spells, when
// the policy leaves each account at its weekly cap within its current window, when each reaches
// its cap in any window of the run, and the account the room runs out on.
func (s sequence) drain(rate, end float64) (run, error) {
	th := s.cfg.Thresholds
	turns := append([]turn(nil), s.turns...)
	index := map[string]int{}
	for i, x := range turns {
		index[x.key] = i
	}
	stop := s.now.Add(secs(end))
	// a dry spell open at the end is followed one more window, to the reset that ends it
	last := stop.Add(Week)
	handoff := map[string]time.Time{}
	capped := map[string]time.Time{} // the first moment of the run each account reaches its cap, in any window
	exhausts := ""
	var out run
	t, active, since := s.now, s.active, s.since
	dry, atLimit := false, false
	for step := 0; ; step++ {
		if step >= maxEvents {
			return run{}, fmt.Errorf("outlook: the week's simulation passed %d steps", maxEvents)
		}
		for i := range turns {
			x := &turns[i]
			for !x.resets.IsZero() && !x.resets.After(t) {
				x.util, x.resets = 0, x.resets.Add(Week)
			}
			if !x.closes.IsZero() && !x.closes.After(t) {
				x.session, x.closes = 0, time.Time{}
			}
		}
		c := policy.Choose(quota.Main, s.cfg, snapshots(turns, t, true), t, policy.Active{Account: active, Since: since})
		pick := picked(c)
		// with only 5-hour windows full the work waits; the week is out when no account has room in it
		weekOut := pick == "" && picked(policy.Choose(quota.Main, s.cfg, snapshots(turns, t, false), t, policy.Active{Account: active, Since: since})) == ""
		inside := t.Before(stop)
		// a row shows the account's next event: a run-out marks it only before any handoff
		_, handedOff := handoff[active]
		if pick != active {
			i, ok := index[active]
			// the balancer moves on from an account at its weekly cap: that is its capped, in
			// whichever window, and its handoff within its current one
			if ok && atLimit && inside {
				if _, seen := capped[active]; !seen {
					capped[active] = t
				}
				if _, seen := handoff[active]; !seen && t.Before(s.turns[i].resets) {
					handoff[active] = t
				}
			}
			since = t
		}
		switch {
		case weekOut && !dry && inside:
			// the room runs out on the account in use, in whichever of its windows
			if _, ok := index[active]; ok && len(out.spells) == 0 && !handedOff {
				handoff[active], exhausts = t, active
				if _, seen := capped[active]; !seen {
					capped[active] = t // no room is left on it
				}
			}
			out.spells = append(out.spells, spell{from: t.Sub(s.now).Seconds(), to: math.Inf(1)})
			dry = true
		case pick != "" && dry:
			out.spells[len(out.spells)-1].to = t.Sub(s.now).Seconds()
			out.spells[len(out.spells)-1].refill = turns[index[pick]].name
			dry = false
		}
		if (!dry && !inside) || !t.Before(last) {
			break
		}
		active = pick
		// the next moment anything can change: a reset or a cap step of any window, the end of a
		// cooldown, a cap of the account in use, the end of its dwell, or the end of the run
		next := last
		for _, x := range turns {
			if !x.resets.IsZero() {
				next = earliest(next, x.resets)
				if _, at, ok := policy.NextCap(x.account, false, x.resets, t, th); ok {
					next = earliest(next, at.Add(time.Nanosecond)) // the step's cap is in force after it ends
				}
			}
			if !x.closes.IsZero() {
				next = earliest(next, x.closes)
				if _, at, ok := policy.NextCap(x.account, true, x.closes, t, th); ok {
					next = earliest(next, at.Add(time.Nanosecond))
				}
			}
			if x.off.After(t) && x.off.Before(never) {
				next = earliest(next, x.off)
			}
		}
		if !dry {
			next = earliest(next, stop)
		}
		atLimit = false
		i, ok := index[active]
		if !ok {
			t = next
			continue
		}
		x := &turns[i]
		if x.fiveHour && x.closes.IsZero() {
			x.closes = t.Add(Five) // its use starts a 5-hour window
			next = earliest(next, x.closes)
		}
		if dwell := since.Add(th.MinDwell()); dwell.After(t) {
			next = earliest(next, dwell)
		}
		weekCap := th.FinalCap
		if !c.Relaxed {
			weekCap = policy.Cap(x.account, x.resets, t, th)
		}
		weekAt := reach(t, last, (weekCap-x.util)*x.weight/rate)
		next = earliest(next, weekAt)
		var sessionCap float64
		sessionAt := time.Time{}
		if x.fiveHour && x.pace > 0 {
			closes := x.closes
			sessionCap = policy.SessionCap(x.account, &closes, t, th)
			sessionAt = reach(t, last, (sessionCap-x.session)/x.pace)
			next = earliest(next, sessionAt)
		}
		dt := next.Sub(t).Seconds()
		x.util += rate * dt / x.weight
		x.session += x.pace * dt
		// a cap reached at the same moment as another event still counts as reached
		if reached(weekAt, next) {
			x.util, atLimit = weekCap, true
		}
		if reached(sessionAt, next) {
			x.session = sessionCap
		}
		t = next
	}
	for _, x := range s.turns {
		h := Handoff{Key: x.key, Exhausts: x.key == exhausts}
		if at, ok := handoff[x.key]; ok {
			h.At = &at
		}
		if at, ok := capped[x.key]; ok {
			h.Capped = &at
		}
		out.accounts = append(out.accounts, h)
	}
	return out, nil
}

// picked is the account a choice gives the work to; extra-usage credits are not plan quota.
func picked(c policy.Choice) string {
	if c.Credits {
		return ""
	}
	return c.Account
}

// reach is when a cap `seconds` away is reached, zero when not before `last`.
func reach(t, last time.Time, seconds float64) time.Time {
	if seconds >= last.Sub(t).Seconds() {
		return time.Time{}
	}
	return t.Add(secs(math.Max(0, seconds)))
}

// reached: a cap due at `at` is reached at `next`, give or take a microsecond of rounding.
func reached(at, next time.Time) bool {
	return !at.IsZero() && at.Sub(next) <= time.Microsecond
}

// snapshots is what the policy reads at `t`: every account's windows, just observed; the 5-hour
// ones only with `session`. Like the engine, it gives the policy every account whether or not
// CPA's credential list names it (the status's `listed`); CPA's holds come in through `off`.
func snapshots(turns []turn, t time.Time, session bool) map[string]policy.Snapshot {
	out := map[string]policy.Snapshot{}
	for _, x := range turns {
		s := policy.Snapshot{Readings: quota.Readings{}, Unavailable: t.Before(x.off), Weight: x.weight, NoFiveHour: !x.fiveHour}
		if !x.resets.IsZero() {
			r := x.resets
			s.Readings[quota.Main] = quota.Reading{Util: x.util, ResetsAt: &r, ObservedAt: t}
		}
		if session && x.fiveHour {
			r := quota.Reading{Util: x.session, ObservedAt: t}
			if !x.closes.IsZero() {
				c := x.closes
				r.ResetsAt = &c
			}
			s.Readings[quota.Session] = r
		}
		out[x.key] = s
	}
	return out
}

func earliest(a, b time.Time) time.Time {
	if !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}
