package engine

import (
	"time"

	"github.com/adnankurt/cpa-balancer/internal/policy"
	"github.com/adnankurt/cpa-balancer/internal/quota"
	"github.com/adnankurt/cpa-balancer/internal/state"
)

type AccountView struct {
	AuthFile    string                  `json:"auth_file"`
	Reserve     bool                    `json:"reserve"`
	Listed      bool                    `json:"listed"`
	Readings    quota.Readings          `json:"readings"`
	Eligibility map[quota.Bucket]string `json:"eligibility"`
	LastAnchor  time.Time               `json:"last_anchor,omitzero"`
	AnchorModel string                  `json:"anchor_model,omitempty"`
}

type View struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Mode        string          `json:"mode"`
	Paused      bool            `json:"paused"`
	ConfigError string          `json:"config_error,omitempty"` // set by the plugin when the last config was rejected
	Decision    policy.Decision `json:"decision"`
	Accounts    []AccountView   `json:"accounts"`
	Changes     []state.Change  `json:"changes"`
}

// Status is what the status page shows; it never contains credentials.
func (e *Engine) Status() View {
	now := e.now()
	e.mu.RLock()
	defer e.mu.RUnlock()
	v := View{GeneratedAt: now, Mode: e.cfg.Mode, Paused: e.cfg.Paused, Decision: e.decision, Changes: append([]state.Change(nil), e.st.Changes...)}
	for _, a := range e.cfg.Accounts {
		snap := e.snaps[a.AuthFile]
		_, listed := e.credentials[a.AuthFile]
		av := AccountView{AuthFile: a.AuthFile, Reserve: a.Reserve, Listed: listed, Readings: snap.Readings,
			Eligibility: map[quota.Bucket]string{}}
		for _, b := range []quota.Bucket{quota.Main, quota.Fable} {
			_, why := policy.Eligibility(a, snap, true, b, now, e.cfg.Thresholds, false)
			av.Eligibility[b] = why
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
