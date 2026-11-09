package engine

import (
	"github.com/wappsdev/cpa-balancer/internal/config"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/probe"
	"github.com/wappsdev/cpa-balancer/internal/quota"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// NextCap is the cap a window moves to next and when.
type NextCap struct {
	Cap float64   `json:"cap"`
	At  time.Time `json:"at"`
}

type AccountView struct {
	AuthFile     string                     `json:"auth_file"`
	Name         string                     `json:"name"`                    // the account's label, or a name from its e-mail
	Reserve      string                     `json:"reserve"`                 // off, default or custom
	ReserveSteps []config.ReserveStep       `json:"reserve_steps,omitempty"` // a custom reserve's steps
	SessionSteps []config.ReserveStep       `json:"session_steps,omitempty"` // its own 5-hour steps; absent, the default cap
	ExtraUsage   bool                       `json:"extra_usage"`
	Listed       bool                       `json:"listed"`
	FiveHour     bool                       `json:"five_hour"`        // the plan has a 5-hour limit (true until a probe says otherwise)
	Fable        bool                       `json:"fable"`            // the plan has a Fable weekly limit (true until a probe says otherwise)
	Plan         string                     `json:"plan,omitempty"`   // max_20x, max_5x or pro; empty while unknown
	Weight       float64                    `json:"weight,omitempty"` // the weekly limit in Pro plans: 10, 5 or 1
	Unavailable  bool                       `json:"unavailable"`      // CPA holds the credential back (cooldown or disabled)
	Readings     quota.Readings             `json:"readings"`
	Eligibility  map[quota.Bucket]string    `json:"eligibility"`
	Caps         map[quota.Bucket]float64   `json:"caps"`                // the switch threshold in force per bucket
	NextCaps     map[quota.Bucket]NextCap   `json:"next_caps,omitempty"` // where the cap changes next before the reset
	Reopens      map[quota.Bucket]time.Time `json:"reopens,omitempty"`   // when a held account takes work again
	LastAnchor   time.Time                  `json:"last_anchor,omitzero"`
	AnchorModel  string                     `json:"anchor_model,omitempty"`
}

type View struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Mode        string          `json:"mode"`
	Paused      bool            `json:"paused"`
	ConfigError string          `json:"config_error,omitempty"` // set by the plugin when the last config was rejected
	FinalCap    float64         `json:"final_cap"`              // every account's cap once reserves are released
	Decision    policy.Decision `json:"decision"`
	Accounts    []AccountView   `json:"accounts"`
	Changes     []state.Change  `json:"changes"`
}

// Status is what the status page shows; it never contains credentials.
func (e *Engine) Status() View {
	now := e.now()
	e.mu.RLock()
	defer e.mu.RUnlock()
	v := View{GeneratedAt: now, Mode: e.cfg.Mode, Paused: e.cfg.Paused, FinalCap: e.cfg.Thresholds.FinalCap, Decision: e.decision, Changes: append([]state.Change(nil), e.st.Changes...)}
	names := e.cfg.Names()
	for _, a := range e.cfg.Accounts {
		snap := e.snaps[a.AuthFile]
		cred, listed := e.credentials[a.AuthFile]
		av := AccountView{AuthFile: a.AuthFile, Name: names[a.AuthFile], Reserve: a.Reserve, ReserveSteps: a.ReserveSteps, SessionSteps: a.SessionSteps, ExtraUsage: a.ExtraUsage, Listed: listed, Unavailable: cred.unavailable,
			Plan: e.st.Account(a.AuthFile).Plan.Name, Weight: probe.WeightOf(e.st.Account(a.AuthFile).Plan.Name), Readings: snap.Readings,
			Eligibility: map[quota.Bucket]string{}, Caps: map[quota.Bucket]float64{}, NextCaps: map[quota.Bucket]NextCap{}, Reopens: map[quota.Bucket]time.Time{}}
		av.FiveHour = !e.st.Account(a.AuthFile).NoFiveHour
		av.Fable = !e.st.Account(a.AuthFile).NoFable
		if av.FiveHour {
			av.Caps[quota.Session] = policy.SessionCap(a, snap.Readings[quota.Session].ResetsAt, now, e.cfg.Thresholds)
			if r := snap.Readings[quota.Session]; r.ResetsAt != nil {
				if c, at, ok := policy.NextCap(a, true, *r.ResetsAt, now, e.cfg.Thresholds); ok {
					av.NextCaps[quota.Session] = NextCap{Cap: c, At: at}
				}
			}
		}
		for _, b := range []quota.Bucket{quota.Main, quota.Fable} {
			ok, why := policy.Eligibility(a, snap, true, b, now, e.cfg.Thresholds, false)
			av.Eligibility[b] = why
			if !ok {
				if at, opens := policy.Reopens(a, snap, true, b, now, e.cfg.Thresholds); opens {
					av.Reopens[b] = at
				}
			}
			if r, ok := snap.Readings[b]; ok && r.ResetsAt != nil {
				av.Caps[b] = policy.Cap(a, *r.ResetsAt, now, e.cfg.Thresholds)
				if c, at, ok := policy.NextCap(a, false, *r.ResetsAt, now, e.cfg.Thresholds); ok {
					av.NextCaps[b] = NextCap{Cap: c, At: at}
				}
			}
		}
		if acc := e.st.Accounts[a.AuthFile]; acc != nil && !acc.LastAnchor.IsZero() {
			av.LastAnchor = acc.LastAnchor
			av.AnchorModel = e.cfg.Anchor.Model
			if acc.AnchorMainModel {
				av.AnchorModel = e.cfg.Anchor.FallbackModel
			}
		}
		v.Accounts = append(v.Accounts, av)
	}
	return v
}
