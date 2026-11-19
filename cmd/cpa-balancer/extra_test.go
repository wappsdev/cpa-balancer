package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/engine"
)

const extraYAML = `extra_models:
  - id: claude-extra-1
    display_name: Claude Extra 1
    accounts: [b.json]
    context_length: 1000000
    max_completion_tokens: 128000
    thinking_levels: [low, high]
`

func extraConfig(t *testing.T, extra string) string {
	return fmt.Sprintf("state_path: %s/state.json\naccounts: [{auth_file: a.json}, {auth_file: b.json}]\nbuckets: {main: [m]}\n%s", t.TempDir(), extra)
}

func envelope[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("envelope %s: %v", raw, err)
	}
	var out T
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegistrationDeclaresTheModelRegistrar(t *testing.T) {
	req, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(extraConfig(t, ""))})
	got := envelope[struct {
		Capabilities map[string]bool `json:"capabilities"`
	}](t, configure(req))
	t.Cleanup(shutdown)
	if !got.Capabilities["model_registrar"] || !got.Capabilities["scheduler"] {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
}

func TestTheRegistrarListsTheExtraModelsForClaude(t *testing.T) {
	configureYAML(t, extraConfig(t, extraYAML))
	t.Cleanup(shutdown)
	got := envelope[pluginapi.ModelRegistrationResponse](t, dispatch(pluginabi.MethodModelRegister, []byte(`{}`)))
	if got.Provider != "claude" || len(got.Models) != 1 {
		t.Fatalf("registration = %+v", got)
	}
	m := got.Models[0]
	if m.ID != "claude-extra-1" || m.DisplayName != "Claude Extra 1" || m.Object != "model" || m.OwnedBy != "anthropic" || m.Type != "claude" ||
		m.ContextLength != 1000000 || m.MaxCompletionTokens != 128000 || m.InputTokenLimit != 1000000 || m.OutputTokenLimit != 128000 {
		t.Fatalf("model = %+v", m)
	}
	if m.Thinking == nil || !m.Thinking.DynamicAllowed || m.Thinking.ZeroAllowed || !slices.Equal(m.Thinking.Levels, []string{"low", "high"}) {
		t.Fatalf("thinking = %+v", m.Thinking)
	}
}

func TestTheRegistrarListsNothingWithoutExtraModels(t *testing.T) {
	configureYAML(t, extraConfig(t, ""))
	t.Cleanup(shutdown)
	got := envelope[pluginapi.ModelRegistrationResponse](t, dispatch(pluginabi.MethodModelRegister, []byte(`{}`)))
	if len(got.Models) != 0 {
		t.Fatalf("registration = %+v", got)
	}
}

func TestAModelWithoutThinkingLevelsHasNoThinking(t *testing.T) {
	configureYAML(t, extraConfig(t, "extra_models: [{id: claude-extra-2, accounts: [b.json], context_length: 200000, max_completion_tokens: 8192}]\n"))
	t.Cleanup(shutdown)
	got := envelope[pluginapi.ModelRegistrationResponse](t, dispatch(pluginabi.MethodModelRegister, []byte(`{}`)))
	if len(got.Models) != 1 || got.Models[0].Thinking != nil || got.Models[0].DisplayName != "claude-extra-2" {
		t.Fatalf("registration = %+v", got)
	}
}

func TestAFailedPickRefusesAnExtraModelAndDelegatesTheRest(t *testing.T) {
	configureYAML(t, extraConfig(t, extraYAML))
	t.Cleanup(shutdown)
	pickReq := func(model string) []byte {
		raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: model,
			Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a.json", Provider: "claude"}, {ID: "b.json", Provider: "claude"}}})
		return raw
	}
	got := envelope[pluginapi.SchedulerPickResponse](t, failedPick(pickReq("claude-extra-1(high)")))
	if !got.Handled || !got.Reject || got.RejectCode != engine.ExtraModelUnavailable || got.AuthID != "" || got.DelegateBuiltin != "" {
		t.Fatalf("a failed pick of an extra model = %+v, want a refusal", got)
	}
	got = envelope[pluginapi.SchedulerPickResponse](t, failedPick(pickReq("m")))
	if !got.Handled || got.Reject || got.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("a failed pick of another model = %+v, want CPA's own order", got)
	}
	got = envelope[pluginapi.SchedulerPickResponse](t, failedPick([]byte("not json")))
	if !got.Handled || got.Reject || got.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("an unreadable pick = %+v, want CPA's own order", got)
	}
}
