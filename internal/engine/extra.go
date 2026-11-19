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
func (e *Engine) extraModelViews(now time.Time) []ExtraModelView {
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
			v.Accounts = append(v.Accounts, ExtraModelAccount{AuthFile: a, Name: names[a], State: e.pinState(a, quota.Main, now)})
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

// pinState is how a pinned request sees an account now: ready, reserve (held back by its reserve
// only, so it serves when no account is ready), full (at its weekly cap), held back (a 5-hour cap or
// a cooldown), disabled or not loaded. Callers hold e.mu.
func (e *Engine) pinState(account string, b quota.Bucket, now time.Time) string {
	if state := e.credentialState(account); state != "ready" {
		return state
	}
	switch e.pinTier(account, b, now) {
	case 1, 3:
		return "ready"
	case 2:
		return "reserve"
	}
	if e.standing(account, b, now) == policy.Full {
		return "full"
	}
	return "held back"
}

// advisorPinView says, for the status page, where a request with the advisor can go now. AllOut:
// none of its accounts can take one, so it is refused. Callers hold e.mu.
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
		state := e.pinState(a, quota.Main, now)
		v.AllOut = v.AllOut && state != "ready" && state != "reserve"
		v.Accounts = append(v.Accounts, ExtraModelAccount{AuthFile: a, Name: names[a], State: state})
	}
	return v
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
// its own accounts. CPA answers a scheduler refusal with HTTP 500 and this reason as the message.
func refuseAdvisor(model string) pluginapi.SchedulerPickResponse {
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: ExtraModelUnavailable,
		RejectReason: "All accounts approved for the " + model + " advisor are unavailable; switch the advisor model or wait for a reset"}
}

// RefuseUnconfigured is the answer, with no configuration accepted yet, for a request that carries
// the advisor by the default rule: the accounts it may use are not known, so it is refused rather
// than handed to CPA's order. ok is false for any other request.
func RefuseUnconfigured(req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool) {
	if !config.AdvisorRequested(req.Options.Headers, config.DefaultAdvisorBeta) {
		return pluginapi.SchedulerPickResponse{}, false
	}
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: ExtraModelUnavailable,
		RejectReason: "The balancer has no valid configuration, so a request with the advisor is refused; fix the plugin's configuration"}, true
}

// refuseExtra ends the request: CPA must not hand it to another account.
func refuseExtra(model string) pluginapi.SchedulerPickResponse {
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: ExtraModelUnavailable,
		RejectReason: model + " is served only by its own accounts, and none of them is available now"}
}
