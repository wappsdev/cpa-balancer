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
    reserve: "off"
  - auth_file: claude-g.json
    reserve: custom
    reserve_steps: [{cap: 50, hours: 120}, {cap: 70, hours: 48}]
buckets:
  main: [claude-opus-5-5]
  fable: [claude-fable-5-1]
thresholds:
  session_cap: 97
anchor:
  enabled: false
`

const minimal = "accounts: [{auth_file: claude-w.json}]\nbuckets: {main: [claude-opus-5-5]}\n"

func TestDefaultsWhenOnlyRequiredKeysAreGiven(t *testing.T) {
	cfg, err := Parse([]byte("enabled: true\npriority: 0\n" + minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paused {
		t.Fatal("paused must default to false")
	}
	if cfg.Mode != "apply" || cfg.Thresholds.SessionCap != 98 ||
		len(cfg.Thresholds.Reserve) != 2 || cfg.Thresholds.Reserve[0] != (ReserveStep{Cap: 85, Hours: 48}) || cfg.Thresholds.Reserve[1] != (ReserveStep{Cap: 90, Hours: 12}) {
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
	if cfg.Mode != "shadow" || cfg.StatePath != "/tmp/x/state.json" || len(cfg.Accounts) != 2 || cfg.Accounts[1].Reserve != ReserveCustom || len(cfg.Accounts[1].ReserveSteps) != 2 || cfg.Accounts[1].ReserveSteps[1] != (ReserveStep{Cap: 70, Hours: 48}) {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if cfg.Thresholds.SessionCap != 97 || cfg.Thresholds.FinalCap != 99 || cfg.Anchor.Enabled {
		t.Fatalf("partial overrides wrong: %+v %+v", cfg.Thresholds, cfg.Anchor)
	}
	if !cfg.Managed("claude-g.json") || cfg.Managed("codex-x.json") {
		t.Fatal("Managed wrong")
	}
}

func TestValidation(t *testing.T) {
	bad := map[string]string{
		"mode":             "mode: yolo\n" + minimal,
		"cap range":        "thresholds: {session_cap: 120}\n" + minimal,
		"step above final": "thresholds: {final_cap: 90, reserve: [{cap: 95, hours: 24}]}\n" + minimal,
		"step at a week":   "thresholds: {reserve: [{cap: 50, hours: 168}]}\n" + minimal,
		"same hours twice": "thresholds: {reserve: [{cap: 50, hours: 24}, {cap: 60, hours: 24}]}\n" + minimal,
		"no default step":  "thresholds: {reserve: []}\n" + minimal,
		"dwell":            "thresholds: {min_dwell_minutes: -1}\n" + minimal,
		"stale":            "thresholds: {stale_hours: 0}\n" + minimal,
		"no accounts":      "buckets: {main: [claude-opus-5-5]}\n",
		"no main models":   "accounts: [{auth_file: claude-w.json}]\n",
		"duplicate":        "accounts: [{auth_file: a.json}, {auth_file: a.json}]\nbuckets: {main: [m]}\n",
	}
	for name, raw := range bad {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if cfg, err := Parse([]byte("paused: true\n" + minimal)); err != nil || !cfg.Paused {
		t.Fatalf("paused: %+v %v", cfg, err)
	}
}

func TestParsesExtraUsage(t *testing.T) {
	cfg, err := Parse([]byte("accounts:\n  - auth_file: a.json\n    extra_usage: true\n  - auth_file: b.json\nbuckets:\n  main: [m]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Accounts[0].ExtraUsage || cfg.Accounts[1].ExtraUsage {
		t.Fatalf("got %+v", cfg.Accounts)
	}
}

func TestAccountNames(t *testing.T) {
	cfg := func(accounts ...Account) Config {
		c := Default()
		c.Accounts = accounts
		c.Buckets.Main = []string{"m"}
		return c
	}
	// a label wins; without one, the e-mail's local part, with its domain when two local parts repeat
	names := cfg(
		Account{AuthFile: "claude-1a-ali@sirket.com.json", Label: "Work"},
		Account{AuthFile: "claude-2b-ali@gmail.com.json"},
		Account{AuthFile: "claude-3c-veli@gmail.com.json"},
		Account{AuthFile: "claude-4d-me@example.com.json"},
		Account{AuthFile: "claude-5e-me@gmail.com.json"},
	).Names()
	want := map[string]string{
		"claude-1a-ali@sirket.com.json": "Work", "claude-2b-ali@gmail.com.json": "ali",
		"claude-3c-veli@gmail.com.json": "veli", "claude-4d-me@example.com.json": "me-wapps",
		"claude-5e-me@gmail.com.json": "me-gmail",
	}
	for f, n := range want {
		if names[f] != n {
			t.Fatalf("%s: got %q, want %q (all %v)", f, names[f], n, names)
		}
	}
	// a derived name that equals a label, or two equal labels, is refused
	if err := cfg(Account{AuthFile: "claude-1-a@x.com.json", Label: "b"}, Account{AuthFile: "claude-2-b@y.com.json"}).Validate(); err == nil {
		t.Fatal("two accounts named b must be refused")
	}
	if err := cfg(Account{AuthFile: "claude-1-a@x.com.json", Label: "bad name!"}).Validate(); err == nil {
		t.Fatal("a label must be one word of letters, digits, - or _")
	}
	if err := cfg(Account{AuthFile: "claude-1-a@x.com.json", Label: "personal-2"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReserveSettings(t *testing.T) {
	parse := func(account string) (Config, error) {
		return Parse([]byte("accounts:\n  - " + account + "\nbuckets: {main: [m]}\n"))
	}
	if c, err := parse("auth_file: a.json"); err != nil || c.Accounts[0].Reserve != ReserveOff {
		t.Fatalf("no reserve given must mean off: %+v %v", c.Accounts, err)
	}
	// a thresholds reserve replaces the default steps rather than adding to them
	c, err := Parse([]byte("thresholds: {reserve: [{cap: 70, hours: 72}]}\naccounts: [{auth_file: a.json}]\nbuckets: {main: [m]}\n"))
	if err != nil || len(c.Thresholds.Reserve) != 1 || c.Thresholds.Reserve[0] != (ReserveStep{Cap: 70, Hours: 72}) {
		t.Fatalf("thresholds reserve: %+v %v", c.Thresholds.Reserve, err)
	}
	for _, bad := range []string{
		"{auth_file: a.json, reserve: custom}",                                         // a custom reserve needs steps
		"{auth_file: a.json, reserve: custom, reserve_steps: [{cap: 120, hours: 24}]}", // above the final cap
		"{auth_file: a.json, reserve: custom, reserve_steps: [{cap: 50, hours: 200}]}", // more than a week
		"{auth_file: a.json, reserve: custom, reserve_steps: [{cap: 0, hours: 24}]}",   // no cap
		"{auth_file: a.json, reserve: sometimes}",
		"{auth_file: a.json, reserve: default, reserve_steps: [{cap: 50, hours: 24}]}", // steps go only with custom
	} {
		if _, err := parse(bad); err == nil {
			t.Fatalf("%s must be refused", bad)
		}
	}
}

func TestFiveHourStepsPerAccount(t *testing.T) {
	c, err := Parse([]byte("accounts: [{auth_file: a.json, session_steps: [{cap: 60, hours: 2}, {cap: 90, hours: 0}]}]\nbuckets: {main: [m]}\n"))
	if err != nil || len(c.Accounts[0].SessionSteps) != 2 || c.Accounts[0].SessionSteps[1] != (ReserveStep{Cap: 90, Hours: 0}) {
		t.Fatalf("%+v %v", c.Accounts, err)
	}
	for _, bad := range []string{
		"[{cap: 60, hours: 5}]",  // a 5-hour window has no step 5 hours before its reset
		"[{cap: 120, hours: 1}]", // above 100
	} {
		if _, err := Parse([]byte("accounts: [{auth_file: a.json, session_steps: " + bad + "}]\nbuckets: {main: [m]}\n")); err == nil {
			t.Fatalf("%s must be refused", bad)
		}
	}
	// a weekly step may run to the reset itself (0 hours): one cap all week
	if _, err := Parse([]byte("accounts: [{auth_file: a.json, reserve: custom, reserve_steps: [{cap: 60, hours: 0}]}]\nbuckets: {main: [m]}\n")); err != nil {
		t.Fatal(err)
	}
}
