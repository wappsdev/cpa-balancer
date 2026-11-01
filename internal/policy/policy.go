// Package policy holds the pure decision logic: which Claude account each quota bucket uses.
package policy

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/config"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

const AnchorSpacing = 10 * time.Minute

var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

type Snapshot struct {
	Readings    quota.Readings
	Unavailable bool
}

type Choice struct {
	Account string `json:"account"` // "" = no restriction
	Reason  string `json:"reason"`
	Relaxed bool   `json:"relaxed"` // reserves released or nothing eligible
	Credits bool   `json:"credits"` // every plan quota is used up; this account spends extra-usage credits
	// Provisional: some configured account had no data, so leaving this choice is not held back by dwell.
	Provisional bool `json:"provisional"`
}

type Decision struct {
	Main       Choice   `json:"main"`
	Fable      Choice   `json:"fable"`
	MainOrder  []string `json:"main_order"`
	FableOrder []string `json:"fable_order"`
	Warnings   []string `json:"warnings"`
}

type Active struct {
	Account     string    `json:"account"`
	Since       time.Time `json:"since"`
	Provisional bool      `json:"provisional,omitempty"`
}

// Cap is the highest weekly utilization (%) the account may reach in a bucket right now. A default
// reserve steps up at 48 and 12 hours before the reset; a custom one holds its cap until its own
// release time; both give way to the final cap at the end.
func Cap(a config.Account, resetsAt, now time.Time, th config.Thresholds) float64 {
	left := resetsAt.Sub(now)
	switch a.Reserve {
	case config.ReserveCustom:
		if left >= time.Duration(a.ReserveHours*float64(time.Hour)) {
			return a.ReserveCap
		}
	case config.ReserveDefault:
		if left >= 48*time.Hour {
			return th.ReserveCapFar
		}
		if left >= 12*time.Hour {
			return th.ReserveCapNear
		}
	}
	return th.FinalCap
}

func Eligibility(a config.Account, s Snapshot, listed bool, b quota.Bucket, now time.Time, th config.Thresholds,
	releaseReserve bool) (bool, string) {
	if !listed {
		return false, "not listed by CPA"
	}
	weekly, ok := s.Readings[b]
	if !ok || weekly.ResetsAt == nil || now.Sub(weekly.ObservedAt) > th.Stale() {
		return false, "no recent quota data"
	}
	if s.Unavailable {
		return false, "disabled in CPA"
	}
	limit := th.FinalCap
	if !releaseReserve {
		limit = Cap(a, *weekly.ResetsAt, now, th)
	}
	if weekly.Util >= limit {
		return false, fmt.Sprintf("weekly %.0f%% >= cap %.0f%%", weekly.Util, limit)
	}
	// Fable use counts toward the all-models week as well, so a full week stops Fable too.
	if all, ok := s.Readings[quota.Main]; b == quota.Fable && ok && all.ResetsAt != nil && now.Sub(all.ObservedAt) <= th.Stale() {
		allLimit := th.FinalCap
		if !releaseReserve {
			allLimit = Cap(a, *all.ResetsAt, now, th)
		}
		if all.Util >= allLimit {
			return false, fmt.Sprintf("all-models week %.0f%% >= cap %.0f%%", all.Util, allLimit)
		}
	}
	if session, ok := s.Readings[quota.Session]; ok && now.Sub(session.ObservedAt) <= th.Stale() && session.Util >= th.SessionCap {
		return false, fmt.Sprintf("5h %.0f%% >= %.0f%%", session.Util, th.SessionCap)
	}
	return true, fmt.Sprintf("weekly %.0f%% < cap %.0f%%, resets %s", weekly.Util, limit,
		weekly.ResetsAt.UTC().Format("2006-01-02 15:04 UTC"))
}

func Choose(b quota.Bucket, cfg config.Config, snaps map[string]Snapshot, now time.Time, cur Active) Choice {
	th := cfg.Thresholds
	provisional := false
	for _, release := range []bool{false, true} {
		notes := map[string]string{}
		var ranked []string
		for _, a := range cfg.Accounts {
			s, listed := snaps[a.AuthFile]
			ok, why := Eligibility(a, s, listed, b, now, th, release)
			notes[a.AuthFile] = why
			provisional = provisional || why == "no recent quota data" || why == "not listed by CPA"
			if ok {
				ranked = append(ranked, a.AuthFile)
			}
		}
		if len(ranked) == 0 {
			continue
		}
		sort.SliceStable(ranked, func(i, j int) bool {
			ri, rj := *snaps[ranked[i]].Readings[b].ResetsAt, *snaps[ranked[j]].Readings[b].ResetsAt
			if !ri.Equal(rj) {
				return ri.Before(rj)
			}
			return ranked[i] == cur.Account && ranked[j] != cur.Account
		})
		best := ranked[0]
		choice := func(account, reason string) Choice {
			return Choice{Account: account, Reason: reason, Relaxed: release, Provisional: provisional}
		}
		switch {
		case cur.Account == "" || cur.Account == best:
			return choice(best, "earliest reset; "+notes[best])
		case !slices.Contains(ranked, cur.Account):
			why := notes[cur.Account]
			if why == "" {
				why = "not configured"
			}
			return choice(best, fmt.Sprintf("forced switch from %s (%s); %s", cur.Account, why, notes[best]))
		case cur.Since.IsZero() || cur.Provisional || now.Sub(cur.Since) >= th.MinDwell():
			return choice(best, "preferred switch; "+notes[best])
		default:
			return choice(cur.Account, fmt.Sprintf("staying until the %d min dwell passes; %s", th.MinDwellMinutes,
				notes[cur.Account]))
		}
	}
	if c, ok := creditChoice(b, cfg, snaps, cur, provisional); ok {
		return c
	}
	return Choice{Reason: "no eligible account even with reserves released; no restriction", Relaxed: true, Provisional: provisional}
}

// creditChoice is the last resort: every plan quota is used up, so the account with extra-usage
// credits that resets soonest takes the traffic (the current one on a tie).
func creditChoice(b quota.Bucket, cfg config.Config, snaps map[string]Snapshot, cur Active, provisional bool) (Choice, bool) {
	var paying []string
	for _, a := range cfg.Accounts {
		if s, listed := snaps[a.AuthFile]; a.ExtraUsage && listed && !s.Unavailable {
			paying = append(paying, a.AuthFile)
		}
	}
	if len(paying) == 0 {
		return Choice{}, false
	}
	sort.SliceStable(paying, func(i, j int) bool {
		ri, rj := resetOf(snaps, paying[i], b), resetOf(snaps, paying[j], b)
		if !ri.Equal(rj) {
			return ri.Before(rj)
		}
		return paying[i] == cur.Account && paying[j] != cur.Account
	})

	return Choice{Account: paying[0], Reason: "every plan quota is used up; spending extra-usage credits on the earliest reset", Relaxed: true, Credits: true, Provisional: provisional}, true
}

func resetOf(snaps map[string]Snapshot, account string, b quota.Bucket) time.Time {
	if r, ok := snaps[account].Readings[b]; ok && r.ResetsAt != nil {
		return *r.ResetsAt
	}
	return farFuture
}

// order puts the chosen account first and the rest by reset time, for pick fallbacks.
func order(b quota.Bucket, chosen string, cfg config.Config, snaps map[string]Snapshot) []string {
	if chosen == "" {
		return nil
	}
	var rest []string
	for _, a := range cfg.Accounts {
		if a.AuthFile != chosen {
			rest = append(rest, a.AuthFile)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return resetOf(snaps, rest[i], b).Before(resetOf(snaps, rest[j], b)) })
	return append([]string{chosen}, rest...)
}

func Decide(snaps map[string]Snapshot, cfg config.Config, now time.Time, active map[quota.Bucket]Active) Decision {
	d := Decision{Main: Choose(quota.Main, cfg, snaps, now, active[quota.Main])}
	if len(cfg.Buckets.Fable) > 0 {
		d.Fable = Choose(quota.Fable, cfg, snaps, now, active[quota.Fable])
	} else {
		d.Fable = Choice{Reason: "fable bucket not configured"}
	}
	d.MainOrder = order(quota.Main, d.Main.Account, cfg, snaps)
	d.FableOrder = order(quota.Fable, d.Fable.Account, cfg, snaps)
	for _, x := range []struct {
		b quota.Bucket
		c Choice
	}{{quota.Main, d.Main}, {quota.Fable, d.Fable}} {
		if x.c.Relaxed {
			d.Warnings = append(d.Warnings, fmt.Sprintf("%s: %s", x.b, x.c.Reason))
		}
	}
	return d
}

// AnchorTargets lists accounts whose 5-hour window should be started now.
func AnchorTargets(d Decision, snaps map[string]Snapshot, cfg config.Config, now time.Time,
	lastAnchor, retryAfter map[string]time.Time) []string {
	active := d.Main.Account
	if !cfg.Anchor.Enabled || active == "" || !snaps[active].Readings[quota.Session].Running(now) {
		return nil // no work going on
	}
	var out []string
	for _, a := range cfg.Accounts {
		name := a.AuthFile
		s, ok := snaps[name]
		if name == active || !ok || s.Unavailable || s.Readings[quota.Session].Running(now) {
			continue
		}
		if weekly, ok := s.Readings[quota.Main]; !ok || weekly.Util >= cfg.Thresholds.FinalCap {
			continue
		}
		if t := lastAnchor[name]; !t.IsZero() && now.Sub(t) < AnchorSpacing {
			continue
		}
		if t := retryAfter[name]; !t.IsZero() && now.Before(t) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// AnchorDidNotStartWindow: a probe taken after an anchor shows a closed window the anchor said would still run.
func AnchorDidNotStartWindow(probeSession quota.Reading, hasProbe bool, anchorReading *quota.Reading, lastAnchor time.Time) bool {
	return hasProbe && probeSession.ResetsAt == nil && !lastAnchor.IsZero() && probeSession.ObservedAt.After(lastAnchor) &&
		anchorReading != nil && anchorReading.ResetsAt != nil && anchorReading.ResetsAt.After(probeSession.ObservedAt)
}
