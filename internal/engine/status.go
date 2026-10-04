package engine

import (
	"time"

	"github.com/adnankurt/cpa-balancer/internal/policy"
	"github.com/adnankurt/cpa-balancer/internal/quota"
	"github.com/adnankurt/cpa-balancer/internal/state"
)

type AccountView struct {
	AuthFile    string                   `json:"auth_file"`
	Reserve     bool                     `json:"reserve"`
	Listed      bool                     `json:"listed"`
	Readings    quota.Readings           `json:"readings"`
	Eligibility map[quota.Bucket]string  `json:"eligibility"`
	Caps        map[quota.Bucket]float64 `json:"caps"` // the switch threshold in force per bucket
	LastAnchor  time.Time                `json:"last_anchor,omitzero"`
	AnchorModel string                   `json:"anchor_model,omitempty"`
}

type View struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Mode        string          `json:"mode"`
	Paused      bool            `json:"paused"`
	ConfigError string          `json:"config_error,omitempty"` // set by the plugin when the last config was rejected
	FinalCap    float64         `json:"final_cap"` // every account's cap once reserves are released
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
	for _, a := range e.cfg.Accounts {
		snap := e.snaps[a.AuthFile]
		_, listed := e.credentials[a.AuthFile]
		av := AccountView{AuthFile: a.AuthFile, Reserve: a.Reserve, Listed: listed, Readings: snap.Readings,
			Eligibility: map[quota.Bucket]string{}, Caps: map[quota.Bucket]float64{quota.Session: e.cfg.Thresholds.SessionCap}}
		for _, b := range []quota.Bucket{quota.Main, quota.Fable} {
			_, why := policy.Eligibility(a, snap, true, b, now, e.cfg.Thresholds, false)
			av.Eligibility[b] = why
			if r, ok := snap.Readings[b]; ok && r.ResetsAt != nil {
				av.Caps[b] = policy.Cap(a.Reserve, *r.ResetsAt, now, e.cfg.Thresholds)
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
