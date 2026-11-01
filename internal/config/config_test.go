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
    reserve_cap: 60
    reserve_hours: 48
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
	if cfg.Mode != "shadow" || cfg.StatePath != "/tmp/x/state.json" || len(cfg.Accounts) != 2 || cfg.Accounts[1].Reserve != ReserveCustom || cfg.Accounts[1].ReserveCap != 60 || cfg.Accounts[1].ReserveHours != 48 {
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
		"mode":           "mode: yolo\n" + minimal,
		"cap range":      "thresholds: {session_cap: 120}\n" + minimal,
		"cap order":      "thresholds: {reserve_cap_far: 95}\n" + minimal,
		"dwell":          "thresholds: {min_dwell_minutes: -1}\n" + minimal,
		"stale":          "thresholds: {stale_hours: 0}\n" + minimal,
		"no accounts":    "buckets: {main: [claude-opus-5-5]}\n",
		"no main models": "accounts: [{auth_file: claude-w.json}]\n",
		"duplicate":      "accounts: [{auth_file: a.json}, {auth_file: a.json}]\nbuckets: {main: [m]}\n",
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
	for _, bad := range []string{
		"{auth_file: a.json, reserve: custom, reserve_hours: 24}",                   // a custom reserve needs its cap
		"{auth_file: a.json, reserve: custom, reserve_cap: 50}",                     // and its release time
		"{auth_file: a.json, reserve: custom, reserve_cap: 120, reserve_hours: 24}", // above the final cap
		"{auth_file: a.json, reserve: custom, reserve_cap: 50, reserve_hours: 200}", // more than a week
		"{auth_file: a.json, reserve: sometimes}",
		"{auth_file: a.json, reserve: default, reserve_cap: 50}", // a cap only goes with custom
		"{auth_file: a.json, reserve: off, reserve_hours: 12}",
	} {
		if _, err := parse(bad); err == nil {
			t.Fatalf("%s must be refused", bad)
		}
	}
}
