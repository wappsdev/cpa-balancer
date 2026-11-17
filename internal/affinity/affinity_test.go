package affinity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sid = "11111111-2222-3333-4444-555555555555"

func headers(kv ...string) map[string][]string {
	h := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		h[kv[i]] = append(h[kv[i]], kv[i+1])
	}
	return h
}

func TestSessionIDFromTheClaudeCodeHeaders(t *testing.T) {
	cases := []struct {
		name     string
		h        map[string][]string
		id, root string
	}{
		{"main session", headers("X-Claude-Code-Session-Id", sid), sid, sid},
		{"header names are matched in any case", headers("x-claude-code-session-id", sid), sid, sid},
		{"an agent id makes a subagent keyed under its parent", headers("X-Claude-Code-Session-Id", sid, "X-Claude-Code-Agent-Id", "ab12"), sid + "/ab12", sid},
		{"the agent id main is the main session", headers("X-Claude-Code-Session-Id", sid, "X-Claude-Code-Agent-Id", "main"), sid, sid},
		{"no header is no session", headers("Content-Type", "application/json"), "", ""},
		{"a nil map is no session", nil, "", ""},
		{"a session id that is not one path element is no session", headers("X-Claude-Code-Session-Id", "../../etc/passwd"), "", ""},
		{"a hidden name is no session", headers("X-Claude-Code-Session-Id", ".hidden"), "", ""},
		{"a colon would break the key", headers("X-Claude-Code-Session-Id", "a:agent:b"), "", ""},
		{"a space is not plain", headers("X-Claude-Code-Session-Id", "a b"), "", ""},
		{"too long", headers("X-Claude-Code-Session-Id", strings.Repeat("a", 200)), "", ""},
		{"a bad agent id keeps the project: the agent id never reaches a path", headers("X-Claude-Code-Session-Id", sid, "X-Claude-Code-Agent-Id", "a/b"), sid, sid},
	}
	for _, c := range cases {
		id, root := SessionID(c.h)
		if id != c.id || root != c.root {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, id, root, c.id, c.root)
		}
	}
}

func transcript(t *testing.T, dir, folder, session string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, folder, session+".jsonl"), []byte("not read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectsFindsTheFolderThatHoldsTheTranscriptByName(t *testing.T) {
	dir := t.TempDir()
	transcript(t, dir, "-work-alpha", sid)
	transcript(t, dir, "-work-beta", "99999999-2222-3333-4444-555555555555")
	// a transcript's own subagent directory is not a session file
	if err := os.MkdirAll(filepath.Join(dir, "-work-beta", sid, "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := NewProjects(dir, time.Now)
	if folder, ok := p.Folder(sid); !ok || folder != "-work-alpha" {
		t.Fatalf("got %q %v", folder, ok)
	}
	if _, ok := p.Folder("00000000-2222-3333-4444-555555555555"); ok {
		t.Fatal("an unknown session has no project")
	}
}

func TestProjectsNeverOpensATranscript(t *testing.T) {
	dir := t.TempDir()
	transcript(t, dir, "-work-alpha", sid)
	if err := os.Chmod(filepath.Join(dir, "-work-alpha", sid+".jsonl"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "-work-alpha", sid+".jsonl"), 0o600) })
	if folder, ok := NewProjects(dir, time.Now).Folder(sid); !ok || folder != "-work-alpha" {
		t.Fatalf("a name lookup must not need to read the file: %q %v", folder, ok)
	}
}

func TestProjectsCachesAHitAndRetriesAMissAfterAShortWhile(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	p := NewProjects(dir, func() time.Time { return clock })
	if _, ok := p.Folder(sid); ok {
		t.Fatal("nothing there yet")
	}
	transcript(t, dir, "-work-alpha", sid) // Claude Code writes the file after the first request
	if _, ok := p.Folder(sid); ok {
		t.Fatal("a recent miss is remembered, so a burst of requests does not scan the directory each time")
	}
	clock = clock.Add(missTTL + time.Second)
	if folder, ok := p.Folder(sid); !ok || folder != "-work-alpha" {
		t.Fatalf("after the wait the file is found: %q %v", folder, ok)
	}
	if err := os.RemoveAll(filepath.Join(dir, "-work-alpha")); err != nil {
		t.Fatal(err)
	}
	if folder, ok := p.Folder(sid); !ok || folder != "-work-alpha" {
		t.Fatalf("a hit is cached: %q %v", folder, ok)
	}
}

// touch sets the modification time of a session's transcript in a folder.
func touch(t *testing.T, dir, folder, session string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(dir, folder, session+".jsonl"), at, at); err != nil {
		t.Fatal(err)
	}
}

func TestProjectsChoosesTheFolderWhoseTranscriptWasWrittenLast(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	// the folder that sorts first holds an old copy; Claude Code resumed the session from the other
	transcript(t, dir, "-work-alpha", sid)
	transcript(t, dir, "-work-beta", sid)
	touch(t, dir, "-work-alpha", sid, clock.Add(-10*24*time.Hour))
	touch(t, dir, "-work-beta", sid, clock)
	p := NewProjects(dir, func() time.Time { return clock })
	if folder, ok := p.Folder(sid); !ok || folder != "-work-beta" {
		t.Fatalf("the live transcript decides, not the first folder: %q %v", folder, ok)
	}
}

func TestProjectsFollowsAResumedSessionAfterTheRecheckTime(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	transcript(t, dir, "-work-alpha", sid)
	transcript(t, dir, "-work-beta", sid)
	touch(t, dir, "-work-alpha", sid, clock)
	touch(t, dir, "-work-beta", sid, clock.Add(-time.Hour))
	p := NewProjects(dir, func() time.Time { return clock })
	if folder, _ := p.Folder(sid); folder != "-work-alpha" {
		t.Fatalf("setup: %q", folder)
	}
	touch(t, dir, "-work-beta", sid, clock.Add(time.Second)) // resumed from the other folder
	clock = clock.Add(recheckTTL / 2)
	if folder, _ := p.Folder(sid); folder != "-work-alpha" {
		t.Fatalf("within the time the answer is the cached one: %q", folder)
	}
	clock = clock.Add(recheckTTL)
	if folder, ok := p.Folder(sid); !ok || folder != "-work-beta" {
		t.Fatalf("after the time the session moves to its current folder: %q %v", folder, ok)
	}
}

func TestProjectsKeepsAnAnswerWhoseTranscriptStaysInOneFolder(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	transcript(t, dir, "-work-alpha", sid)
	p := NewProjects(dir, func() time.Time { return clock })
	for i := 0; i < 3; i++ {
		if folder, ok := p.Folder(sid); !ok || folder != "-work-alpha" {
			t.Fatalf("%d: %q %v", i, folder, ok)
		}
		clock = clock.Add(recheckTTL + time.Second)
	}
}

func TestProjectsCachesAreBounded(t *testing.T) {
	dir := t.TempDir()
	p := NewProjects(dir, time.Now)
	for i := 0; i < 3*maxCached; i++ {
		p.Folder(fmt.Sprintf("missing-%d", i))
	}
	if n := p.size(); n > maxCached {
		t.Fatalf("cache grew to %d", n)
	}
}

func TestProjectsWithoutADirectoryHasNoProject(t *testing.T) {
	if _, ok := NewProjects(filepath.Join(t.TempDir(), "absent"), time.Now).Folder(sid); ok {
		t.Fatal("a missing projects directory is no project")
	}
	if _, ok := NewProjects("", time.Now).Folder(sid); ok {
		t.Fatal("an empty projects directory setting is no project")
	}
}

func TestValidProject(t *testing.T) {
	for name, want := range map[string]bool{
		"-work-alpha": true, "My Project": true, "a.b": true, "-work-é": true,
		"": false, ".": false, "..": false, "a/b": false, "a\x00b": false, "a\nb": false, strings.Repeat("a", 256): false, "\xff": false,
	} {
		if got := ValidProject(name); got != want {
			t.Errorf("ValidProject(%q) = %v, want %v", name, got, want)
		}
	}
}
