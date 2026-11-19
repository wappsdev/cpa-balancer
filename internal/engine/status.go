package engine

import (
	"github.com/wappsdev/cpa-balancer/internal/config"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
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
	FiveHour     bool                       `json:"five_hour"`               // the plan has a 5-hour limit (true until a probe says otherwise)
	Fable        bool                       `json:"fable"`                   // the plan has a Fable weekly limit (true until a probe says otherwise)
	Plan         string                     `json:"plan,omitempty"`          // max_20x, max_5x or pro; empty while unknown
	PlanLabel    string                     `json:"plan_label,omitempty"`    // the page's name for the plan
	Meter        *AccountMeter              `json:"meter,omitempty"`         // what the account's own meter says (see AddSessions); only an Ultra account has one
	ByProject    []AccountProject           `json:"by_project,omitempty"`    // the projects that used it most in the 7 days (see AddSessions)
	MoreProjects int                        `json:"more_projects,omitempty"` // how many more used it, past the list
	Weight       float64                    `json:"weight,omitempty"`        // the weekly limit in Pro plans: 10, 5 or 1
	Unavailable  bool                       `json:"unavailable"`             // CPA holds the credential back (cooldown or disabled)
	Disabled     bool                       `json:"disabled"`                // switched off by hand: no reset brings it back
	Readings     quota.Readings             `json:"readings"`
	Eligibility  map[quota.Bucket]string    `json:"eligibility"`
	Caps         map[quota.Bucket]float64   `json:"caps"`                // the switch threshold in force per bucket
	NextCaps     map[quota.Bucket]NextCap   `json:"next_caps,omitempty"` // where the cap changes next before the reset
	Reopens      map[quota.Bucket]time.Time `json:"reopens,omitempty"`   // when a held account takes work again
	LastAnchor   time.Time                  `json:"last_anchor,omitzero"`
	AnchorModel  string                     `json:"anchor_model,omitempty"`
}

// AccountMeter is what an Ultra account's own meter taught the balancer: the percent of the account's
// limit window that one million units of use cost (see history.BasisText).
type AccountMeter struct {
	Known        bool    `json:"known"` // false until a window with enough on the meter has been seen
	PerMillion   float64 `json:"per_million"`
	FromPrevious bool    `json:"from_previous"`          // the current window shows too little: the previous window's figure
	WindowHours  float64 `json:"window_hours,omitempty"` // the time between two resets, measured; 0 until two have been seen
	Outside      float64 `json:"outside,omitempty"`      // points of the meter now that rose with no use of the proxy near them: in no row
}

// MeterView says what the percent of the Sessions card means.
type MeterView struct {
	Label string `json:"label"` // the plan the percent is of, as the page names it
	Basis string `json:"basis"` // what one unit of use counts
	// FableModels are the models of the Fable limit. Their use draws on that limit as well as on the
	// meter the percent is of (the unified limit header counts Fable use toward it), so it is inside
	// the percent, and marked on the page.
	FableModels []string `json:"fable_models"`
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
	// Sessions is read from the history files, so the plugin adds it after Status returns: the
	// engine's lock is not held for a disk read.
	Sessions *history.Sessions `json:"sessions,omitempty"`
	// Affinity is where each recent session went and why, the project homes and the pins.
	Affinity *AffinityView `json:"affinity,omitempty"`
	// Meter is added with the history's sessions (see AddSessions).
	Meter *MeterView `json:"meter,omitempty"`
	// ExtraModels are the models outside CPA's catalog and the accounts each is pinned to.
	ExtraModels []ExtraModelView `json:"extra_models,omitempty"`
}

type ExtraModelView struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"display_name"`
	Accounts    []ExtraModelAccount `json:"accounts"` // in the order they are tried
}

type ExtraModelAccount struct {
	AuthFile string `json:"auth_file"`
	Name     string `json:"name"`
	// State is "ready", "held back" (CPA cools it down), "disabled" (switched off by hand) or
	// "not loaded" (CPA does not list it).
	State string `json:"state"`
}

// Config is the configuration the engine runs on; it never changes for one engine.
func (e *Engine) Config() config.Config { return e.cfg }

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
		av := AccountView{AuthFile: a.AuthFile, Name: names[a.AuthFile], Reserve: a.Reserve, ReserveSteps: a.ReserveSteps, SessionSteps: a.SessionSteps, ExtraUsage: a.ExtraUsage, Listed: listed, Unavailable: cred.unavailable, Disabled: cred.disabled,
			Plan: e.st.Account(a.AuthFile).Plan.Name, PlanLabel: probe.DisplayName(e.st.Account(a.AuthFile).Plan.Name), Weight: probe.WeightOf(e.st.Account(a.AuthFile).Plan.Name), Readings: snap.Readings,
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
	v.Affinity = e.affinityView(now)
	v.ExtraModels = e.extraModelViews()
	return v
}
