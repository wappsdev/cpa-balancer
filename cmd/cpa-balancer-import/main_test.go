package main

import (
	"fmt"
	"os"
	"path/filepath"
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
		t.Fatalf("a dry run wrote %d entries", len(entries))
	}
	if err := run([]string{"--projects", projects, "--state", state}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "history")); len(entries) != 1 {
		t.Fatalf("the import wrote %d files", len(entries))
	}
}
