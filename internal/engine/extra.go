package engine

import (
	"slices"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// ExtraModelUnavailable is the reject code of a request for an extra model that none of its
// accounts can take. It is not CPA's "auth_unavailable", which CPA retries.
const ExtraModelUnavailable = "extra_model_unavailable"

// pickExtra sends a request for an extra model to the first of its accounts among the candidates,
// or refuses it. CPA registers the model for every claude account, so this is the only pin: it
// holds while paused and in shadow mode, and over session, folder and CPA's own pins. The session
// is not placed, because the model's accounts are not the balancer's choice.
func pickExtra(m config.ExtraModel, req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
	candidates := map[string]bool{}
	for _, c := range req.Candidates {
		candidates[c.ID] = true
	}
	allowed := m.Accounts
	if pinned, _ := req.Options.Metadata["pinned_auth_id"].(string); pinned != "" {
		allowed = nil
		if slices.Contains(m.Accounts, pinned) {
			allowed = []string{pinned}
		}
	}
	for _, a := range allowed {
		if candidates[a] {
			return pluginapi.SchedulerPickResponse{AuthID: a, Handled: true}
		}
	}
	return refuseExtra(m.ID)
}

// ModelInfos is what the plugin registers with CPA for the extra models. A model with thinking
// levels thinks adaptively: no fixed budget, and it cannot be turned off.
func ModelInfos(models []config.ExtraModel) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		info := pluginapi.ModelInfo{ID: m.ID, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: m.DisplayName,
			ContextLength: m.ContextLength, InputTokenLimit: m.ContextLength, MaxCompletionTokens: m.MaxCompletionTokens, OutputTokenLimit: m.MaxCompletionTokens}
		if info.DisplayName == "" {
			info.DisplayName = m.ID
		}
		if len(m.ThinkingLevels) > 0 {
			info.Thinking = &pluginapi.ThinkingSupport{DynamicAllowed: true, Levels: slices.Clone(m.ThinkingLevels)}
		}
		out = append(out, info)
	}
	return out
}

// extraModelViews says, for the status page, where each extra model can go now; callers hold e.mu.
func (e *Engine) extraModelViews() []ExtraModelView {
	if len(e.cfg.ExtraModels) == 0 {
		return nil
	}
	names := e.cfg.Names()
	out := make([]ExtraModelView, 0, len(e.cfg.ExtraModels))
	for _, m := range e.cfg.ExtraModels {
		v := ExtraModelView{ID: m.ID, DisplayName: m.DisplayName}
		if v.DisplayName == "" {
			v.DisplayName = m.ID
		}
		for _, a := range m.Accounts {
			v.Accounts = append(v.Accounts, ExtraModelAccount{AuthFile: a, Name: names[a], State: e.credentialState(a)})
		}
		out = append(out, v)
	}
	return out
}

// credentialState is how CPA holds an account: ready, not loaded, disabled or held back (cooled
// down); callers hold e.mu.
func (e *Engine) credentialState(account string) string {
	cred, listed := e.credentials[account]
	switch {
	case !listed:
		return "not loaded"
	case cred.disabled:
		return "disabled"
	case cred.unavailable:
		return "held back"
	}
	return "ready"
}

// advisorPinView says, for the status page, where a request with the advisor can go now. An
// advisor request is a main-model request, so an account whose all-models week is at its cap is
// "full": CPA still offers it, but it answers with a limit. Callers hold e.mu.
func (e *Engine) advisorPinView(now time.Time) *AdvisorPinView {
	p := e.cfg.AdvisorPin
	if p == nil {
		return nil
	}
	m, _ := e.cfg.ExtraModel(p.Model)
	v := &AdvisorPinView{Model: m.ID, DisplayName: m.DisplayName, BetaPrefix: p.BetaPrefix, Accounts: []ExtraModelAccount{}, AllOut: true}
	if v.DisplayName == "" {
		v.DisplayName = m.ID
	}
	names := e.cfg.Names()
	for _, a := range m.Accounts {
		state := e.credentialState(a)
		if state == "ready" && e.standing(a, quota.Main, now) == policy.Full {
			state = "full"
		}
		v.AllOut = v.AllOut && state != "ready"
		v.Accounts = append(v.Accounts, ExtraModelAccount{AuthFile: a, Name: names[a], State: state})
	}
	return v
}

// pinnedPick is the answer of a request pinned to an extra model's accounts, whatever else holds: a
// request for the model itself, else one that carries the advisor tool of the advisor pin. ok is
// false for any other request.
func pinnedPick(cfg config.Config, req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, ok bool) {
	if m, ok := cfg.ExtraModel(req.Model); ok {
		return pickExtra(m, req), true
	}
	if m, ok := cfg.AdvisorModel(req.Options.Headers); ok {
		if resp = pickExtra(m, req); resp.Reject {
			resp = refuseAdvisor(m.ID)
		}
		return resp, true
	}
	return pluginapi.SchedulerPickResponse{}, false
}

// RefuseExtra is the answer for a pick the plugin could not make (a panic): a request pinned to an
// extra model's accounts (the model's own, or the advisor's) is refused, anything else is left to
// the caller.
func RefuseExtra(cfg config.Config, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool) {
	if m, ok := cfg.ExtraModel(req.Model); ok {
		return refuseExtra(m.ID), true
	}
	if m, ok := cfg.AdvisorModel(req.Options.Headers); ok {
		return refuseAdvisor(m.ID), true
	}
	return pluginapi.SchedulerPickResponse{}, false
}

// refuseAdvisor ends a request that carries the advisor tool: its model may only be used through
// its own accounts.
func refuseAdvisor(model string) pluginapi.SchedulerPickResponse {
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: ExtraModelUnavailable,
		RejectReason: "a request with the " + model + " advisor is served only by that model's accounts, and none of them is available now"}
}

// refuseExtra ends the request: CPA must not hand it to another account.
func refuseExtra(model string) pluginapi.SchedulerPickResponse {
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: ExtraModelUnavailable,
		RejectReason: model + " is served only by its own accounts, and none of them is available now"}
}
