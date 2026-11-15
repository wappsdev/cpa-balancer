package backfill

import (
	"bytes"
	"encoding/json"
	"errors"
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
	sessC = "33333333-aaaa-4bbb-8ccc-000000000003"
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

// titleLine builds one of the lines that name a session: kind is custom-title, agent-name or ai-title.
func titleLine(kind, session, value string) string {
	field := map[string]string{"custom-title": "customTitle", "agent-name": "agentName", "ai-title": "aiTitle"}[kind]
	return line(map[string]any{"type": kind, field: value, "sessionId": session})
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

func titleOf(s transcripts.Summary, id string) (string, string) {
	if r := root(s, id); r != nil {
		return r.Title, r.TitleKind
	}
	return "", "?"
}

func TestTheLastTitleOfEachKindWinsAndTheMostDeliberateKindShows(t *testing.T) {
	msg := func(session string) string { return assistant(session, "m-"+session, opus, at, usage{in: 1, out: 1}) }
	projects := project(t, map[string][]string{
		// a custom name set before the later automatic ones still shows; the last custom one wins
		"proj-a/" + sessA + ".jsonl": {titleLine("custom-title", sessA, "Custom one"), msg(sessA), titleLine("ai-title", sessA, "Ai late"), titleLine("agent-name", sessA, "Agent late"), titleLine("custom-title", sessA, "Custom two")},
		// no custom name: the agent's name beats the automatic one, whatever the order
		"proj-a/" + sessB + ".jsonl": {titleLine("agent-name", sessB, "Agent first"), titleLine("ai-title", sessB, "Ai second"), titleLine("agent-name", sessB, "Agent last"), titleLine("ai-title", sessB, "Ai last"), msg(sessB)},
		"proj-a/" + sessC + ".jsonl": {titleLine("ai-title", sessC, "Ai one"), msg(sessC), titleLine("ai-title", sessC, "Ai two")},
	})
	store := storeDir(t)
	rep := run(t, projects, store, false)
	s := summary(store)
	for id, want := range map[string][2]string{sessA: {"Custom two", "custom"}, sessB: {"Agent last", "agent"}, sessC: {"Ai two", "ai"}} {
		if title, kind := titleOf(s, id); title != want[0] || kind != want[1] {
			t.Errorf("%s: %q %q, want %q %q", id[:2], title, kind, want[0], want[1])
		}
	}
	if rep.Titled != 3 {
		t.Fatalf("%+v", rep)
	}
}

func TestATitleOnlyCountsForTheSessionWhoseTranscriptHoldsIt(t *testing.T) {
	projects := project(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl": {
			assistant(sessA, "m1", opus, at, usage{in: 1, out: 1}),
			titleLine("custom-title", sessB, "Copied from another session"), // a line copied in from a resumed session
		},
		"proj-a/" + sessA + "/subagents/agent-ab12.jsonl": {assistant(sessA, "m2", haiku, at, usage{in: 1, out: 1}), titleLine("custom-title", sessA, "Named by a subagent")},
		"proj-a/" + sessB + ".jsonl":                      {titleLine("custom-title", sessB, "A name with no usage behind it")},
	})
	store := storeDir(t)
	rep := run(t, projects, store, false)
	if title, _ := titleOf(summary(store), sessA); title != "" {
		t.Fatalf("a name from another session or a subagent's file is not the session's: %q", title)
	}
	if rep.Titled != 0 {
		t.Fatalf("%+v", rep)
	}
	if _, err := os.Stat(filepath.Join(store, transcripts.TitlesFile)); err == nil {
		t.Fatal("no title, no titles file")
	}
}

func TestATitleIsBoundedAndStrippedOfControlCharacters(t *testing.T) {
	hostile := "<img src=x onerror=alert(1)>\x00\x1b[31m\n" + strings.Repeat("z", 400)
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", opus, at, usage{in: 1, out: 1}), titleLine("custom-title", sessA, hostile)}})
	store := storeDir(t)
	run(t, projects, store, false)
	title, kind := titleOf(summary(store), sessA)
	if kind != "custom" || len([]rune(title)) != transcripts.MaxTitle || !strings.HasPrefix(title, "<img src=x onerror=alert(1)>[31m") || strings.ContainsAny(title, "\x00\x1b\n") {
		t.Fatalf("%q %q", title, kind)
	}
	b, _ := os.ReadFile(filepath.Join(store, transcripts.TitlesFile))
	if strings.Contains(string(b), `\u001b`) || strings.Contains(string(b), `\u0000`) || strings.Count(string(b), "\n") != 1 {
		t.Fatalf("%s", b)
	}
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

func TestLinksAreNotFollowed(t *testing.T) {
	elsewhere := project(t, map[string][]string{"agent-ab12.jsonl": {assistant(sessA, "m-linked", haiku, at, usage{in: 1, out: 1})}})
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", opus, at, usage{in: 1, out: 1})}})
	if err := os.MkdirAll(filepath.Join(projects, "proj-a", sessA), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(projects, "proj-a", sessA, "subagents")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(projects, "proj-a", sessA+".jsonl"), filepath.Join(projects, "proj-a", sessB+".jsonl")); err != nil {
		t.Fatal(err)
	}
	store := storeDir(t)
	rep := run(t, projects, store, false)
	if rep.Messages != 1 || rep.MainFiles != 1 || rep.SubagentFiles != 0 {
		t.Fatalf("a linked directory or file is not read: %+v", rep)
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

func TestARunReplacesTheDaysItCoversAndKeepsOlderDays(t *testing.T) {
	store := storeDir(t)
	older := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC) // Claude Code has deleted these transcripts
	if err := transcripts.Write(store, map[transcripts.Key]history.Traffic{
		{Hour: older, Session: "older-session", Model: opus}:         {Requests: 4, InputTokens: 4},
		{Hour: at.Add(24 * time.Hour), Session: "gone", Model: opus}: {Requests: 9},
	}, nil, now); err != nil {
		t.Fatal(err)
	}
	oldFile := bytesOf(t, store)["/2026-09-01.jsonl"]
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", opus, at.Add(24*time.Hour), usage{in: 1, out: 1})}})
	run(t, projects, store, false)
	run(t, projects, store, false) // and a rerun changes nothing more
	s := summary(store)
	if root(s, "gone") != nil || root(s, sessA) == nil || root(s, "older-session") == nil || s.Total.Requests != 5 {
		t.Fatalf("a covered day is replaced, an older one outlives the transcripts: %+v", s)
	}
	if bytesOf(t, store)["/2026-09-01.jsonl"] != oldFile {
		t.Fatal("an older day file must stay byte for byte")
	}
}

func TestARunThatFindsNoMessageFailsAndLeavesTheStoreAlone(t *testing.T) {
	store := storeDir(t)
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", opus, at, usage{in: 1, out: 1}), titleLine("custom-title", sessA, "Made-up name")}})
	run(t, projects, store, false)
	before := bytesOf(t, store)
	if len(before) == 0 {
		t.Fatal("the fixture store is empty")
	}
	for name, dir := range map[string]string{
		"an empty projects directory":           t.TempDir(),
		"one with a project but no transcripts": project(t, map[string][]string{"proj-a/notes.txt": {"x"}}),
		"one with only a title":                 project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {titleLine("custom-title", sessA, "Other name")}}),
	} {
		rep, err := Run(Options{Projects: dir, Store: store, Now: now})
		if err == nil || !errors.Is(err, ErrNoMessages) || rep.Messages != 0 {
			t.Fatalf("%s: %v %+v", name, err, rep)
		}
		after := bytesOf(t, store)
		if len(after) != len(before) {
			t.Fatalf("%s: the store changed", name)
		}
		for f, b := range before {
			if after[f] != b {
				t.Fatalf("%s: %s changed", name, f)
			}
		}
	}
	if _, err := Run(Options{Projects: t.TempDir(), Store: store, DryRun: true, Now: now}); !errors.Is(err, ErrNoMessages) {
		t.Fatalf("a dry run finds nothing too: %v", err)
	}
}

func TestSessionNamesAreMergedAcrossRuns(t *testing.T) {
	store := storeDir(t)
	first := project(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl": {assistant(sessA, "a1", opus, at, usage{in: 1, out: 1}), titleLine("ai-title", sessA, "Old name of A")},
		"proj-a/" + sessB + ".jsonl": {assistant(sessB, "b1", opus, at.Add(-48*time.Hour), usage{in: 1, out: 1}), titleLine("custom-title", sessB, "Name of B")},
	})
	run(t, first, store, false)
	// the second run sees only A, with a new name; B's transcript is gone
	second := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "a2", opus, at.Add(time.Hour), usage{in: 1, out: 1}), titleLine("custom-title", sessA, "New name of A")}})
	run(t, second, store, false)
	s := summary(store)
	if title, kind := titleOf(s, sessA); title != "New name of A" || kind != "custom" {
		t.Fatalf("a name from this run replaces the old one: %q %q", title, kind)
	}
	if title, kind := titleOf(s, sessB); title != "Name of B" || kind != "custom" {
		t.Fatalf("a session not seen this run keeps its name: %q %q", title, kind)
	}
}

func TestSessionsThatFoldIntoOtherGetNoTitleAndTheStoreHoldsNoRawTitle(t *testing.T) {
	raw := "  Name\x1b[31m\u202e " + strings.Repeat("x", 300)
	projects := project(t, map[string][]string{
		"proj-a/bad id!.jsonl":       {assistant("x", "m1", opus, at, usage{in: 1, out: 1}), titleLine("custom-title", "bad id!", "First folded name")},
		"proj-a/bad id?.jsonl":       {assistant("y", "m2", opus, at, usage{in: 1, out: 1}), titleLine("custom-title", "bad id?", "Second folded name")},
		"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m3", opus, at, usage{in: 1, out: 1}), titleLine("custom-title", sessA, raw)},
	})
	for range 3 { // the map order of the names must not matter
		store := storeDir(t)
		rep := run(t, projects, store, false)
		if rep.Titled != 1 {
			t.Fatalf("only the real session is named: %+v", rep)
		}
		b, err := os.ReadFile(filepath.Join(store, transcripts.TitlesFile))
		if err != nil || strings.Contains(string(b), "folded") || strings.ContainsAny(string(b), "\u202e\x1b") || strings.Contains(string(b), `\u001b`) {
			t.Fatalf("%v %s", err, b)
		}
		if title, _ := titleOf(summary(store), history.OtherSession); title != "" {
			t.Fatalf("(other) has no title: %q", title)
		}
	}
}

func TestADryRunWritesNothingAndPrintsOnlyCounts(t *testing.T) {
	long := `{"type":"user","timestamp":"2026-10-04T11:00:00Z","message":{"content":"` + strings.Repeat("z", MaxLine+10) + `"}}`
	projects := project(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {
		assistant(sessA, "m1", opus, at, usage{in: 10, out: 1}),
		long,
		titleLine("custom-title", sessA, "Made-up name that must not be printed"),
		"not json at all",
		assistant(sessA, "m2", opus, at.Add(24*time.Hour), usage{in: 20, out: 2}),
	}})
	store := storeDir(t)
	rep := run(t, projects, store, true)
	if _, err := os.Stat(store); err == nil {
		t.Fatal("a dry run must not create the store")
	}
	if rep.Rows != 2 || rep.Sessions != 1 || rep.SkippedLines != 2 || rep.Messages != 2 || rep.Titled != 1 || !rep.DryRun {
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
	for _, leak := range []string{secret, sessA, "proj-a", "opus", "Made-up name"} {
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
