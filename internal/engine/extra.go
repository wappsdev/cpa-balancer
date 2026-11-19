package engine

import (
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
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

// RefuseExtra is the answer for a pick the plugin could not make (a panic): an extra model is
// refused, anything else is left to the caller.
func RefuseExtra(cfg config.Config, model string) (pluginapi.SchedulerPickResponse, bool) {
	m, ok := cfg.ExtraModel(model)
	if !ok {
		return pluginapi.SchedulerPickResponse{}, false
	}
	return refuseExtra(m.ID), true
}

// refuseExtra ends the request: CPA must not hand it to another account.
func refuseExtra(model string) pluginapi.SchedulerPickResponse {
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: ExtraModelUnavailable,
		RejectReason: model + " is served only by its own accounts, and none of them is available now"}
}
