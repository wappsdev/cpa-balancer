package history

import (
	"fmt"
	"time"
)

var ranges = map[string]struct{ span, step time.Duration }{
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 2 * time.Hour},
	"90d": {90 * 24 * time.Hour, 6 * time.Hour},
}

type AccountSeries struct {
	Main     []*float64         `json:"main"`
	MainCap  []*float64         `json:"main_cap"`
	Fable    []*float64         `json:"fable"`
	FableCap []*float64         `json:"fable_cap"`
	Session  []*float64         `json:"session"`
	Requests []int64            `json:"requests"`
	Tokens   []int64            `json:"tokens"` // input + output
	Models   map[string][]int64 `json:"models"` // requests per model
}

type Series struct {
	Range    string                    `json:"range"`
	Step     int64                     `json:"step_seconds"`
	T        []int64                   `json:"t"` // unix seconds, point start
	Accounts map[string]*AccountSeries `json:"accounts"`
	Active   map[string][]string       `json:"active"` // bucket -> account per point ("" = none)
	Events   []Event                   `json:"events"`
}

func maxPtr(cur, v *float64) *float64 {
	if v == nil || (cur != nil && *cur >= *v) {
		return cur
	}
	x := *v
	return &x
}

// Query downsamples a range: utilization takes the max, traffic the sum, the active account the
// one seen in most slots.
func (r *Recorder) Query(rangeName string, now time.Time) (Series, error) {
	rg, ok := ranges[rangeName]
	if !ok {
		return Series{}, fmt.Errorf("range must be one of 24h, 7d, 30d, 90d")
	}
	n := int(rg.span / rg.step)
	end := now.UTC().Truncate(rg.step).Add(rg.step)
	start := end.Add(-time.Duration(n) * rg.step)
	out := Series{Range: rangeName, Step: int64(rg.step / time.Second), T: make([]int64, n),
		Accounts: map[string]*AccountSeries{}, Active: map[string][]string{}, Events: []Event{}}
	for i := range n {
		out.T[i] = start.Add(time.Duration(i) * rg.step).Unix()
	}
	series := func(name string) *AccountSeries {
		a, ok := out.Accounts[name]
		if !ok {
			a = &AccountSeries{Main: make([]*float64, n), MainCap: make([]*float64, n), Fable: make([]*float64, n),
				FableCap: make([]*float64, n), Session: make([]*float64, n), Requests: make([]int64, n),
				Tokens: make([]int64, n), Models: map[string][]int64{}}
			out.Accounts[name] = a
		}
		return a
	}
	votes := map[string][]map[string]int{} // bucket -> point -> account -> slots
	for _, s := range r.slots(start, end) {
		i := int(s.T.Sub(start) / rg.step)
		if i < 0 || i >= n {
			continue
		}
		for name, a := range s.Accounts {
			as := series(name)
			as.Main[i], as.MainCap[i] = maxPtr(as.Main[i], a.Main), maxPtr(as.MainCap[i], a.MainCap)
			as.Fable[i], as.FableCap[i] = maxPtr(as.Fable[i], a.Fable), maxPtr(as.FableCap[i], a.FableCap)
			if a.SessionOpen {
				as.Session[i] = maxPtr(as.Session[i], a.Session)
			}
			for model, t := range a.Models {
				as.Requests[i] += t.Requests
				as.Tokens[i] += t.InputTokens + t.OutputTokens
				if _, ok := as.Models[model]; !ok {
					as.Models[model] = make([]int64, n)
				}
				as.Models[model][i] += t.Requests
			}
		}
		for bucket, account := range s.Active {
			if votes[bucket] == nil {
				votes[bucket] = make([]map[string]int, n)
			}
			if votes[bucket][i] == nil {
				votes[bucket][i] = map[string]int{}
			}
			votes[bucket][i][account]++
		}
		out.Events = append(out.Events, s.Events...)
	}
	for bucket, points := range votes {
		out.Active[bucket] = make([]string, n)
		for i, counts := range points {
			best, most := "", 0
			for account, c := range counts {
				if c > most || (c == most && account < best) {
					best, most = account, c
				}
			}
			out.Active[bucket][i] = best
		}
	}
	return out, nil
}
