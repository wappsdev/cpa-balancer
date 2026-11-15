package policy

import (
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// Standing says what a project's account is to a request in one bucket.
type Standing int

const (
	// Usable: the account can take the request (Eligibility says yes).
	Usable Standing = iota
	// Detour: the account is held back for a reason that passes (a 5-hour cap, a CPA cooldown, stale
	// data, not listed), so the request goes elsewhere and the project keeps its account.
	Detour
	// Full: a fresh weekly reading is at or over the account's cap, so it is time to move the
	// project. Only a weekly cap is "full"; nothing else moves a project.
	Full
)

// Stand is the account's standing in a bucket. It uses Cap and Eligibility's own freshness rule,
// so "full" is the same weekly cap the switch uses.
func Stand(a config.Account, s Snapshot, listed bool, b quota.Bucket, now time.Time, th config.Thresholds) Standing {
	if !listed {
		return Detour
	}
	if ok, _ := Eligibility(a, s, listed, b, now, th, false); ok {
		return Usable
	}
	// Fable use counts toward the all-models week as well, so a full all-models week stops Fable too.
	weeks := []quota.Bucket{b}
	if b == quota.Fable {
		weeks = append(weeks, quota.Main)
	}
	for _, w := range weeks {
		if r, ok := s.Readings[w]; ok && r.ResetsAt != nil && now.Sub(r.ObservedAt) <= th.Stale() && r.Util >= Cap(a, *r.ResetsAt, now, th) {
			return Full
		}
	}
	return Detour
}

// Headroom is the weekly room an account has under its cap in a bucket, in Pro plans' percentage
// points: the room in percent times the plan's size, so a Max and a Pro compare. An unknown plan
// counts as one Pro plan. ok is false without a fresh reading.
func Headroom(a config.Account, s Snapshot, b quota.Bucket, now time.Time, th config.Thresholds) (float64, bool) {
	r, ok := s.Readings[b]
	if !ok || r.ResetsAt == nil || now.Sub(r.ObservedAt) > th.Stale() {
		return 0, false
	}
	weight := s.Weight
	if weight == 0 {
		weight = 1
	}
	return max(0, Cap(a, *r.ResetsAt, now, th)-r.Util) * weight, true
}
