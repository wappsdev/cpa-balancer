package engine

import (
	"fmt"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/quota"
)

// RecheckResult is one account's state right after a probe made on request.
type RecheckResult struct {
	AuthFile  string  `json:"auth_file"`
	AuthIndex string  `json:"auth_index"`
	Locked    bool    `json:"locked"`   // CPA keeps the credential out of rotation after a 429
	Disabled  bool    `json:"disabled"` // switched off by hand; a cooldown reset does not bring it back
	Open      bool    `json:"open"`     // Anthropic takes requests again: weekly and 5-hour under 100%
	Weekly    float64 `json:"weekly"`
	Session   float64 `json:"session"`
	Error     string  `json:"error,omitempty"`
}

// Recheck probes every account (or only the one named) now, outside the probe schedule, and
// re-decides. CPA keeps a credential cooled until its weekly reset even when the window opens
// early (a manual reset), and a plugin cannot lift that; the caller lifts it for Locked && Open.
func (e *Engine) Recheck(only string) []RecheckResult {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	now := e.now()
	e.refreshCredentials()
	out := []RecheckResult{}
	for _, a := range e.cfg.Accounts {
		if only != "" && a.AuthFile != only {
			continue
		}
		e.mu.RLock()
		cred, listed := e.credentials[a.AuthFile]
		retryAfter := e.st.Account(a.AuthFile).RetryAfter
		e.mu.RUnlock()
		r := RecheckResult{AuthFile: a.AuthFile, AuthIndex: cred.index, Locked: cred.unavailable && !cred.disabled, Disabled: cred.disabled}
		switch {
		case !listed:
			r.Error = "not listed by CPA"
		case now.Before(retryAfter):
			r.Error = fmt.Sprintf("usage endpoint asked to wait until %s", retryAfter.Format(time.Kitchen))
		default:
			e.recheckOne(&r, now)
		}
		out = append(out, r)
	}
	e.mu.Lock()
	e.recompute(now)
	e.mu.Unlock()
	e.save()
	return out
}

func (e *Engine) recheckOne(r *RecheckResult, now time.Time) {
	res := e.runProbe(r.AuthIndex, now)
	e.mu.Lock()
	defer e.mu.Unlock()
	acc := e.st.Account(r.AuthFile)
	acc.LastProbe, acc.RetryAfter = now, res.RetryAfter
	e.dirty = true
	if res.Err != nil {
		r.Error = res.Err.Error()
		return
	}
	acc.Probe = res.Readings
	weekly, hasWeekly := res.Readings[quota.Main]
	session := res.Readings[quota.Session]
	r.Weekly, r.Session = weekly.Util, session.Util
	r.Open = hasWeekly && weekly.Util < 100 && session.Util < 100
}
