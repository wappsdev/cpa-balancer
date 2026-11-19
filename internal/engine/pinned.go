package engine

import (
	"slices"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/affinity"
	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// A request pinned to an extra model's accounts (one for the model itself, or one that carries the
// pinned advisor) goes to one of them or is refused; it never falls back. Among them the choice
// follows the normal path's rules, in tiers:
//  1. usable: under its caps, its reserve included, with a fresh reading, not held back by CPA;
//  2. held back by its reserve only: under the final cap, its 5-hour window under its cap;
//  3. no fresh reading (a pause stops the probes, a new account has none, and an estimate made
//     after a reset is not one), not held back by CPA.
//
// Within a tier the weekly reset that comes first goes first, then the extra model's order. A
// higher tier is used only when the tiers before it are empty, so a reserve is spent only when
// nothing else can take the request: refusing would end the session. An account at its final cap
// or its 5-hour cap, or one CPA holds back, is never chosen, and with no account in any tier the
// request is refused. A main session stays on the pinned account it started on (its prompt cache
// is there) until that account is full; while the account cannot take a request for a passing
// reason (a 5-hour cap, a cooldown), that request goes to the next account and the session returns.

// pinTier is an account's tier for a pinned request in a bucket, 0 when it cannot take one. Callers
// hold e.mu.
func (e *Engine) pinTier(account string, b quota.Bucket, now time.Time) int {
	i := slices.IndexFunc(e.cfg.Accounts, func(a config.Account) bool { return a.AuthFile == account })
	if i < 0 {
		return 0
	}
	a, s, th := e.cfg.Accounts[i], e.snaps[account], e.cfg.Thresholds
	if r, ok := s.Readings[b]; ok && r.Source == "estimate" {
		// made after a reset, until a real reading comes: as good as none
		if s.Unavailable {
			return 0
		}
		return 3
	}
	if ok, _ := policy.Eligibility(a, s, true, b, now, th, false); ok {
		return 1
	}
	if ok, _ := policy.Eligibility(a, s, true, b, now, th, true); ok {
		return 2
	}
	if r, ok := s.Readings[b]; (!ok || r.ResetsAt == nil || now.Sub(r.ObservedAt) > th.Stale()) && !s.Unavailable {
		return 3
	}
	return 0
}

// pinBucket is the bucket whose limits a pinned request draws on: its model's, else the all-models
// week (an extra model is in no bucket).
func (e *Engine) pinBucket(model string) quota.Bucket {
	if b, ok := e.bucketOf(model); ok {
		return b
	}
	return quota.Main
}

// pickPinned chooses among m's accounts for a request; callers hold e.mu. See the rules above.
func (e *Engine) pickPinned(m config.ExtraModel, req pluginapi.SchedulerPickRequest, now time.Time) (pluginapi.SchedulerPickResponse, bool) {
	candidates := map[string]bool{}
	for _, c := range req.Candidates {
		candidates[c.ID] = true
	}
	use := func(a string) (pluginapi.SchedulerPickResponse, bool) {
		return pluginapi.SchedulerPickResponse{AuthID: a, Handled: true}, true
	}
	// CPA's own pin (a continued exchange) is kept when it is one of the accounts, else refused.
	if pinned, _ := req.Options.Metadata["pinned_auth_id"].(string); pinned != "" {
		if slices.Contains(m.Accounts, pinned) && candidates[pinned] {
			return use(pinned)
		}
		return pluginapi.SchedulerPickResponse{}, false
	}
	b := e.pinBucket(req.Model)
	_, root := affinity.SessionID(req.Options.Headers)
	key := root + "\x00" + m.ID
	if s, ok := e.pinStarts[key]; ok && root != "" && slices.Contains(m.Accounts, s.account) {
		switch {
		case candidates[s.account] && e.pinTier(s.account, b, now) == 1:
			e.startIn(&e.pinStarts, key, s.account, now)
			return use(s.account)
		case e.standing(s.account, b, now) != policy.Full:
			// a detour for this request: the session keeps its account
			if a := e.firstPinned(m, b, candidates, now); a != "" {
				return use(a)
			}
			return pluginapi.SchedulerPickResponse{}, false
		}
	}
	a := e.firstPinned(m, b, candidates, now)
	if a == "" {
		return pluginapi.SchedulerPickResponse{}, false
	}
	if root != "" {
		e.startIn(&e.pinStarts, key, a, now)
	}
	return use(a)
}

// firstPinned is the first of m's accounts CPA offers by tier, then by reset, then in m's order; ""
// when none can take the request. Callers hold e.mu.
func (e *Engine) firstPinned(m config.ExtraModel, b quota.Bucket, candidates map[string]bool, now time.Time) string {
	type option struct {
		account string
		tier    int
		reset   time.Time
	}
	var options []option
	for _, a := range m.Accounts {
		if !candidates[a] {
			continue
		}
		o := option{account: a, tier: e.pinTier(a, b, now)}
		if o.tier == 0 {
			continue
		}
		if r, ok := e.snaps[a].Readings[b]; ok && r.ResetsAt != nil && o.tier < 3 {
			o.reset = *r.ResetsAt
		}
		options = append(options, o)
	}
	slices.SortStableFunc(options, func(x, y option) int {
		if x.tier != y.tier {
			return x.tier - y.tier
		}
		return x.reset.Compare(y.reset)
	})
	if len(options) == 0 {
		return ""
	}
	return options[0].account
}

// pinnedPick answers a request pinned to an extra model's accounts: one for the model itself, else
// one that carries the advisor tool of the advisor pin. ok is false for any other request.
func (e *Engine) pinnedPick(req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, ok bool) {
	m, isExtra := e.cfg.ExtraModel(req.Model)
	advisor := false
	if !isExtra {
		if m, advisor = e.cfg.AdvisorModel(req.Options.Headers); !advisor {
			return pluginapi.SchedulerPickResponse{}, false
		}
	}
	e.mu.Lock()
	resp, picked := e.pickPinned(m, req, e.now())
	e.mu.Unlock()
	switch {
	case picked:
		return resp, true
	case advisor:
		return refuseAdvisor(m.ID), true
	}
	return refuseExtra(m.ID), true
}
