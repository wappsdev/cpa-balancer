//go:build sdkproof

// This test runs inside a copy of the CLIProxyAPI module (see run.sh), through CPA's real request
// path: the API handler, the plugin host's request interceptors and scheduler, and the auth
// manager. It proves what the advisor pin relies on when the advisor is only in the body (CPA adds
// its beta after the pick):
//   - a header a before-auth request interceptor adds reaches the scheduler's pick;
//   - a scheduler reject ends the request with HTTP 500, and its reason is the error's message.
//
// An after-auth interceptor cannot remove that header again: the plugin host drops ClearHeaders on
// that path. The claude executor forwards only its own list of client headers, which the marker is
// not on (advisor_body_test.go proves it), so it never reaches the upstream.
//
// The interceptor and the scheduler here stand in for the plugin's: they follow the same rule.
package handlers_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	marker      = "X-Cpa-Balancer-Advisor"
	advisorText = "All accounts approved for the claude-proof-advisor advisor are unavailable; switch the advisor model or wait for a reset"
)

// markInterceptor marks a body with an advisor_ tool before the pick.
type markInterceptor struct{}

func (markInterceptor) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if strings.Contains(string(req.Body), `"type":"advisor_`) {
		return pluginapi.RequestInterceptResponse{Headers: http.Header{marker: {"1"}}}, nil
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

func (markInterceptor) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return pluginapi.RequestInterceptResponse{}, nil
}

// pinScheduler pins a marked request to auth-b (or refuses it while out is set); any other to auth-a.
type pinScheduler struct {
	mu     sync.Mutex
	out    bool
	marked []bool
}

func (s *pinScheduler) Pick(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	isMarked := http.Header(req.Options.Headers).Get(marker) != ""
	s.marked = append(s.marked, isMarked)
	if !isMarked {
		return pluginapi.SchedulerPickResponse{AuthID: "auth-a", Handled: true}, nil
	}
	if s.out {
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "extra_model_unavailable", RejectReason: advisorText}, nil
	}
	return pluginapi.SchedulerPickResponse{AuthID: "auth-b", Handled: true}, nil
}

// headerExecutor records the auth and the headers each call reached the executor with.
type headerExecutor struct {
	mu      sync.Mutex
	auths   []string
	markers []string
}

func (e *headerExecutor) record(auth *coreauth.Auth, opts cliproxyexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = append(e.auths, auth.ID)
	e.markers = append(e.markers, opts.Headers.Get(marker))
}
func (e *headerExecutor) Identifier() string { return "claude" }
func (e *headerExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth, opts)
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
}
func (e *headerExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Message: "not used"}
}
func (e *headerExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *headerExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &coreauth.Error{Message: "not used"}
}
func (e *headerExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Message: "not used"}
}
func (e *headerExecutor) take() (auths, markers []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	auths, markers, e.auths, e.markers = e.auths, e.markers, nil, nil
	return auths, markers
}

func TestProofAnAdvisorOnlyInTheBodyIsPinnedThroughTheHandler(t *testing.T) {
	const model = "claude-proof-handler-1"
	ctx := context.Background()
	host := pluginhost.New()
	sched := &pinScheduler{}
	host.RegisterPluginForTest("balancer", pluginapi.Plugin{
		Metadata:     pluginapi.Metadata{Name: "balancer", Version: "1.0.0"},
		Capabilities: pluginapi.Capabilities{RequestInterceptor: markInterceptor{}, Scheduler: sched},
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetPluginScheduler(host)
	exec := &headerExecutor{}
	manager.RegisterExecutor(exec)
	for _, id := range []string{"auth-a", "auth-b"} {
		if _, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler.SetPluginHost(host)
	run := func(body string) error {
		_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "claude", model, []byte(body), "")
		if errMsg != nil {
			if errMsg.StatusCode != http.StatusInternalServerError {
				t.Fatalf("a scheduler reject answered HTTP %d, want 500 (the status a scheduler cannot set)", errMsg.StatusCode)
			}
			return errMsg.Error
		}
		return nil
	}

	advisorBody := `{"model":"` + model + `","tools":[{"type":"advisor_20260301","name":"advisor","model":"claude-proof-advisor"}],"messages":[]}`
	for i := 0; i < 2; i++ {
		if err := run(advisorBody); err != nil {
			t.Fatalf("a body with the advisor: %v", err)
		}
	}
	auths, _ := exec.take()
	if !slices.Equal(auths, []string{"auth-b", "auth-b"}) {
		t.Fatalf("a body with the advisor and no header was served by %v, want only auth-b", auths)
	}
	if !slices.Equal(sched.marked, []bool{true, true}) {
		t.Fatalf("the scheduler saw the marker %v, want it on every pick", sched.marked)
	}

	if err := run(`{"model":"` + model + `","messages":[]}`); err != nil {
		t.Fatalf("a body without the advisor: %v", err)
	}
	if auths, _ := exec.take(); !slices.Equal(auths, []string{"auth-a"}) {
		t.Fatalf("a body without the advisor was served by %v, want auth-a", auths)
	}

	sched.out = true
	err := run(advisorBody)
	if err == nil || !strings.Contains(err.Error(), advisorText) {
		t.Fatalf("with no pinned account the request ends with %v, want the scheduler's reason", err)
	}
	if auths, _ := exec.take(); len(auths) != 0 {
		t.Fatalf("a refused request reached %v", auths)
	}
	t.Logf("refusal: %v", err)
}
