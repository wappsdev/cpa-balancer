package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunNeedsBothDirectoriesAndAnExistingStateDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{}, {"--projects", dir}, {"--state", dir}, {"--projects", dir, "--state", filepath.Join(dir, "missing")}} {
		if err := run(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestAnErrorNeverNamesAPath(t *testing.T) {
	state := t.TempDir()
	missing := filepath.Join(t.TempDir(), "private-project-name")
	err := run([]string{"--projects", missing, "--state", state})
	if err == nil {
		t.Fatal("expected an error")
	}
	if text := plain(err); strings.Contains(text, "private-project-name") || strings.Contains(text, state) {
		t.Fatalf("the printed error names a path: %q", text)
	}
	if msg := run([]string{"--projects", state, "--state", missing}); msg == nil || strings.Contains(msg.Error(), "private-project-name") {
		t.Fatalf("%v", msg)
	}
}

func TestDryRunReadsTranscriptsAndTouchesNothing(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	at := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	transcript := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":2}}}`+"\n", at)
	if err := os.MkdirAll(filepath.Join(projects, "project-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "project-one", "11111111-aaaa-bbbb-cccc-000000000001.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--projects", projects, "--state", state, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatalf("a dry run wrote %d entries under the state directory", len(entries))
	}
	if err := run([]string{"--projects", projects, "--state", state}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "transcripts")); len(entries) != 1 {
		t.Fatalf("the import wrote %d files", len(entries))
	}
}

func TestARealRunLeavesTheLiveHistoryByteForByteAlone(t *testing.T) {
	projects, state := t.TempDir(), t.TempDir()
	at := time.Now().UTC().Add(-48 * time.Hour)
	transcript := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":2}}}`+"\n", at.Format(time.RFC3339))
	if err := os.MkdirAll(filepath.Join(projects, "project-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "project-one", "11111111-aaaa-bbbb-cccc-000000000001.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(state, "history", at.Format("2006-01-02")+".jsonl")
	slot := fmt.Sprintf(`{"t":%q,"accounts":{"a":{"models":{"m":{"requests":5}}}}}`+"\n", at.Format(time.RFC3339))
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte(slot), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--projects", projects, "--state", state}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != slot {
		t.Fatalf("the live history changed: %q %v", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "history")); len(entries) != 1 {
		t.Fatalf("the import added %d files to the history", len(entries))
	}
}

func TestTheRetiredFableFlagIsGone(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"--projects", dir, "--state", dir, "--fable", "x"}); err == nil {
		t.Fatal("--fable is not a flag any more")
	}
}
