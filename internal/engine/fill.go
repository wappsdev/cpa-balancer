package engine

import (
	"fmt"
	"maps"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// Fill spends each account's week before it resets: a session that starts without a pin goes to the
// usable account whose weekly reset comes first. A running session is never moved by it (that would
// cost its prompt cache); it keeps the account it started on until that account is full.

// week is the length of a weekly window.
const week = 7 * 24 * time.Hour

// sameReset is how close two weekly resets are to count as one: a tie goes to the balancing rule.
const sameReset = time.Minute

// started is the account a main session started on, kept in memory with the picks.
type started struct {
	account string
	at      time.Time // the session's last request
}

// fillFirst is the part of usable that fill prefers: the accounts whose weekly reset comes first,
// among those with a real reading (an estimate made after a reset is not one) and at least
// fill.min_room of room under their cap. Only a usable account has a fresh reading, so an account
// with an unknown one is never in it. Empty when none qualifies: then the balancing rule decides.
// Callers hold e.mu.
func (e *Engine) fillFirst(usable []string, now time.Time) []string {
	var first time.Time
	resets := map[string]time.Time{}
	for _, a := range usable {
		r, ok := e.snaps[a].Readings[quota.Main]
		if !ok || r.ResetsAt == nil || r.Source == "estimate" || e.standing(a, quota.Main, now) != policy.Usable {
			continue
		}
		if why := e.fillWhy(a, now); why == nil || why.Room < e.cfg.Fill.MinRoom {
			continue
		}
		resets[a] = *r.ResetsAt
		if first.IsZero() || r.ResetsAt.Before(first) {
			first = *r.ResetsAt
		}
	}
	var out []string
	for _, a := range usable {
		if at, ok := resets[a]; ok && at.Sub(first) < sameReset {
			out = append(out, a)
		}
	}
	return out
}

// fillWhy is what fill saw of an account: its weekly reset and its room under the cap, in percent.
func (e *Engine) fillWhy(account string, now time.Time) *state.Fill {
	r := e.snaps[account].Readings[quota.Main]
	for _, a := range e.cfg.Accounts {
		if a.AuthFile == account && r.ResetsAt != nil {
			return &state.Fill{ResetsAt: *r.ResetsAt, Room: max(0, policy.Cap(a, *r.ResetsAt, now, e.cfg.Thresholds)-r.Util)}
		}
	}
	return nil
}

// pastReset reports whether the account's week has reset since the project was placed on it.
func (e *Engine) pastReset(h *state.Home) bool {
	r, ok := e.snaps[h.Account].Readings[quota.Main]
	return ok && r.ResetsAt != nil && r.ResetsAt.Add(-week).After(h.Since)
}

// usableAccounts are the accounts CPA offers (all of them for a nil offer) that can take a
// request of the main bucket now. Callers hold e.mu.
func (e *Engine) usableAccounts(offered map[string]bool, now time.Time) []string {
	var out []string
	for _, a := range e.cfg.Accounts {
		if (offered == nil || offered[a.AuthFile]) && e.standing(a.AuthFile, quota.Main, now) == policy.Usable {
			out = append(out, a.AuthFile)
		}
	}
	return out
}

// sessionHome is the account of a session of a project without a pin, under fill. A running session
// keeps the account it started on until that account is full. A session that starts uses the
// project's home while that account is usable and has not reset since the project was placed;
// otherwise fill places the project again, and why says what it saw. "" when there is no account.
// Callers hold e.mu.
func (e *Engine) sessionHome(root, project string, offered map[string]bool, now time.Time) (account string, why *state.Fill) {
	if s, ok := e.starts[root]; ok && e.standing(s.account, quota.Main, now) != policy.Full {
		e.startOn(root, s.account, now)
		e.st.TouchHome(project, now)
		return s.account, nil
	}
	h := e.st.Affinity.Homes[project]
	if h != nil && e.standing(h.Account, quota.Main, now) == policy.Usable && !e.pastReset(h) {
		e.st.TouchHome(project, now)
		e.startOn(root, h.Account, now)
		return h.Account, nil
	}
	usable := e.usableAccounts(offered, now)
	first := e.fillFirst(usable, now)
	byFill := len(first) > 0
	if !byFill {
		first = usable // no account has fill's room: the balancing rule places the project
	}
	best := e.balanced(project, first, now)
	if best == "" {
		if h != nil {
			return h.Account, nil // nothing can take it: the request takes a detour, the home stays
		}
		return "", nil
	}
	if byFill {
		why = e.fillWhy(best, now)
	}
	e.st.PlaceHome(project, best, why, now)
	e.dirty = true
	if h != nil && h.Account != best {
		e.host.Log("info", fmt.Sprintf("cpa-balancer: fill moved a project from %q to %q", h.Account, best))
	}
	e.startOn(root, best, now)
	return best, why
}

// Inherit takes over what the engine it replaces kept in memory about recent sessions: where each
// went and where each started, so a config change does not move a running session. An engine of
// another state file is another set of homes, so nothing is taken from it.
func (e *Engine) Inherit(prev *Engine) {
	if prev.cfg.StatePath != e.cfg.StatePath {
		return
	}
	prev.mu.RLock()
	picks, starts, pinStarts := maps.Clone(prev.picks), maps.Clone(prev.starts), maps.Clone(prev.pinStarts)
	prev.mu.RUnlock()
	e.mu.Lock()
	e.picks, e.starts, e.pinStarts = picks, starts, pinStarts
	e.mu.Unlock()
}

// startOn notes the account a main session runs on under fill.
func (e *Engine) startOn(root, account string, now time.Time) {
	e.startIn(&e.starts, root, account, now)
}

// startIn notes the account a session runs on in one of the session maps; at the limit the session
// idle longest goes.
func (e *Engine) startIn(m *map[string]started, key, account string, now time.Time) {
	if *m == nil {
		*m = map[string]started{}
	}
	if _, ok := (*m)[key]; !ok && len(*m) >= MaxRememberedSessions {
		oldest, first := "", true
		for k, s := range *m {
			if first || s.at.Before((*m)[oldest].at) {
				oldest, first = k, false
			}
		}
		delete(*m, oldest)
	}
	(*m)[key] = started{account: account, at: now}
}
