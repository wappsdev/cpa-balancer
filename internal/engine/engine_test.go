package engine

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/config"
	"github.com/adnankurt/cpa-balancer/internal/history"
	"github.com/adnankurt/cpa-balancer/internal/quota"
	"github.com/adnankurt/cpa-balancer/internal/state"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

const (
	W     = "claude-w.json" // no reserve
	G     = "claude-g.json" // keeps a reserve
	Opus  = "claude-opus-5-5"
	Fable = "claude-fable-5-1"
)

type fakeHost struct {
	files     []pluginapi.HostAuthFileEntry
	auth      map[string][]byte // auth_index -> json
	probeBody map[string]string // access token -> usage JSON
	httpCalls []string
	execs     []pluginapi.HostModelExecutionRequest
	execResp  pluginapi.HostModelExecutionResponse
	logs      []string
}

func newHost() *fakeHost {
	return &fakeHost{
		files: []pluginapi.HostAuthFileEntry{
			{ID: W, Name: W, AuthIndex: "idx-w", Provider: "claude"},
			{ID: G, Name: G, AuthIndex: "idx-g", Provider: "claude"},
			{ID: "codex-x.json", AuthIndex: "idx-x", Provider: "codex"},
		},
		auth:      map[string][]byte{"idx-w": []byte(`{"access_token":"tok-w-secret"}`), "idx-g": []byte(`{"access_token":"tok-g-secret"}`)},
		probeBody: map[string]string{},
	}
}

func (h *fakeHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) { return h.files, nil }
func (h *fakeHost) AuthGet(index string) ([]byte, error) {
	if raw, ok := h.auth[index]; ok {
		return raw, nil
	}
	return nil, errors.New("unknown index")
}
func (h *fakeHost) HTTPDo(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	token := strings.TrimPrefix(req.Headers.Get("Authorization"), "Bearer ")
	h.httpCalls = append(h.httpCalls, token)
	return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(h.probeBody[token])}, nil
}
func (h *fakeHost) ModelExecute(req pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
	h.execs = append(h.execs, req)
	return h.execResp, nil
}
func (h *fakeHost) Log(level, message string) { h.logs = append(h.logs, level+" "+message) }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testConfig(t *testing.T) config.Config {
	c := config.Default()
	c.StatePath = filepath.Join(t.TempDir(), "state.json")
	c.Accounts = []config.Account{{AuthFile: W}, {AuthFile: G, Reserve: true}}
	c.Buckets.Main = []string{Opus}
	c.Buckets.Fable = []string{Fable}
	return c
}

func headers(util7d, reset7dH, util5h, reset5hH float64, now time.Time) http.Header {
	unix := func(h float64) string {
		return strconv.FormatInt(now.Add(time.Duration(h*float64(time.Hour))).Unix(), 10)
	}
	return http.Header{
		"Anthropic-Ratelimit-Unified-7d-Utilization": {strconv.FormatFloat(util7d/100, 'f', 4, 64)},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {unix(reset7dH)},
		"Anthropic-Ratelimit-Unified-5h-Utilization": {strconv.FormatFloat(util5h/100, 'f', 4, 64)},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {unix(reset5hH)},
	}
}

func usage(account, model string, h http.Header) pluginapi.UsageRecord {
	return pluginapi.UsageRecord{Provider: "claude", AuthID: account, Model: model, ResponseHeaders: h}
}

func pick(e *Engine, model string, ids ...string) pluginapi.SchedulerPickResponse {
	req := pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: model}
	for _, id := range ids {
		req.Candidates = append(req.Candidates, pluginapi.SchedulerAuthCandidate{ID: id, Provider: "claude"})
	}
	return e.Pick(req)
}

// warm feeds main and Fable usage for both accounts: G resets first, so G is chosen.
func warm(e *Engine, now time.Time, gSession float64) {
	e.OnUsage(usage(W, Opus, headers(10, 140, 0, 4, now)))
	e.OnUsage(usage(W, Fable, headers(1, 140, 0, 4, now)))
	e.OnUsage(usage(G, Opus, headers(52, 90, gSession, 3, now)))
	e.OnUsage(usage(G, Fable, headers(0, 90, gSession, 3, now)))
}

func TestPickFollowsDecisionAndCandidates(t *testing.T) {
	c := &clock{t0}
	e := New(testConfig(t), newHost(), c.now, nil)
	if got := pick(e, Opus, W, G); got != delegate {
		t.Fatalf("without data the pick must be delegated, got %+v", got)
	}
	warm(e, t0, 30)
	if got := pick(e, Opus, W, G); got.AuthID != G || !got.Handled {
		t.Fatalf("got %+v", got)
	}
	if got := pick(e, Opus, W); got.AuthID != W {
		t.Fatalf("non-candidate must be skipped, got %+v", got)
	}
	if got := pick(e, Fable, W, G); got.AuthID != G {
		t.Fatalf("fable pick %+v", got)
	}
	if got := pick(e, "claude-haiku-4-5-20251001", W, G); got != delegate {
		t.Fatalf("model outside buckets must be delegated, got %+v", got)
	}
	codex := e.Pick(pluginapi.SchedulerPickRequest{Providers: []string{"codex"}, Model: Opus,
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "codex-x.json"}}})
	if codex != delegate {
		t.Fatalf("non-Claude must be delegated, got %+v", codex)
	}
}

func TestSessionCapSwitchesTheNextRequest(t *testing.T) {
	c := &clock{t0}
	e := New(testConfig(t), newHost(), c.now, nil)
	warm(e, t0, 30)
	e.OnUsage(usage(G, Opus, headers(52, 90, 98, 3, t0)))
	if got := pick(e, Opus, W, G); got.AuthID != W {
		t.Fatalf("got %+v", got)
	}
}

func TestPinnedRequestIsNotRerouted(t *testing.T) {
	e := New(testConfig(t), newHost(), (&clock{t0}).now, nil)
	warm(e, t0, 30)
	req := pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: "claude-haiku-4-5-20251001",
		Options:    pluginapi.SchedulerOptions{Metadata: map[string]any{"pinned_auth_id": W}},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: W}}}
	if got := e.Pick(req); got.AuthID != W {
		t.Fatalf("got %+v", got)
	}
}

func TestShadowModeAlwaysDelegates(t *testing.T) {
	cfg := testConfig(t)
	cfg.Mode = "shadow"
	e := New(cfg, newHost(), (&clock{t0}).now, nil)
	warm(e, t0, 30)
	if got := pick(e, Opus, W, G); got != delegate {
		t.Fatalf("got %+v", got)
	}
}

const idleProbe = `{"limits":[{"kind":"session","percent":0,"resets_at":null},
 {"kind":"weekly_all","percent":10,"resets_at":"2026-10-09T00:00:00+00:00"},
 {"kind":"weekly_scoped","percent":1,"resets_at":"2026-10-09T00:00:00+00:00","scope":{"model":{"display_name":"Fable"}}}]}`

func TestTickProbesIdleAccountAndAnchorsIt(t *testing.T) {
	c := &clock{t0}
	h := newHost()
	h.probeBody["tok-w-secret"] = idleProbe
	reset := t0.Add(5 * time.Hour)
	h.execResp = pluginapi.HostModelExecutionResponse{StatusCode: 200, Headers: http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.0"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {strconv.FormatInt(reset.Unix(), 10)},
	}}
	cfg := testConfig(t)
	e := New(cfg, h, c.now, nil)
	e.OnUsage(usage(G, Opus, headers(52, 90, 30, 3, t0)))
	e.OnUsage(usage(G, Fable, headers(0, 90, 30, 3, t0)))
	e.Tick()
	if strings.Join(h.httpCalls, ",") != "tok-w-secret" {
		t.Fatalf("expected one probe of W, got %v", h.httpCalls)
	}
	if len(h.execs) != 1 || h.execs[0].AuthID != W || h.execs[0].Model != cfg.Anchor.Model {
		t.Fatalf("expected one Haiku anchor of W, got %+v", h.execs)
	}
	saved, err := state.Load(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	w := saved.Accounts[W]
	if w == nil || !w.LastAnchor.Equal(t0) || w.AnchorReading == nil || !w.AnchorReading.ResetsAt.Equal(reset) {
		t.Fatalf("anchor not persisted: %+v", w)
	}
	if saved.Active[quota.Main].Account != G {
		t.Fatalf("active not persisted: %+v", saved.Active)
	}
	// 11 minutes later the anchored window runs: neither a new probe nor a new anchor.
	c.t = t0.Add(11 * time.Minute)
	e.OnUsage(usage(G, Opus, headers(53, 90, 31, 3, c.t)))
	e.Tick()
	if len(h.execs) != 1 || len(h.httpCalls) != 1 {
		t.Fatalf("unexpected follow-up calls: execs=%d probes=%d", len(h.execs), len(h.httpCalls))
	}
}

func TestClosedWindowAfterHaikuAnchorSwitchesToMainModel(t *testing.T) {
	c := &clock{t0}
	cfg := testConfig(t)
	st := state.New()
	reset := t0.Add(3 * time.Hour)
	st.Account(W).LastAnchor = t0.Add(-2 * time.Hour)
	st.Account(W).AnchorReading = &quota.Reading{ResetsAt: &reset, ObservedAt: t0.Add(-2 * time.Hour), Source: "anchor"}
	if err := st.Save(cfg.StatePath); err != nil {
		t.Fatal(err)
	}
	h := newHost()
	h.probeBody["tok-w-secret"] = idleProbe
	e := New(cfg, h, c.now, nil)
	e.Tick()
	saved, _ := state.Load(cfg.StatePath)
	if !saved.Accounts[W].AnchorMainModel {
		t.Fatalf("expected the main-model fallback, logs: %v", h.logs)
	}
}

func TestStatusHasNoCredentials(t *testing.T) {
	h := newHost()
	e := New(testConfig(t), h, (&clock{t0}).now, nil)
	warm(e, t0, 30)
	e.Tick()
	raw, err := json.Marshal(e.Status())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || !strings.Contains(string(raw), G) {
		t.Fatalf("status JSON: %s", raw)
	}
}

func TestStatusReportsTheCapInForcePerBucket(t *testing.T) {
	e := New(testConfig(t), newHost(), (&clock{t0}).now, nil)
	warm(e, t0, 30)
	caps := map[string]map[quota.Bucket]float64{}
	for _, a := range e.Status().Accounts {
		caps[a.AuthFile] = a.Caps
	}
	// G keeps a reserve and resets in 90 h (far cap); W has none (final cap); 5-hour uses session_cap.
	if caps[G][quota.Main] != 85 || caps[G][quota.Session] != 98 || caps[W][quota.Main] != 99 || caps[W][quota.Fable] != 99 {
		t.Fatalf("caps %+v", caps)
	}
}

func TestStatusReportsTheFinalCap(t *testing.T) {
	// A reserve account's cap rises to final_cap near its reset; "when does work stop" needs it.
	if v := New(testConfig(t), newHost(), (&clock{t0}).now, nil).Status(); v.FinalCap != 99 {
		t.Fatalf("final cap %v", v.FinalCap)
	}
}

func TestSwitchMadeByUsageIsProbedOnTheNextTick(t *testing.T) {
	c := &clock{t0}
	h := newHost()
	h.probeBody["tok-w-secret"] = idleProbe
	e := New(testConfig(t), h, c.now, nil)
	e.Tick() // credentials; no probe: nothing configured has a running anchor or stale data yet
	h.httpCalls = nil
	warm(e, t0, 30)
	c.t = t0.Add(15 * time.Minute)
	e.OnUsage(usage(G, Opus, headers(52, 90, 98, 3, c.t))) // forced switch to W between ticks
	if got := pick(e, Opus, W, G); got.AuthID != W {
		t.Fatalf("expected the forced switch to W, got %+v", got)
	}
	e.Tick()
	if strings.Join(h.httpCalls, ",") != "tok-w-secret" {
		t.Fatalf("W's 15-minute-old data must be probed after the switch, got %v", h.httpCalls)
	}
}

func TestPausedDelegatesAndSkipsProbesAndAnchors(t *testing.T) {
	cfg := testConfig(t)
	cfg.Paused = true
	h := newHost()
	h.probeBody["tok-w-secret"] = idleProbe
	rec := history.NewRecorder(t.TempDir())
	e := New(cfg, h, (&clock{t0}).now, rec)
	e.OnUsage(usage(G, Opus, headers(52, 90, 30, 3, t0)))
	e.OnUsage(usage(G, Fable, headers(0, 90, 30, 3, t0)))
	e.Tick()
	if v := e.Status(); len(v.Changes) != 0 {
		t.Fatalf("paused must not record account changes: %+v", v.Changes)
	}
	if s, _ := rec.Query("24h", t0); len(s.Events) != 0 {
		t.Fatalf("paused must not record switch events: %+v", s.Events)
	}
	if got := pick(e, Opus, W, G); got != delegate {
		t.Fatalf("paused must delegate, got %+v", got)
	}
	if len(h.httpCalls) != 0 || len(h.execs) != 0 {
		t.Fatalf("paused must not probe or anchor: probes=%v execs=%d", h.httpCalls, len(h.execs))
	}
	if !e.Status().Paused {
		t.Fatal("status must report paused")
	}
}

func TestTrafficAndEventsReachHistory(t *testing.T) {
	c := &clock{t0}
	rec := history.NewRecorder(t.TempDir())
	e := New(testConfig(t), newHost(), c.now, rec)
	warm(e, t0, 30)
	failed := usage(G, Opus, headers(52, 90, 98, 3, t0))
	failed.Failed = true
	failed.Detail.InputTokens, failed.Detail.OutputTokens = 100, 20
	e.OnUsage(failed) // forced switch to W
	e.Tick()
	c.t = t0.Add(6 * time.Minute)
	e.Tick() // closes and writes the 12:00 slot
	s, err := rec.Query("24h", c.t)
	if err != nil {
		t.Fatal(err)
	}
	i := len(s.T) - 2 // the 12:00 point; the last one is the open 12:05 slot
	g := s.Accounts[G]
	if g == nil || g.Requests[i] != 3 || g.Tokens[i] != 120 || g.Main[i] == nil || *g.Main[i] != 52 || *g.MainCap[i] != 85 {
		t.Fatalf("point %d of G is wrong", i)
	}
	if s.Active["main"][i] != W {
		t.Fatalf("active %q", s.Active["main"][i])
	}
	var last history.Event
	for _, ev := range s.Events {
		if ev.Kind == "switch" && ev.Bucket == "main" {
			last = ev
		}
	}
	if last.From != G || last.To != W || last.Reason == "" {
		t.Fatalf("last main switch %+v", last)
	}
}

func TestHistorySurvivesANewEngine(t *testing.T) {
	c := &clock{t0}
	rec := history.NewRecorder(t.TempDir())
	cfg := testConfig(t)
	first := New(cfg, newHost(), c.now, rec)
	first.OnUsage(usage(W, Opus, headers(10, 140, 5, 4, t0)))
	second := New(cfg, newHost(), c.now, rec) // a reconfiguration
	c.t = t0.Add(6 * time.Minute)
	second.Tick()
	s, _ := rec.Query("24h", c.t)
	if s.Accounts[W] == nil || s.Accounts[W].Requests[len(s.T)-2] != 1 {
		t.Fatal("traffic from the first engine was lost")
	}
}

func TestStatusReportsExtraUsage(t *testing.T) {
	cfg := testConfig(t)
	cfg.Accounts[1].ExtraUsage = true
	v := New(cfg, newHost(), (&clock{t0}).now, nil).Status()
	if v.Accounts[0].ExtraUsage || !v.Accounts[1].ExtraUsage {
		t.Fatalf("extra usage per account: %+v %+v", v.Accounts[0].ExtraUsage, v.Accounts[1].ExtraUsage)
	}
}
