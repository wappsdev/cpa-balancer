package config

import (
	"strings"
	"testing"
)

const full = `
enabled: true
priority: 0
mode: shadow
state_path: /tmp/x/state.json
accounts:
  - auth_file: claude-w.json
    reserve: false
  - auth_file: claude-g.json
    reserve: true
buckets:
  main: [claude-opus-5-5]
  fable: [claude-fable-5-1]
thresholds:
  session_cap: 97
anchor:
  enabled: false
`

func TestDefaultsWhenOnlyHostKeysAreGiven(t *testing.T) {
	cfg, err := Parse([]byte("enabled: true\npriority: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "apply" || cfg.Thresholds.SessionCap != 98 || cfg.Thresholds.ReserveCapFar != 85 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if !cfg.Anchor.Enabled || cfg.Anchor.Model != "claude-haiku-4-5-20251001" || cfg.Anchor.FallbackModel != "claude-sonnet-5-5" {
		t.Fatalf("anchor defaults not applied: %+v", cfg.Anchor)
	}
	if strings.HasPrefix(cfg.StatePath, "~") || !strings.HasSuffix(cfg.StatePath, "/.cli-proxy-api/cpa-balancer/state.json") {
		t.Fatalf("state path not expanded: %s", cfg.StatePath)
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "shadow" || cfg.StatePath != "/tmp/x/state.json" || len(cfg.Accounts) != 2 || !cfg.Accounts[1].Reserve {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if cfg.Thresholds.SessionCap != 97 || cfg.Thresholds.FinalCap != 99 || cfg.Anchor.Enabled {
		t.Fatalf("partial overrides wrong: %+v %+v", cfg.Thresholds, cfg.Anchor)
	}
	if !cfg.Managed("claude-g.json") || cfg.Managed("codex-x.json") {
		t.Fatal("Managed wrong")
	}
}

func TestRejectsUnknownMode(t *testing.T) {
	if _, err := Parse([]byte("mode: yolo\n")); err == nil {
		t.Fatal("expected an error")
	}
}
