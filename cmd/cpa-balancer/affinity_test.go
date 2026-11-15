package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/history"
)

const affinitySession = "11111111-2222-3333-4444-555555555555"

// affinitySetup configures two accounts and a projects directory holding one session's transcript.
func affinitySetup(t *testing.T, extra string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(filepath.Join(projects, "-work-alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "-work-alpha", affinitySession+".jsonl"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := recorder
	recorder = history.NewRecorder(filepath.Join(dir, "history"))
	t.Cleanup(func() { recorder = previous })
	configureYAML(t, fmt.Sprintf("state_path: %s/state.json\naccounts: [{auth_file: a.json}, {auth_file: b.json}]\nbuckets: {main: [m]}\naffinity: {projects_dir: %s}\n%s",
		dir, projects, extra))
	t.Cleanup(shutdown)
	return dir
}

func usageVia(t *testing.T, account string, weekPercent float64) {
	t.Helper()
	reset := func(h int) string { return strconv.FormatInt(time.Now().Add(time.Duration(h)*time.Hour).Unix(), 10) }
	rec := pluginapi.UsageRecord{Provider: "claude", AuthID: account, Model: "m", ResponseHeaders: http.Header{
		"Anthropic-Ratelimit-Unified-7d-Utilization": {strconv.FormatFloat(weekPercent/100, 'f', 4, 64)},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {reset(140)},
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.0100"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {reset(3)},
	}}
	raw, _ := json.Marshal(rec)
	dispatch(pluginabi.MethodUsageHandle, raw)
}

// pickViaWire sends a scheduler pick the way the host does: JSON of the request, the client's
// headers inside Options.
func pickViaWire(t *testing.T, headers http.Header) pluginapi.SchedulerPickResponse {
	t.Helper()
	req := pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: "m",
		Options:    pluginapi.SchedulerOptions{Headers: headers},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a.json"}, {ID: "b.json"}}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(dispatch(pluginabi.MethodSchedulerPick, raw), &env); err != nil || !env.OK {
		t.Fatalf("envelope: %+v %v", env, err)
	}
	var resp pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestASchedulerPickOnTheWireUsesTheSessionsProject(t *testing.T) {
	affinitySetup(t, "")
	usageVia(t, "a.json", 80) // b has far more room
	usageVia(t, "b.json", 10)
	h := http.Header{"X-Claude-Code-Session-Id": {affinitySession}, "Content-Type": {"application/json"}}
	if got := pickViaWire(t, h); got.AuthID != "b.json" || !got.Handled {
		t.Fatalf("%+v", got)
	}
	v := pluginAPI{}.Status().(engine.View)
	if v.Affinity == nil || v.Affinity.Sessions[affinitySession].Reason != engine.ReasonProject || v.Affinity.Sessions[affinitySession].Project != "-work-alpha" {
		t.Fatalf("%+v", v.Affinity)
	}
	// a session of no known folder is unchanged
	if got := pickViaWire(t, http.Header{"X-Claude-Code-Session-Id": {"99999999-2222-3333-4444-555555555555"}}); got != pickViaWire(t, http.Header{}) {
		t.Fatalf("%+v", got)
	}
}

func TestAHomeAssignedJustBeforeAConfigChangeIsNotLost(t *testing.T) {
	affinitySetup(t, "")
	usageVia(t, "a.json", 80)
	usageVia(t, "b.json", 10)
	pickViaWire(t, http.Header{"X-Claude-Code-Session-Id": {affinitySession}}) // places the project; not yet on disk
	before := engineNow()
	dir := filepath.Dir(before.Config().StatePath)
	configureYAML(t, fmt.Sprintf("state_path: %s/state.json\naccounts: [{auth_file: a.json}, {auth_file: b.json}]\nbuckets: {main: [m]}\nmode: shadow\naffinity: {projects_dir: %s/projects}\n", dir, dir))
	if engineNow() == before {
		t.Fatal("setup: the configuration must build a new engine")
	}
	v := pluginAPI{}.Status().(engine.View)
	if len(v.Affinity.Homes) != 1 || v.Affinity.Homes[0].Account != "b.json" {
		t.Fatalf("the new engine starts from what the old one knew: %+v", v.Affinity)
	}
}

func TestPinsAreSetAndClearedThroughThePluginAPI(t *testing.T) {
	affinitySetup(t, "")
	if err := (pluginAPI{}).Pin("session", affinitySession, "b.json"); err != nil {
		t.Fatal(err)
	}
	v := pluginAPI{}.Status().(engine.View)
	if len(v.Affinity.SessionPins) != 1 || v.Affinity.SessionPins[0].Account != "b.json" || v.Affinity.SessionPins[0].Source != engine.SourceUI {
		t.Fatalf("%+v", v.Affinity.SessionPins)
	}
	if err := (pluginAPI{}).Pin("session", affinitySession, ""); err != nil {
		t.Fatal(err)
	}
	if v := (pluginAPI{}).Status().(engine.View); len(v.Affinity.SessionPins) != 0 {
		t.Fatalf("%+v", v.Affinity.SessionPins)
	}
	if (pluginAPI{}).Pin("session", affinitySession, "other.json") == nil || (pluginAPI{}).Pin("folder", "a/b", "a.json") == nil {
		t.Fatal("an unknown account and a bad folder are refused")
	}
}
