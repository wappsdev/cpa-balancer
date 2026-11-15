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
	"github.com/wappsdev/cpa-balancer/internal/transcripts"
)

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const (
	sessA = "11111111-aaaa-4bbb-8ccc-000000000001"
	sessB = "22222222-aaaa-4bbb-8ccc-000000000002"
	opus  = "claude-opus-5-5"
	haiku = "claude-haiku-5-5"
	fable = "claude-fable-5-2"
	// secret text sits in every fixture message: nothing the tool prints or stores may carry it
	secret = "TOP-SECRET-MESSAGE-TEXT"
)

var at = time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC)

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

// project writes a projects directory: file path under it -> lines.
func project(t *testing.T, files map[string][]string) string {
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

func run(t *testing.T, projects, store string, dry bool) Report {
	t.Helper()
	rep, err := Run(Options{Projects: projects, Store: store, DryRun: dry, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func storeDir(t *testing.T) string { return filepath.Join(t.TempDir(), "transcripts") }

func summary(store string) transcripts.Summary { return transcripts.Query(store, now, 90) }

func root(s transcripts.Summary, id string) *transcripts.Session {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return &s.Sessions[i]
		}
	}
	return nil
}

// storedRow is a line of the store as the tests read it: its raw form, not through the reader.
type storedRow struct {
	Session  string `json:"session"`
	Model    string `json:"model"`
	Advisor  bool   `json:"advisor"`
	Requests int64  `json:"requests"`
	Input    int64  `json:"input_tokens"`
	Output   int64  `json:"output_tokens"`
}

func rows(t *testing.T, store string) []storedRow {
	t.Helper()
	var out []storedRow
	files, _ := filepath.Glob(filepath.Join(store, "*.jsonl"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r storedRow
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
	}
	return out
}

func bytesOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			out[strings.TrimPrefix(p, dir)] = string(b)
		}
		return nil
	})
	return out
}

func TestAMessageSplitOverSeveralLinesCountsOnceWithItsLargestOutput(t *testing.T) {
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "msg_1", opus, at, usage{in: 10, out: 5, cacheRead: 100, cacheWrite: 7}),
		assistant(sessA, "msg_1", opus, at.Add(time.Second), usage{in: 10, out: 20, cacheRead: 100, cacheWrite: 7}),
		assistant(sessA, "msg_1", opus, at.Add(2*time.Second), usage{in: 10, out: 12, cacheRead: 100, cacheWrite: 7}),
		assistant(sessA, "msg_2", opus, at.Add(time.Minute), usage{in: 1, out: 1}),
	}})
	store := storeDir(t)
	rep := run(t, projects, store, false)
	s := root(summary(store), sessA)
	if s == nil || s.Total.Requests != 2 || s.Total.OutputTokens != 21 || s.Total.InputTokens != 11 || s.Total.CacheReadTokens != 100 || s.Total.CacheCreationTokens != 7 {
		t.Fatalf("%+v", s)
	}
	if rep.Messages != 2 {
		t.Fatalf("report %+v", rep)
	}
}

func TestAMessageRepeatedInTwoTranscriptsCountsOnce(t *testing.T) {
	// a resumed session copies its earlier messages into a new transcript
	projects := project(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl": {assistant(sessA, "msg_shared", opus, at, usage{in: 10, out: 5}), assistant(sessA, "msg_a", opus, at.Add(time.Minute), usage{in: 1, out: 1})},
		"proj-a/" + sessB + ".jsonl": {assistant(sessB, "msg_shared", opus, at, usage{in: 10, out: 5}), assistant(sessB, "msg_b", opus, at.Add(2*time.Minute), usage{in: 2, out: 1})},
	})
	store := storeDir(t)
	rep := run(t, projects, store, false)
	s := summary(store)
	if rep.Messages != 3 || s.Total.Requests != 3 || s.Total.InputTokens != 13 {
		t.Fatalf("report %+v total %+v", rep, s.Total)
	}
	// the tie goes to the smaller session id, whatever the order the files were read in
	if a := root(s, sessA); a == nil || a.Total.Requests != 2 {
		t.Fatalf("%+v", a)
	}
	if b := root(s, sessB); b == nil || b.Total.Requests != 1 {
		t.Fatalf("%+v", b)
	}
}

func TestMessagesWithoutAnIdAreNotMergedAcrossSessions(t *testing.T) {
	noID := func(session string) string { return assistant(session, "", opus, at, usage{in: 3, out: 1}) }
	projects := project(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl": {noID(sessA), noID(sessA)}, // two lines: two messages, each its own
		"proj-a/" + sessB + ".jsonl": {noID(sessB)},
	})
	store := storeDir(t)
	run(t, projects, store, false)
	if s := summary(store); s.Total.Requests != 3 {
		t.Fatalf("%+v", s.Total)
	}
}

func TestSubagentTranscriptsRollIntoTheirParentAndAreKeptPerChild(t *testing.T) {
	projects := project(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl":                                     {assistant(sessA, "msg_1", opus, at, usage{in: 10, out: 1})},
		"proj-a/" + sessA + "/subagents/agent-ab12.jsonl":                {assistant(sessA, "msg_s1", haiku, at.Add(time.Minute), usage{in: 4, out: 1})},
		"proj-a/" + sessA + "/subagents/agent-cd34.jsonl":                {assistant(sessA, "msg_s2", haiku, at.Add(2*time.Minute), usage{in: 6, out: 1}), assistant(sessA, "msg_s1", haiku, at, usage{in: 4, out: 1})},
		"proj-a/" + sessA + "/subagents/workflows/wf_x/agent-ef56.jsonl": {assistant(sessA, "msg_w1", haiku, at.Add(3*time.Minute), usage{in: 100, out: 1})},
		"proj-a/" + sessA + "/subagents/notes-without-an-agent-id.jsonl": {assistant(sessA, "msg_n1", haiku, at.Add(4*time.Minute), usage{in: 1000, out: 1})},
		"proj-a/" + sessB + ".jsonl":                                     {assistant(sessB, "msg_9", opus, at, usage{in: 1, out: 1})},
		"proj-a/ignored-notes.txt":                                       {"not a transcript"},
		"proj-a/" + sessA + "/subagents/not-a-transcript.md":             {"nope"},
	})
	store := storeDir(t)
	rep := run(t, projects, store, false)
	s := summary(store)
	a := root(s, sessA)
	if a == nil || len(s.Sessions) != 2 || a.Total.Requests != 5 || a.Total.InputTokens != 10+4+6+100+1000 {
		t.Fatalf("a parent counts every message of its subagents, once: %+v", a)
	}
	got := map[string]int64{}
	for _, c := range a.Subagents {
		got[c.ID] = c.Total.InputTokens
	}
	// msg_s1 sits in two child files: it goes to the one whose id sorts first
	want := map[string]int64{sessA + "/ab12": 4, sessA + "/cd34": 6, sessA + "/ef56": 100}
	if len(got) != len(want) || got[sessA+"/ab12"] != 4 || got[sessA+"/cd34"] != 6 || got[sessA+"/ef56"] != 100 {
		t.Fatalf("each child with a derivable agent id is kept under its canonical id: %v", got)
	}
	// the store counts every message once: the rows add up to the messages
	var requests int64
	for _, r := range rows(t, store) {
		if !r.Advisor {
			requests += r.Requests
		}
	}
	if requests != int64(rep.Messages) || requests != 6 {
		t.Fatalf("rows hold %d requests, the report %d messages", requests, rep.Messages)
	}
	if rep.MainFiles != 2 || rep.SubagentFiles != 3+1 || rep.NestedFiles != 1 {
		t.Fatalf("files %+v", rep)
	}
}

func TestAnAdvisorIterationIsItsOwnRowWithTheAdvisorModel(t *testing.T) {
	iterations := []map[string]any{
		{"type": "message", "model": opus, "input_tokens": 3, "output_tokens": 139},
		{"type": "message", "model": opus, "input_tokens": 3, "output_tokens": 689},
		{"type": "advisor_message", "model": fable, "input_tokens": 775000, "output_tokens": 6344, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 50},
		{"type": "advisor_message", "model": fable}, // an advisor row with no use is nothing
	}
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", opus, at, usage{in: 6, out: 828, iterations: iterations}),
	}})
	store := storeDir(t)
	run(t, projects, store, false)
	s := summary(store)
	a := root(s, sessA)
	if a.Total.OutputTokens != 828 || a.Total.Requests != 1 || s.Total.OutputTokens != 828 || a.Total.InputTokens != 6 {
		t.Fatalf("the top-level usage is the executor's own: %+v", a.Total)
	}
	adv := a.AdvisorModels[fable]
	if a.Advisor.Requests != 1 || adv.InputTokens != 775000 || adv.OutputTokens != 6344 || adv.CacheReadTokens != 100 || adv.CacheCreationTokens != 50 || len(a.AdvisorModels) != 1 {
		t.Fatalf("advisor %+v %+v", a.Advisor, a.AdvisorModels)
	}
	var flagged []storedRow
	for _, r := range rows(t, store) {
		if r.Advisor {
			flagged = append(flagged, r)
		}
	}
	if len(flagged) != 1 || flagged[0].Model != fable || flagged[0].Session != sessA || flagged[0].Input != 775000 {
		t.Fatalf("the advisor row carries the advisor model and its flag: %+v", flagged)
	}
}

func TestMessagesAreCountedPerUTCHourAndModel(t *testing.T) {
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", opus, at.Add(5*time.Minute), usage{in: 1, out: 1}),
		assistant(sessA, "m2", opus, at.Add(50*time.Minute), usage{in: 2, out: 1}),
		assistant(sessA, "m3", haiku, at.Add(50*time.Minute), usage{in: 4, out: 1}),
		assistant(sessA, "m4", opus, at.Add(70*time.Minute), usage{in: 8, out: 1}),
	}})
	store := storeDir(t)
	run(t, projects, store, false)
	got := rows(t, store)
	if len(got) != 3 {
		t.Fatalf("hour 11 opus, hour 11 haiku, hour 12 opus: %+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(store, "2026-10-04.jsonl"))
	if !strings.Contains(string(b), `"hour":"2026-10-04T11:00:00Z"`) || !strings.Contains(string(b), `"hour":"2026-10-04T12:00:00Z"`) {
		t.Fatalf("%s", b)
	}
}

func TestRunningTwiceLeavesTheSameBytes(t *testing.T) {
	projects := project(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl":                      {assistant(sessA, "m1", opus, at, usage{in: 10, out: 1}), assistant(sessA, "m2", opus, at.Add(30*time.Hour), usage{in: 10, out: 1})},
		"proj-a/" + sessA + "/subagents/agent-ab12.jsonl": {assistant(sessA, "m3", haiku, at, usage{in: 1, out: 1})},
	})
	store := storeDir(t)
	first := run(t, projects, store, false)
	before := bytesOf(t, store)
	second := run(t, projects, store, false)
	after := bytesOf(t, store)
	if len(before) != 2 || len(after) != len(before) {
		t.Fatalf("%d %d files", len(before), len(after))
	}
	for name, b := range before {
		if after[name] != b {
			t.Fatalf("%s changed on a rerun", name)
		}
	}
	if first.Rows != second.Rows || first.Rows != 3 {
		t.Fatalf("%+v %+v", first, second)
	}
}

func TestARunReplacesWhatTheStoreHeld(t *testing.T) {
	store := storeDir(t)
	if err := transcripts.Write(store, map[transcripts.Key]history.Traffic{{Hour: at, Session: "gone", Model: opus}: {Requests: 9}}); err != nil {
		t.Fatal(err)
	}
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", opus, at.Add(24*time.Hour), usage{in: 1, out: 1})}})
	run(t, projects, store, false)
	s := summary(store)
	if root(s, "gone") != nil || s.Total.Requests != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestADryRunWritesNothingAndPrintsOnlyCounts(t *testing.T) {
	long := `{"type":"user","timestamp":"2026-10-04T11:00:00Z","message":{"content":"` + strings.Repeat("z", MaxLine+10) + `"}}`
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", opus, at, usage{in: 10, out: 1}),
		long,
		"not json at all",
		assistant(sessA, "m2", opus, at.Add(24*time.Hour), usage{in: 20, out: 2}),
	}})
	store := storeDir(t)
	rep := run(t, projects, store, true)
	if _, err := os.Stat(store); err == nil {
		t.Fatal("a dry run must not create the store")
	}
	if rep.Rows != 2 || rep.Sessions != 1 || rep.SkippedLines != 2 || rep.Messages != 2 || !rep.DryRun {
		t.Fatalf("%+v", rep)
	}
	var out bytes.Buffer
	rep.Write(&out)
	text := out.String()
	for _, want := range []string{"2026-10-04", "2026-10-05", "dry run"} {
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

func TestNothingOfTheTextReachesTheStore(t *testing.T) {
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", opus, at, usage{in: 1, out: 1})}})
	store := storeDir(t)
	run(t, projects, store, false)
	for name, b := range bytesOf(t, store) {
		if strings.Contains(b, secret) || strings.Contains(b, "proj-a") {
			t.Fatalf("%s holds text or a project name", name)
		}
	}
}

func TestRecordsTooNewOrPastRetentionAreLeftOut(t *testing.T) {
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "old", opus, now.Add(-history.Retention-time.Hour), usage{in: 1, out: 1}),
		assistant(sessA, "future", opus, now.Add(time.Minute), usage{in: 1, out: 1}),
		assistant(sessA, "ok", opus, now.Add(-time.Hour), usage{in: 1, out: 1}),
		assistant(sessA, "just-now", opus, now.Add(-time.Minute), usage{in: 1, out: 1}), // no live slot to keep clear of: counted
		line(map[string]any{"type": "assistant", "sessionId": sessA, "timestamp": "garbage", "message": map[string]any{"id": "bad-time", "model": "m", "usage": map[string]any{"output_tokens": 5}}}),
		line(map[string]any{"type": "assistant", "sessionId": sessA, "timestamp": at.Format(time.RFC3339), "message": map[string]any{"id": "empty", "model": "<synthetic>", "usage": map[string]any{}}}),
	}})
	store := storeDir(t)
	rep := run(t, projects, store, false)
	if s := summary(store); s.Total.Requests != 2 || rep.Messages != 2 {
		t.Fatalf("%+v %+v", s.Total, rep)
	}
}

func TestAnIdOrModelOutsideTheLiveStoresBoundsIsStoredAsOther(t *testing.T) {
	projects := project(t, map[string][]string{"proj-a/bad id!.jsonl": {assistant("x", "m1", strings.Repeat("m", 65), at, usage{in: 1, out: 1})}})
	store := storeDir(t)
	run(t, projects, store, false)
	got := rows(t, store)
	if len(got) != 1 || got[0].Session != history.OtherSession || got[0].Model != history.OtherSession {
		t.Fatalf("%+v", got)
	}
}

func TestAMissingProjectsDirectoryIsAnError(t *testing.T) {
	if _, err := Run(Options{Projects: filepath.Join(t.TempDir(), "nope"), Store: storeDir(t), Now: now}); err == nil {
		t.Fatal("expected an error")
	}
}
