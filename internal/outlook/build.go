package outlook

import (
	"time"

	"github.com/adnankurt/cpa-balancer/internal/engine"
	"github.com/adnankurt/cpa-balancer/internal/history"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

// Build looks ahead from the status and a week of history: the weekly window of every account
// against the final cap, at the pace the history shows.
func Build(v engine.View, h history.Series, now time.Time) Outlook {
	accounts := make([]Account, 0, len(v.Accounts))
	for _, a := range v.Accounts {
		acc := Account{Key: a.AuthFile, Name: a.Name, Weight: a.Weight}
		if r, ok := a.Readings[quota.Main]; ok && r.ResetsAt != nil {
			acc.Util, acc.ResetsAt = r.Util, *r.ResetsAt
		}
		accounts = append(accounts, acc)
	}
	series := Series{Step: h.Step, T: h.T, Main: map[string][]*float64{}}
	for name, s := range h.Accounts {
		series.Main[name] = s.Main
	}

	return Compute(accounts, v.FinalCap, MeasurePace(series, accounts), now)
}
