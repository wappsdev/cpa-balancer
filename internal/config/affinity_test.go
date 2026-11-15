package config

import (
	"fmt"
	"strings"
	"testing"
)

const sid = "11111111-2222-3333-4444-555555555555"

func TestAffinityDefaultsToClaudeCodesProjectsDirectory(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(cfg.Affinity.ProjectsDir, "~") || !strings.HasSuffix(cfg.Affinity.ProjectsDir, "/.claude/projects") {
		t.Fatalf("projects dir: %q", cfg.Affinity.ProjectsDir)
	}
	if len(cfg.Affinity.FolderPins) != 0 || len(cfg.Affinity.SessionPins) != 0 {
		t.Fatalf("no pins by default: %+v", cfg.Affinity)
	}
}

func TestAffinityReadsItsDirectoryAndPinsFromTheConfig(t *testing.T) {
	cfg, err := Parse([]byte(minimal + fmt.Sprintf(`affinity:
  projects_dir: /data/projects
  folder_pins: {-work-alpha: claude-w.json}
  session_pins: {%s: claude-w.json}
`, sid)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Affinity.ProjectsDir != "/data/projects" || cfg.Affinity.FolderPins["-work-alpha"] != "claude-w.json" || cfg.Affinity.SessionPins[sid] != "claude-w.json" {
		t.Fatalf("%+v", cfg.Affinity)
	}
}

func TestAffinityRejectsWhatItCannotUse(t *testing.T) {
	many := func(n int, key func(int) string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "%q: claude-w.json, ", key(i))
		}
		return b.String()
	}
	for name, block := range map[string]string{
		"a folder pin on an account that is not configured":     "folder_pins: {-work-alpha: claude-other.json}",
		"a session pin on an account that is not configured":    fmt.Sprintf("session_pins: {%s: claude-other.json}", sid),
		"a folder that is a path":                               "folder_pins: {\"a/b\": claude-w.json}",
		"an empty folder name":                                  "folder_pins: {\"\": claude-w.json}",
		"a session id that is not plain":                        "session_pins: {\"a b\": claude-w.json}",
		"a subagent id is not pinned on its own, its parent is": "session_pins: {\"" + sid + "/ab12\": claude-w.json}",
		"too many folder pins":                                  "folder_pins: {" + many(MaxFolderPins+1, func(i int) string { return fmt.Sprintf("-work-%d", i) }) + "}",
		"too many session pins":                                 "session_pins: {" + many(MaxSessionPins+1, func(i int) string { return fmt.Sprintf("s%d", i) }) + "}",
		"an empty projects dir":                                 "projects_dir: \"\"",
	} {
		if _, err := Parse([]byte(minimal + "affinity:\n  " + block + "\n")); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}
