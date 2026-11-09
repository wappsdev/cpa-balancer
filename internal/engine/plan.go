package engine

import (
	"fmt"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/probe"
)

const (
	planEvery = 24 * time.Hour
	planRetry = time.Hour
)

// checkPlans reads each account's plan from its profile once a day, so 1% of a Max 20x and 1%
// of a Pro can be compared (the HUD weighs them). A failed read is tried again within the hour.
func (e *Engine) checkPlans(now time.Time) {
	type job struct{ name, index string }
	var jobs []job
	e.mu.RLock()
	for _, a := range e.cfg.Accounts {
		cred, ok := e.credentials[a.AuthFile]
		acc := e.st.Accounts[a.AuthFile]
		if !ok || (acc != nil && now.Sub(acc.PlanCheckedAt) < planEvery) {
			continue
		}
		jobs = append(jobs, job{a.AuthFile, cred.index})
	}
	e.mu.RUnlock()
	for _, j := range jobs {
		e.refreshPlan(j.name, j.index, now)
	}
}

// refreshPlan reads one account's plan now; a failed read is tried again within the hour.
func (e *Engine) refreshPlan(name, index string, now time.Time) {
	plan, err := e.readPlan(index)
	e.mu.Lock()
	defer e.mu.Unlock()
	acc := e.st.Account(name)
	if err != nil {
		acc.PlanCheckedAt = now.Add(planRetry - planEvery)
		e.host.Log("warn", fmt.Sprintf("cpa-balancer: plan of %s: %v", name, err))
	} else {
		acc.Plan, acc.PlanCheckedAt = plan, now
	}
	e.dirty = true
}

func (e *Engine) readPlan(index string) (probe.Plan, error) {
	raw, err := e.host.AuthGet(index)
	if err != nil {
		return probe.Plan{}, fmt.Errorf("auth get: %w", err)
	}
	token, err := probe.AccessToken(raw)
	if err != nil {
		return probe.Plan{}, err
	}
	resp, err := e.host.HTTPDo(probe.ProfileRequest(token))
	if err != nil {
		return probe.Plan{}, fmt.Errorf("http: %w", err)
	}
	if resp.StatusCode != 200 {
		return probe.Plan{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return probe.ParsePlan(resp.Body)
}
