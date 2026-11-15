package backfill

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const (
	sessA = "11111111-aaaa-bbbb-cccc-000000000001"
	sessB = "22222222-aaaa-bbbb-cccc-000000000002"
	// secret text sits in every fixture message: nothing the tool prints or stores may carry it
	secret = "TOP-SECRET-MESSAGE-TEXT"
)

type usage struct {
	in, out, cacheRead, cacheWrite int64
	iterations                     []map[string]any
}

// assistant builds one transcript line.
func assistant(session, id, model string, at time.Time, u usage) string {
	us := map[string]any{"input_tokens": u.in, "output_tokens": u.out, "cache_read_input_tokens": u.cacheRead, "cache_creation_input_tokens": u.cacheWrite}
	if u.iterations != nil {
		us["iterations"] = u.iterations
	}
	return line(map[string]any{"type": "assistant", "sessionId": session, "timestamp": at.Format(time.RFC3339Nano), "isSidechain": false,
		"message": map[string]any{"id": id, "model": model, "usage": us, "content": []map[string]any{{"type": "text", "text": secret}}}})
}

func line(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// transcripts writes a projects directory: project -> file path under it -> lines.
func transcripts(t *testing.T, files map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, lines := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func run(t *testing.T, projects, hist string, dry bool) Report {
	t.Helper()
	rep, err := Run(Options{Projects: projects, History: hist, FableMark: "fable", DryRun: dry, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func window(t *testing.T, hist string) history.SessionWindow {
	t.Helper()
	return history.NewRecorder(hist).Sessions(now).Week
}

func row(w history.SessionWindow, id string) *history.SessionUsage {
	for i := range w.Sessions {
		if w.Sessions[i].ID == id {
			return &w.Sessions[i]
		}
	}
	return nil
}

var at = time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC)

func TestAMessageSplitOverSeveralLinesCountsOnceWithItsLargestOutput(t *testing.T) {
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "msg_1", "claude-opus-5-5", at, usage{in: 10, out: 5, cacheRead: 100, cacheWrite: 7}),
		assistant(sessA, "msg_1", "claude-opus-5-5", at.Add(time.Second), usage{in: 10, out: 20, cacheRead: 100, cacheWrite: 7}),
		assistant(sessA, "msg_1", "claude-opus-5-5", at.Add(2*time.Second), usage{in: 10, out: 12, cacheRead: 100, cacheWrite: 7}),
		assistant(sessA, "msg_2", "claude-opus-5-5", at.Add(time.Minute), usage{in: 1, out: 1}),
	}})
	hist := t.TempDir()
	rep := run(t, root, hist, false)
	s := row(window(t, hist), sessA)
	if s == nil || s.Own.Requests != 2 || s.Own.OutputTokens != 21 || s.Own.InputTokens != 11 || s.Own.CacheReadTokens != 100 || s.Own.CacheCreationTokens != 7 {
		t.Fatalf("%+v", s)
	}
	if rep.Messages != 2 {
		t.Fatalf("report %+v", rep)
	}
}

func TestSubagentTranscriptsCountUnderTheirParentSession(t *testing.T) {
	root := transcripts(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl":                         {assistant(sessA, "msg_1", "claude-opus-5-5", at, usage{in: 10, out: 1})},
		"proj-a/" + sessA + "/subagents/agent-one.jsonl":     {assistant(sessA, "msg_s1", "claude-haiku-5-5", at.Add(time.Minute), usage{in: 4, out: 1})},
		"proj-a/" + sessA + "/subagents/agent-two.jsonl":     {assistant(sessA, "msg_s2", "claude-haiku-5-5", at.Add(2*time.Minute), usage{in: 6, out: 1}), assistant(sessA, "msg_s1", "claude-haiku-5-5", at, usage{in: 4, out: 1})},
		"proj-a/" + sessB + ".jsonl":                         {assistant(sessB, "msg_9", "claude-opus-5-5", at, usage{in: 1, out: 1})},
		"proj-a/ignored-notes.txt":                           {"not a transcript"},
		"proj-a/" + sessA + "/subagents/not-a-transcript.md": {"nope"},
	})
	hist := t.TempDir()
	run(t, root, hist, false)
	w := window(t, hist)
	a := row(w, sessA)
	if a == nil || a.Own.Requests != 3 || a.Own.InputTokens != 20 || len(a.Subagents) != 0 {
		t.Fatalf("a message in a subagent file is part of its parent, once: %+v", a)
	}
	if row(w, sessB) == nil || len(w.Sessions) != 2 {
		t.Fatalf("%+v", w.Sessions)
	}
}

func TestRunningTwiceAddsNothing(t *testing.T) {
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "msg_1", "claude-opus-5-5", at, usage{in: 10, out: 1}),
		assistant(sessA, "msg_2", "claude-opus-5-5", at.Add(30*time.Minute), usage{in: 10, out: 1}),
	}})
	hist := t.TempDir()
	first := run(t, root, hist, false)
	before := window(t, hist)
	second := run(t, root, hist, false)
	after := window(t, hist)
	if first.SlotsAdded != 2 || second.SlotsAdded != 0 || second.SlotsLive != 2 {
		t.Fatalf("first %+v second %+v", first, second)
	}
	if before.Total != after.Total || after.Total.Requests != 2 {
		t.Fatalf("before %+v after %+v", before.Total, after.Total)
	}
}

func TestEverySlotThePluginWroteIsLeftAlone(t *testing.T) {
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "msg_1", "claude-opus-5-5", at.Add(5*time.Minute), usage{in: 10, out: 1}),                // the slot with live traffic
		assistant(sessA, "msg_2", "claude-opus-5-5", at.Add(4*time.Minute+50*time.Second), usage{in: 20, out: 1}), // 10 s before it, in the slot the plugin saw idle
		assistant(sessA, "msg_3", "claude-opus-5-5", at.Add(20*time.Minute), usage{in: 40, out: 1}),               // a time with no slot at all
	}})
	hist := t.TempDir()
	live := history.NewRecorder(hist)
	// The plugin writes a slot on every tick: 11:00 saw no traffic, 11:05 saw one request.
	live.Snapshot(at, map[string]string{"main": "g"}, map[string]history.Levels{"g": {}})
	live.AddTraffic(at.Add(5*time.Minute), history.Source{Account: "g", Model: "claude-opus-5-5", Session: sessB}, history.Traffic{Requests: 1, InputTokens: 99})
	if err := live.Flush(at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rep := run(t, root, hist, false)
	if rep.SlotsAdded != 1 || rep.SlotsLive != 2 {
		t.Fatalf("a slot the plugin wrote, idle or busy, must not take transcript traffic: %+v", rep)
	}
	w := window(t, hist)
	if b := row(w, sessB); b == nil || b.Own.InputTokens != 99 {
		t.Fatalf("live data must stay as it was: %+v", b)
	}
	if a := row(w, sessA); a == nil || a.Own.Requests != 1 || a.Own.InputTokens != 40 || !a.Estimated {
		t.Fatalf("only the message in a slot the plugin never wrote is added: %+v", a)
	}
}

func TestEachRequestGoesToTheAccountTheBalancerHadActive(t *testing.T) {
	const opus, fable = "claude-opus-5-5", "claude-fable-5-2"
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", opus, at.Add(-3*time.Hour), usage{in: 1, out: 1}),   // before any record: unknown
		assistant(sessA, "m2", opus, at.Add(10*time.Minute), usage{in: 2, out: 1}), // the plugin was down: the last record says g
		assistant(sessA, "m3", opus, at.Add(20*time.Minute), usage{in: 4, out: 1}),
		assistant(sessA, "m4", fable, at.Add(50*time.Minute), usage{in: 8, out: 1}), // Fable stays on g
		assistant(sessA, "m5", opus, at.Add(50*time.Minute), usage{in: 16, out: 1}), // after the switch to w at 11:32
		assistant(sessA, "m6", opus, at.Add(6*time.Hour), usage{in: 32, out: 1}),    // more than an hour after the last record: unknown
	}})
	hist := t.TempDir()
	live := history.NewRecorder(hist)
	// The plugin ran until 11:05, was down, and came back for one slot at 11:30 that saw a switch.
	live.Snapshot(at.Add(-5*time.Minute), map[string]string{"main": "g", "fable": "g"}, nil)
	live.Snapshot(at, map[string]string{"main": "g", "fable": "g"}, nil)
	live.AddEvent(history.Event{At: at.Add(32 * time.Minute), Kind: "switch", Bucket: "main", From: "g", To: "w"})
	if err := live.Flush(at.Add(24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	run(t, root, hist, false)
	got := window(t, hist).Sessions[0].Accounts
	want := map[string]int64{Unknown: 2, "g": 3, "w": 1}
	for account, requests := range want {
		if got[account].Requests != requests {
			t.Errorf("%s has %d requests, want %d (%+v)", account, got[account].Requests, requests, got)
		}
	}
	if got["g"].InputTokens != 2+4+8 || got["w"].InputTokens != 16 {
		t.Errorf("%+v", got)
	}
}

func TestAdvisorIterationsAreCountedApartFromTheExecutorsOwnUse(t *testing.T) {
	iterations := []map[string]any{
		{"type": "message", "model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 139},
		{"type": "message", "model": "claude-opus-5-5", "input_tokens": 3, "output_tokens": 689},
		{"type": "advisor_message", "model": "claude-fable-5-2", "input_tokens": 775000, "output_tokens": 6344, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 50},
	}
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", "claude-opus-5-5", at, usage{in: 6, out: 828, iterations: iterations}),
	}})
	hist := t.TempDir()
	run(t, root, hist, false)
	w := window(t, hist)
	s := row(w, sessA)
	if s.Own.OutputTokens != 828 || s.Own.Requests != 1 || w.Total.OutputTokens != 828 {
		t.Fatalf("the top-level usage is the executor's: %+v", s.Own)
	}
	adv := s.AdvisorModels["claude-fable-5-2"]
	if s.Advisor.Requests != 1 || adv.InputTokens != 775000 || adv.OutputTokens != 6344 || adv.CacheReadTokens != 100 || adv.CacheCreationTokens != 50 || len(s.AdvisorModels) != 1 {
		t.Fatalf("advisor %+v %+v", s.Advisor, s.AdvisorModels)
	}
}

func TestDryRunWritesNothingAndPrintsOnlyCounts(t *testing.T) {
	long := `{"type":"user","timestamp":"2026-10-04T11:00:00Z","message":{"content":"` + strings.Repeat("z", MaxLine+10) + `"}}`
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", "claude-opus-5-5", at, usage{in: 10, out: 1}),
		long,
		"not json at all",
		assistant(sessA, "m2", "claude-opus-5-5", at.Add(24*time.Hour), usage{in: 20, out: 2}),
	}})
	hist := filepath.Join(t.TempDir(), "history")
	rep := run(t, root, hist, true)
	if _, err := os.Stat(hist); err == nil {
		t.Fatal("a dry run must not create the history directory")
	}
	if rep.SlotsAdded != 0 || rep.SlotsWould != 2 || rep.Sessions != 1 || rep.SkippedLines != 2 {
		t.Fatalf("%+v", rep)
	}
	var out bytes.Buffer
	rep.Write(&out)
	text := out.String()
	for _, want := range []string{"2026-10-04", "2026-10-05"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %s in\n%s", want, text)
		}
	}
	for _, leak := range []string{secret, sessA, "proj-a", "opus"} {
		if strings.Contains(text, leak) {
			t.Errorf("the report names %q:\n%s", leak, text)
		}
	}
}

func TestRecordsTooNewOrPastRetentionAreLeftOut(t *testing.T) {
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "old", "claude-opus-5-5", now.Add(-history.Retention-time.Hour), usage{in: 1, out: 1}),
		assistant(sessA, "fresh", "claude-opus-5-5", now.Add(-time.Minute), usage{in: 1, out: 1}), // the plugin's open slot
		assistant(sessA, "ok", "claude-opus-5-5", now.Add(-time.Hour), usage{in: 1, out: 1}),
		line(map[string]any{"type": "assistant", "sessionId": sessA, "timestamp": "garbage", "message": map[string]any{"id": "bad-time", "model": "m", "usage": map[string]any{"output_tokens": 5}}}),
		line(map[string]any{"type": "assistant", "sessionId": sessA, "timestamp": at.Format(time.RFC3339), "message": map[string]any{"id": "empty", "model": "<synthetic>", "usage": map[string]any{}}}),
	}})
	hist := t.TempDir()
	rep := run(t, root, hist, false)
	if w := window(t, hist); w.Total.Requests != 1 || rep.Messages != 1 {
		t.Fatalf("%+v %+v", w.Total, rep)
	}
}

func TestSessionsAndAccountsStillAddUpAfterAnImport(t *testing.T) {
	root := transcripts(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl":                   {assistant(sessA, "m1", "claude-opus-5-5", at, usage{in: 10, out: 1, cacheRead: 5}), assistant(sessA, "m2", "claude-fable-5-2", at.Add(time.Hour), usage{in: 3, out: 1})},
		"proj-b/" + sessB + ".jsonl":                   {assistant(sessB, "m3", "claude-haiku-5-5", at, usage{in: 7, out: 1, cacheWrite: 2})},
		"proj-b/" + sessB + "/subagents/agent-x.jsonl": {assistant(sessB, "m4", "claude-haiku-5-5", at.Add(5*time.Minute), usage{in: 9, out: 1})},
	})
	hist := t.TempDir()
	run(t, root, hist, false)
	w := window(t, hist)
	var roots, accounts history.Traffic
	for _, s := range w.Sessions {
		roots.InputTokens += s.Total.InputTokens
		roots.Requests += s.Total.Requests
	}
	for _, a := range w.Accounts {
		accounts.InputTokens += a.InputTokens
		accounts.Requests += a.Requests
	}
	if roots.Requests != 4 || roots.InputTokens != 29 || accounts != roots || w.Total.Requests != 4 {
		t.Fatalf("roots %+v accounts %+v total %+v", roots, accounts, w.Total)
	}
}

func TestAMissingProjectsDirectoryIsAnError(t *testing.T) {
	if _, err := Run(Options{Projects: filepath.Join(t.TempDir(), "nope"), History: t.TempDir(), Now: now}); err == nil {
		t.Fatal("expected an error")
	}
}
