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
	NoFiveHour  bool // the plan has no 5-hour limit, so it has no window to start
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

// SessionCap is where the account's 5-hour window switches it away now: its own step in force, else
// the thresholds' session_cap. A closed window (resetsAt nil) takes the last step.
func SessionCap(a config.Account, resetsAt *time.Time, now time.Time, th config.Thresholds) float64 {
	if len(a.SessionSteps) == 0 {
		return th.SessionCap
	}
	left := time.Duration(0)
	if resetsAt != nil {
		left = resetsAt.Sub(now)
	}
	return stepCap(a.SessionSteps, left, th.SessionCap)
}

// stepCap is the cap of the step with the most hours that have not yet run out, else last.
func stepCap(steps []config.ReserveStep, left time.Duration, last float64) float64 {
	best := -1.0
	limit := last
	for _, s := range steps {
		if left >= time.Duration(s.Hours*float64(time.Hour)) && s.Hours > best {
			best, limit = s.Hours, s.Cap
		}
	}
	return limit
}

// Cap is the highest weekly utilization (%) the account may reach in a bucket right now: the
// reserve step (default: the thresholds' steps; custom: the account's own) with the most hours
// that have not yet run out, else the final cap.
func Cap(a config.Account, resetsAt, now time.Time, th config.Thresholds) float64 {
	steps := map[string][]config.ReserveStep{config.ReserveDefault: th.Reserve, config.ReserveCustom: a.ReserveSteps}[a.Reserve]
	return stepCap(steps, resetsAt.Sub(now), th.FinalCap)
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
	if session, ok := s.Readings[quota.Session]; ok && now.Sub(session.ObservedAt) <= th.Stale() && session.Util >= SessionCap(a, session.ResetsAt, now, th) {
		return false, fmt.Sprintf("5h %.0f%% >= %.0f%%", session.Util, SessionCap(a, session.ResetsAt, now, th))
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

// order puts the chosen account first, then the accounts that can take work now, then the rest,
// each group by reset time: the next account shown, and the pick's fallbacks, never skip an
// account with room for one at its cap.
func order(b quota.Bucket, chosen string, cfg config.Config, snaps map[string]Snapshot, now time.Time) []string {
	if chosen == "" {
		return nil
	}
	var rest []config.Account
	for _, a := range cfg.Accounts {
		if a.AuthFile != chosen {
			rest = append(rest, a)
		}
	}
	open := map[string]bool{}
	for _, a := range rest {
		s, listed := snaps[a.AuthFile]
		open[a.AuthFile], _ = Eligibility(a, s, listed, b, now, cfg.Thresholds, false)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		x, y := rest[i].AuthFile, rest[j].AuthFile
		if open[x] != open[y] {
			return open[x]
		}
		return resetOf(snaps, x, b).Before(resetOf(snaps, y, b))
	})
	out := []string{chosen}
	for _, a := range rest {
		out = append(out, a.AuthFile)
	}
	return out
}

func Decide(snaps map[string]Snapshot, cfg config.Config, now time.Time, active map[quota.Bucket]Active) Decision {
	d := Decision{Main: Choose(quota.Main, cfg, snaps, now, active[quota.Main])}
	if len(cfg.Buckets.Fable) > 0 {
		d.Fable = Choose(quota.Fable, cfg, snaps, now, active[quota.Fable])
	} else {
		d.Fable = Choice{Reason: "fable bucket not configured"}
	}
	d.MainOrder = order(quota.Main, d.Main.Account, cfg, snaps, now)
	d.FableOrder = order(quota.Fable, d.Fable.Account, cfg, snaps, now)
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
		if name == active || !ok || s.Unavailable || s.NoFiveHour || s.Readings[quota.Session].Running(now) {
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
