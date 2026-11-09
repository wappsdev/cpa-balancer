package outlook

import (
	"math"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// sequence moves the weekly use through the accounts the way the balancer does: at every moment
// the account its policy picks takes the work, at the pace in its own percent, until it reaches
// the cap in force; the policy then picks the next. Reserve steps raise a cap when they end, a
// reset refills its account, and with every account at its cap the policy releases the reserves
// up to the final cap. The room is out when the policy has no account left.
type sequence struct {
	cfg    config.Config
	now    time.Time
	active string // the balancer's pick now
	turns  []turn
}

// turn is one account as the sequence moves it.
type turn struct {
	account             config.Account // zero when not configured: the policy never picks it
	key, name           string
	weight              float64 // its weekly limit in Pro plans
	util                float64
	resets              time.Time // zero: no weekly reading
	listed, unavailable bool
}

// sequenceOf reads the accounts' weekly windows from the status, each with its weight.
func sequenceOf(v engine.View, cfg config.Config, weights []float64, now time.Time) sequence {
	configured := map[string]config.Account{}
	for _, a := range cfg.Accounts {
		configured[a.AuthFile] = a
	}
	s := sequence{cfg: cfg, now: now, active: v.Decision.Main.Account}
	for i, a := range v.Accounts {
		t := turn{account: configured[a.AuthFile], key: a.AuthFile, name: a.Name, weight: weights[i], listed: a.Listed, unavailable: a.Unavailable}
		if r, ok := a.Readings[quota.Main]; ok && r.ResetsAt != nil {
			t.util, t.resets = r.Util, *r.ResetsAt
		}
		s.turns = append(s.turns, t)
	}
	return s
}

// drain runs the sequence at `rate` Pro-plan points a second for `end` seconds: the dry spells,
// and when the policy leaves each account at its cap within its current window.
func (s sequence) drain(rate, end float64) run {
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
	exhausts := ""
	var out run
	t, active, since := s.now, s.active, time.Time{}
	dry, atLimit := false, false
	for {
		for i := range turns {
			for x := &turns[i]; !x.resets.IsZero() && !x.resets.After(t); {
				x.util, x.resets = 0, x.resets.Add(Week)
			}
		}
		c := policy.Choose(quota.Main, s.cfg, snapshots(turns, t), t, policy.Active{Account: active, Since: since})
		pick := c.Account
		if c.Credits {
			pick = "" // extra-usage credits are not plan quota: the room is out
		}
		inside := t.Before(stop)
		if pick != active {
			// the balancer moves on from an account at its cap, within its current window
			if i, ok := index[active]; ok && atLimit && inside && t.Before(s.turns[i].resets) {
				if _, seen := handoff[active]; !seen {
					handoff[active] = t
				}
				if pick == "" && len(out.spells) == 0 {
					handoff[active], exhausts = t, active
				}
			}
			since = t
		}
		switch {
		case pick == "" && !dry && inside:
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
		// the next moment anything can change: a reset, a cap step, the active account's cap,
		// the end of its dwell, or the end of the run
		next := last
		for _, x := range turns {
			if x.resets.IsZero() {
				continue
			}
			next = earliest(next, x.resets)
			if _, at, ok := policy.NextCap(x.account, false, x.resets, t, th); ok {
				next = earliest(next, at.Add(time.Nanosecond)) // the step's cap is in force after it ends
			}
		}
		if !dry {
			next = earliest(next, stop)
		}
		var limit time.Time
		var cap float64
		if i, ok := index[active]; ok {
			x := &turns[i]
			cap = th.FinalCap
			if !c.Relaxed {
				cap = policy.Cap(x.account, x.resets, t, th)
			}
			if left := (cap - x.util) * x.weight / rate; left < next.Sub(t).Seconds() {
				limit = t.Add(secs(left))
				next = limit
			}
			if dwell := since.Add(th.MinDwell()); !since.IsZero() && dwell.After(t) {
				next = earliest(next, dwell)
			}
			x.util += rate * next.Sub(t).Seconds() / x.weight
			if next.Equal(limit) {
				x.util = cap
			}
		}
		atLimit = !limit.IsZero() && next.Equal(limit)
		t = next
	}
	for _, x := range s.turns {
		h := Handoff{Key: x.key, Exhausts: x.key == exhausts}
		if at, ok := handoff[x.key]; ok {
			h.At = &at
		}
		out.accounts = append(out.accounts, h)
	}
	return out
}

// snapshots is what the policy reads at `t`: each listed account's weekly window, just observed.
func snapshots(turns []turn, t time.Time) map[string]policy.Snapshot {
	out := map[string]policy.Snapshot{}
	for _, x := range turns {
		if !x.listed {
			continue
		}
		s := policy.Snapshot{Readings: quota.Readings{}, Unavailable: x.unavailable, Weight: x.weight}
		if !x.resets.IsZero() {
			r := x.resets
			s.Readings[quota.Main] = quota.Reading{Util: x.util, ResetsAt: &r, ObservedAt: t}
		}
		out[x.key] = s
	}
	return out
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
