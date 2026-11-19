package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/engine"
)

const advisorBody = `{"model":"claude-opus-5-5","tools":[{"type":"advisor_20260301","name":"advisor","model":"claude-mythos-9-1"}]}`

func interceptBeforeVia(t *testing.T, body string, headers map[string][]string) pluginapi.RequestInterceptResponse {
	t.Helper()
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{Model: "claude-opus-5-5", Headers: headers, Body: []byte(body)})
	return envelope[pluginapi.RequestInterceptResponse](t, dispatch(pluginabi.MethodRequestInterceptBefore, raw))
}

// noEngine runs the rest of a test as if no configuration had ever been accepted.
func noEngine(t *testing.T) {
	mu.Lock()
	previous := current
	current = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		current = previous
		mu.Unlock()
	})
}

func TestThePluginDeclaresTheRequestInterceptor(t *testing.T) {
	req, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(extraConfig(t, ""))})
	got := envelope[struct {
		Capabilities map[string]bool `json:"capabilities"`
	}](t, configure(req))
	t.Cleanup(shutdown)
	if !got.Capabilities["request_interceptor"] {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
}

func TestTheInterceptorMarksARequestWhoseBodyCarriesTheAdvisor(t *testing.T) {
	configureYAML(t, extraConfig(t, extraYAML+"advisor_pin: {model: claude-mythos-9-1}\n"))
	t.Cleanup(shutdown)
	for _, body := range []string{advisorBody, `{"model":"claude-opus-5-5","betas":["advisor-tool-2026-03-01"]}`} {
		got := interceptBeforeVia(t, body, map[string][]string{"Content-Type": {"application/json"}})
		if len(got.Headers[config.AdvisorMarker]) != 1 || got.Terminate || len(got.Body) != 0 {
			t.Fatalf("%s: response = %+v, want the marker and nothing else", body, got)
		}
	}
	if got := interceptBeforeVia(t, `{"model":"claude-opus-5-5","tools":[{"type":"web_search_20250305"}]}`, nil); len(got.Headers) != 0 || len(got.ClearHeaders) != 0 {
		t.Fatalf("a request without the advisor is left as it is: %+v", got)
	}
	// the marker pins the request: the advisor's account takes it
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: "m",
		Options:    pluginapi.SchedulerOptions{Headers: map[string][]string{config.AdvisorMarker: {"1"}}},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a.json", Provider: "claude"}, {ID: "b.json", Provider: "claude"}}})
	if got := envelope[pluginapi.SchedulerPickResponse](t, dispatch(pluginabi.MethodSchedulerPick, raw)); got.AuthID != "b.json" {
		t.Fatalf("a marked request = %+v, want the advisor's account", got)
	}
}

func TestTheInterceptorLeavesRequestsAloneWithoutAnAdvisorPin(t *testing.T) {
	configureYAML(t, extraConfig(t, extraYAML))
	t.Cleanup(shutdown)
	if got := interceptBeforeVia(t, advisorBody, nil); len(got.Headers) != 0 {
		t.Fatalf("without advisor_pin nothing changes: %+v", got)
	}
}

func TestTheMarkerIsRemovedOnceTheAccountIsChosen(t *testing.T) {
	configureYAML(t, extraConfig(t, extraYAML+"advisor_pin: {model: claude-mythos-9-1}\n"))
	t.Cleanup(shutdown)
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{Model: "claude-opus-5-5", Headers: map[string][]string{config.AdvisorMarker: {"1"}}})
	got := envelope[pluginapi.RequestInterceptResponse](t, dispatch(pluginabi.MethodRequestInterceptAfter, raw))
	if len(got.ClearHeaders) != 1 || got.ClearHeaders[0] != config.AdvisorMarker || len(got.Headers) != 0 || got.Terminate {
		t.Fatalf("after the pick = %+v, want the marker cleared", got)
	}
}

func TestWithoutAnEngineARequestWithTheAdvisorIsRefused(t *testing.T) {
	noEngine(t)
	if got := interceptBeforeVia(t, advisorBody, nil); len(got.Headers[config.AdvisorMarker]) != 1 {
		t.Fatalf("with no configuration the default rule marks the request: %+v", got)
	}
	pick := func(headers map[string][]string) pluginapi.SchedulerPickResponse {
		raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: "m",
			Options:    pluginapi.SchedulerOptions{Headers: headers},
			Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a.json", Provider: "claude"}}})
		return envelope[pluginapi.SchedulerPickResponse](t, dispatch(pluginabi.MethodSchedulerPick, raw))
	}
	for _, h := range []map[string][]string{{"Anthropic-Beta": {"oauth-2025-04-20,advisor-tool-2026-03-01"}}, {config.AdvisorMarker: {"1"}}} {
		if got := pick(h); !got.Handled || !got.Reject || got.RejectCode != engine.ExtraModelUnavailable || got.DelegateBuiltin != "" {
			t.Fatalf("%v: pick = %+v, want a refusal", h, got)
		}
	}
	if got := pick(nil); got.Reject || got.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
		t.Fatalf("any other request goes to CPA's order: %+v", got)
	}
}
