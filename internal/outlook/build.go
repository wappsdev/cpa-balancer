package outlook

import (
	"time"

	"github.com/adnankurt/cpa-balancer/internal/engine"
	"github.com/adnankurt/cpa-balancer/internal/history"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

// sessionCap is the 5-hour cap of an account the balancer reports none for.
const sessionCap = 98

// Build looks ahead from the status and a week of history. The weekly window of every account
// counts against the final cap (reserves are released before work stops), at the pace the history
// shows. The 5-hour window counts the accounts that have one, each against its own cap, at each
// window's own average.
func Build(v engine.View, h history.Series, now time.Time) Both {
	var week, five []Account
	for _, a := range v.Accounts {
		weekly := Account{Key: a.AuthFile, Name: a.Name, Weight: a.Weight, Cap: v.FinalCap}
		if weekly.Cap == 0 {
			weekly.Cap = a.Caps[quota.Main]
		}
		if r, ok := a.Readings[quota.Main]; ok && r.ResetsAt != nil {
			weekly.Util, weekly.ResetsAt = r.Util, *r.ResetsAt
		}
		week = append(week, weekly)
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
	out := Both{Pace: MeasurePace(series, week)}
	if out.Pace != nil {
		out.Pace.Source = "measured"
	}
	if o, ok := Compute(week, Week, out.Pace, now); ok {
		out.Week = &o
	}
	if o, ok := Compute(five, Five, nil, now); ok {
		out.FiveHour = &o
	}

	return out
}
