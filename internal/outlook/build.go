package outlook

import (
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// sessionCap is the 5-hour cap of an account the balancer reports none for.
const sessionCap = 98

// Build looks ahead from the status, the configuration and a week of history. The weekly window
// of every account counts against the final cap (reserves are released before work stops), at
// the pace the history shows, and the use moves through the accounts as the balancer's policy
// moves it, which also gives each account's handoff. The 5-hour window counts the accounts that
// have one, each against its own cap, at each window's own average.
func Build(v engine.View, cfg config.Config, h history.Series, now time.Time) Both {
	var five []Account
	for _, a := range v.Accounts {
		if !a.FiveHour {
			continue
		}
		session := Account{Key: a.AuthFile, Name: a.Name, Weight: a.Weight, Cap: a.Caps[quota.Session]}
		if session.Cap == 0 {
			session.Cap = sessionCap
		}
		if r, ok := a.Readings[quota.Session]; ok && r.ResetsAt != nil {
			session.Util, session.ResetsAt = r.Util, *r.ResetsAt
		}
		five = append(five, session)
	}
	series := Series{Step: h.Step, T: h.T, Main: map[string][]*float64{}}
	for name, s := range h.Accounts {
		series.Main[name] = s.Main
	}
	out := Both{Pace: MeasurePace(series, weekAccounts(v))}
	if out.Pace != nil {
		out.Pace.Source = "measured"
	}
	if o, ok := weekly(v, cfg, out.Pace, now); ok {
		out.Week = &o
	}
	if o, ok := Compute(five, Five, nil, now); ok {
		out.FiveHour = &o
	}

	return out
}

// weekAccounts is every account's weekly window against the final cap.
func weekAccounts(v engine.View) []Account {
	var week []Account
	for _, a := range v.Accounts {
		w := Account{Key: a.AuthFile, Name: a.Name, Weight: a.Weight, Cap: v.FinalCap}
		if w.Cap == 0 {
			w.Cap = a.Caps[quota.Main]
		}
		if r, ok := a.Readings[quota.Main]; ok && r.ResetsAt != nil {
			w.Util, w.ResetsAt = r.Util, *r.ResetsAt
		}
		week = append(week, w)
	}
	return week
}

// weekly is the weekly outlook, with the use moved through the accounts as the balancer moves it.
func weekly(v engine.View, cfg config.Config, measured *Pace, now time.Time) (Outlook, bool) {
	accounts := weekAccounts(v)
	return look(accounts, Week, measured, now, sequenceOf(v, cfg, weightsOf(accounts), now).drain)
}
