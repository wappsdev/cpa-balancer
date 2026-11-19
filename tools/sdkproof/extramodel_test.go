//go:build sdkproof

// This test runs inside a copy of the CLIProxyAPI module (see run.sh), because it drives the
// host's own service, plugin host and auth manager, which are internal to that module. It proves
// what the balancer's extra models rely on:
//   - a model registrar with provider "claude" and no executor adds its models to every claude
//     auth's model list, next to the built-in ones, and the built-in claude executor serves them;
//   - every claude auth then supports the model, so only the scheduler can pin it;
//   - a scheduler Reject or error ends the request; a pick the scheduler does not handle falls
//     back to the built-in selector, which may choose any claude auth;
//   - the model stays listed while the plugin is loaded, whatever the pinned auth's state.
package cliproxy

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const proofModel = "claude-proof-extra-1"

type proofRegistrar struct{}

func (proofRegistrar) RegisterModels(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	return pluginapi.ModelRegistrationResponse{Provider: "claude", Models: []pluginapi.ModelInfo{{
		ID: proofModel, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: "Proof Extra",
		ContextLength: 1000000, MaxCompletionTokens: 128000,
		Thinking: &pluginapi.ThinkingSupport{DynamicAllowed: true, Levels: []string{"low", "high"}},
	}}}, nil
}

// proofScheduler pins proofModel to "auth-b" and answers as mode says.
type proofScheduler struct {
	mu     sync.Mutex
	mode   string // "pin", "error", "unhandled"
	models []string
}

func (s *proofScheduler) Pick(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = append(s.models, req.Model)
	switch s.mode {
	case "error":
		return pluginapi.SchedulerPickResponse{}, errorString("plugin failed")
	case "unhandled":
		return pluginapi.SchedulerPickResponse{}, nil
	}
	for _, c := range req.Candidates {
		if c.ID == "auth-b" {
			return pluginapi.SchedulerPickResponse{AuthID: "auth-b", Handled: true}, nil
		}
	}
	return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "extra_model_unavailable", RejectReason: "pinned auth not available"}, nil
}

type errorString string

func (e errorString) Error() string { return string(e) }

type proofExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (e *proofExecutor) Identifier() string { return "claude" }
func (e *proofExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
}
func (e *proofExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errorString("not used")
}
func (e *proofExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) { return a, nil }
func (e *proofExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errorString("not used")
}
func (e *proofExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errorString("not used")
}
func (e *proofExecutor) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.calls
	e.calls = nil
	return out
}

func TestProofExtraModelThroughRegistrarAndScheduler(t *testing.T) {
	ctx := context.Background()
	host := pluginhost.New()
	sched := &proofScheduler{mode: "pin"}
	host.RegisterPluginForTest("balancer", pluginapi.Plugin{
		Metadata:     pluginapi.Metadata{Name: "balancer", Version: "1.0.0"},
		Capabilities: pluginapi.Capabilities{ModelRegistrar: proofRegistrar{}, Scheduler: sched},
	})
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil), pluginHost: host}
	service.coreManager.SetPluginScheduler(host)
	for _, id := range []string{"auth-a", "auth-b"} {
		auth := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
		if _, err := service.coreManager.Register(ctx, auth); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient("plugin:balancer:claude") })
	service.syncPluginModelRuntime(ctx)
	exec := &proofExecutor{}
	service.coreManager.RegisterExecutor(exec) // the sync registered the real claude executor

	reg := registry.GetGlobalRegistry()
	if got := reg.GetModelProviders(proofModel); !slices.Equal(got, []string{"claude"}) {
		t.Fatalf("providers of the extra model = %v, want [claude]", got)
	}
	for _, id := range []string{"auth-a", "auth-b"} {
		if !reg.ClientSupportsModel(id, proofModel) {
			t.Fatalf("%s does not support the extra model: the registrar did not add it to the built-in provider", id)
		}
		if !reg.ClientSupportsModel(id, "claude-opus-5-5") {
			t.Fatalf("%s lost its built-in claude models", id)
		}
	}
	if info := registry.LookupModelInfo(proofModel, "claude"); info == nil || info.Thinking == nil || !info.Thinking.DynamicAllowed || info.ContextLength != 1000000 {
		t.Fatalf("model info = %+v, want the registrar's capabilities", info)
	}

	run := func(model string) error {
		_, err := service.coreManager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		return err
	}
	for i := 0; i < 4; i++ {
		if err := run(proofModel); err != nil {
			t.Fatalf("execute: %v", err)
		}
	}
	if got := exec.take(); !slices.Equal(got, []string{"auth-b", "auth-b", "auth-b", "auth-b"}) {
		t.Fatalf("served by %v, want only auth-b", got)
	}
	if err := run(proofModel + "(high)"); err != nil {
		t.Fatalf("execute with a thinking suffix: %v", err)
	}
	exec.take()
	t.Logf("models the scheduler saw: %q", sched.models)

	// The pinned auth disabled: the scheduler rejects, nothing is served, the model stays listed.
	b, _ := service.coreManager.GetByID("auth-b")
	b.Disabled, b.Status = true, coreauth.StatusDisabled
	if _, err := service.coreManager.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	service.registerModelsForAuth(ctx, b)
	err := run(proofModel)
	if err == nil || !strings.Contains(err.Error(), "pinned auth not available") {
		t.Fatalf("with the pinned auth disabled: err = %v, want the scheduler's reject", err)
	}
	if got := exec.take(); len(got) != 0 {
		t.Fatalf("with the pinned auth disabled the request reached %v", got)
	}
	listed := false
	for _, m := range reg.GetAvailableModels("openai") {
		listed = listed || m["id"] == proofModel
	}
	t.Logf("listed with the pinned auth disabled: %v", listed)

	// A scheduler error ends the request too.
	sched.mode = "error"
	if err := run(proofModel); err == nil {
		t.Fatal("a scheduler error did not end the request")
	}
	if got := exec.take(); len(got) != 0 {
		t.Fatalf("after a scheduler error the request reached %v", got)
	}

	// A pick the scheduler does not handle goes to the built-in selector: any claude auth.
	sched.mode = "unhandled"
	if err := run(proofModel); err != nil {
		t.Fatalf("unhandled pick: %v", err)
	}
	t.Logf("an unhandled pick was served by %v", exec.take())
}
