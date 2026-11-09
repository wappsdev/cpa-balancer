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
// moves it, 5-hour windows included, which also gives each account's handoff. The 5-hour window
// counts the accounts that have one, each against its own cap, at each window's own average.
func Build(v engine.View, cfg config.Config, h history.Series, now time.Time) (Both, error) {
	series := Series{Step: h.Step, T: h.T, Main: map[string][]*float64{}, Session: map[string][]*float64{}}
	for name, s := range h.Accounts {
		series.Main[name], series.Session[name] = s.Main, s.Session
	}
	out := Both{Pace: MeasurePace(series, weekAccounts(v))}
	if out.Pace != nil {
		out.Pace.Source = "measured"
	}
	o, ok, err := weekly(v, cfg, out.Pace, sessionPaces(v, series, now), now)
	if err != nil {
		return Both{}, err
	}
	if ok {
		out.Week = &o
	}
	if o, ok := Compute(fiveAccounts(v), Five, nil, now); ok {
		out.FiveHour = &o
	}

	return out, nil
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

// fiveAccounts is the 5-hour window of every account that has one, against its own cap.
func fiveAccounts(v engine.View) []Account {
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
	return five
}

// sessionPaces is how fast each account's 5-hour window fills while it is used, in percent a
// second by key: its own rises in the history when they add up to enough work, else the 5-hour
// windows' average now in its own percent. An account with neither has none: its window never
// fills.
func sessionPaces(v engine.View, h Series, now time.Time) map[string]float64 {
	weights := weightsOf(weekAccounts(v))
	average := windowAverage(fiveAccounts(v), Five, now)
	out := map[string]float64{}
	for i, a := range v.Accounts {
		if !a.FiveHour {
			continue
		}
		var risen float64
		worked := 0.0
		s := h.Session[a.AuthFile]
		for j := 1; j < len(s); j++ {
			if s[j] != nil && s[j-1] != nil && *s[j] > *s[j-1] {
				risen += *s[j] - *s[j-1]
				worked += float64(h.Step)
			}
		}
		switch {
		case worked > 0 && worked >= MinWork.Seconds():
			out[a.AuthFile] = risen / worked
		case average > 0:
			out[a.AuthFile] = average / weights[i]
		}
	}
	return out
}

// weekly is the weekly outlook, with the use moved through the accounts as the balancer moves it;
// `sessions` is each account's 5-hour pace in percent a second.
func weekly(v engine.View, cfg config.Config, measured *Pace, sessions map[string]float64, now time.Time) (Outlook, bool, error) {
	accounts := weekAccounts(v)
	return look(accounts, Week, measured, now, sequenceOf(v, cfg, weightsOf(accounts), sessions, now).drain)
}
