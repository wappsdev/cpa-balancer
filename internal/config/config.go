// Package config parses the plugin's block from plugins.configs.cpa-balancer.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Account struct {
	AuthFile string `yaml:"auth_file" json:"auth_file"`
	// Label names the account in the Management Center, the HUD and its commands; empty means a
	// name taken from the e-mail (Names).
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// Reserve holds quota back until the weekly reset is near: off, default (the thresholds'
	// steps) or custom (ReserveSteps, this account's own).
	Reserve      string        `yaml:"reserve" json:"reserve"` // empty is off
	ReserveSteps []ReserveStep `yaml:"reserve_steps,omitempty" json:"reserve_steps,omitempty"`
	// SessionCap is where this account's 5-hour window switches it away; 0 is the thresholds' one.
	SessionCap float64 `yaml:"session_cap,omitempty" json:"session_cap,omitempty"`
	// ExtraUsage: the account has extra-usage credits on, so it keeps answering past its plan
	// limits. It is used only when no account has plan quota left.
	ExtraUsage bool `yaml:"extra_usage" json:"extra_usage"`
}

const (
	ReserveOff     = "off"
	ReserveDefault = "default"
	ReserveCustom  = "custom"
)

// ReserveStep caps an account's weekly use at Cap until Hours before its reset. Of a list, the
// step with the most hours that have not yet run out is in force; after the last, the final cap.
type ReserveStep struct {
	Cap   float64 `yaml:"cap" json:"cap"`
	Hours float64 `yaml:"hours" json:"hours"`
}

type Thresholds struct {
	// Reserve is the default reserve's steps, for every account set to default.
	Reserve         []ReserveStep `yaml:"reserve" json:"reserve"`
	FinalCap        float64       `yaml:"final_cap" json:"final_cap"`
	SessionCap      float64       `yaml:"session_cap" json:"session_cap"`
	MinDwellMinutes int           `yaml:"min_dwell_minutes" json:"min_dwell_minutes"`
	StaleHours      float64       `yaml:"stale_hours" json:"stale_hours"`
}

func (t Thresholds) MinDwell() time.Duration { return time.Duration(t.MinDwellMinutes) * time.Minute }
func (t Thresholds) Stale() time.Duration    { return time.Duration(t.StaleHours * float64(time.Hour)) }

type Anchor struct {
	Enabled       bool   `yaml:"enabled" json:"enabled"`
	Model         string `yaml:"model" json:"model"`
	FallbackModel string `yaml:"fallback_model" json:"fallback_model"`
}

type Buckets struct {
	Main  []string `yaml:"main" json:"main"`
	Fable []string `yaml:"fable" json:"fable"`
}

type Config struct {
	Enabled    bool       `yaml:"enabled" json:"enabled"`
	Paused     bool       `yaml:"paused" json:"paused"` // stay loaded, delegate every pick, no probes or anchors
	Mode       string     `yaml:"mode" json:"mode"`
	StatePath  string     `yaml:"state_path" json:"state_path"`
	Accounts   []Account  `yaml:"accounts" json:"accounts"`
	Buckets    Buckets    `yaml:"buckets" json:"buckets"`
	Thresholds Thresholds `yaml:"thresholds" json:"thresholds"`
	Anchor     Anchor     `yaml:"anchor" json:"anchor"`
}

func Default() Config {
	return Config{
		Enabled:   true,
		Mode:      "apply",
		StatePath: "~/.cli-proxy-api/cpa-balancer/state.json",
		Thresholds: Thresholds{
			Reserve:  []ReserveStep{{Cap: 85, Hours: 48}, {Cap: 90, Hours: 12}},
			FinalCap: 99, SessionCap: 98,
			MinDwellMinutes: 60, StaleHours: 6,
		},
		Anchor: Anchor{Enabled: true, Model: "claude-haiku-4-5-20251001", FallbackModel: "claude-sonnet-5-5"},
	}
}

// Parse overlays the YAML block on the defaults. Unknown keys (CPA adds `priority`) are ignored.
func Parse(raw []byte) (Config, error) {
	cfg := Default()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	for i := range cfg.Accounts {
		if cfg.Accounts[i].Reserve == "" {
			cfg.Accounts[i].Reserve = ReserveOff
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if strings.HasPrefix(cfg.StatePath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolve state_path: %w", err)
		}
		cfg.StatePath = filepath.Join(home, cfg.StatePath[2:])
	}
	return cfg, nil
}

// Validate checks the rules the settings form also enforces.
func (c Config) Validate() error {
	if c.Mode != "apply" && c.Mode != "shadow" {
		return fmt.Errorf("mode must be apply or shadow, got %q", c.Mode)
	}
	th := c.Thresholds
	for _, p := range []struct {
		name  string
		value float64
	}{{"final_cap", th.FinalCap}, {"session_cap", th.SessionCap}} {
		if p.value < 0 || p.value > 100 {
			return fmt.Errorf("%s must be within 0-100, got %v", p.name, p.value)
		}
	}
	if err := validSteps(th.Reserve, th.FinalCap); err != nil {
		return fmt.Errorf("default reserve: %w", err)
	}
	if th.MinDwellMinutes < 0 {
		return fmt.Errorf("min_dwell_minutes must not be negative")
	}
	if th.StaleHours <= 0 {
		return fmt.Errorf("stale_hours must be positive")
	}
	if len(c.Accounts) == 0 {
		return fmt.Errorf("at least one account is required")
	}
	if len(c.Buckets.Main) == 0 {
		return fmt.Errorf("the main bucket needs at least one model")
	}
	seen := map[string]bool{}
	for _, a := range c.Accounts {
		if a.AuthFile == "" || seen[a.AuthFile] {
			return fmt.Errorf("account auth files must be unique and non-empty, got %q", a.AuthFile)
		}
		seen[a.AuthFile] = true
		switch {
		case a.Reserve != "" && a.Reserve != ReserveOff && a.Reserve != ReserveDefault && a.Reserve != ReserveCustom:
			return fmt.Errorf("%s: reserve must be off, default or custom, got %q", a.AuthFile, a.Reserve)
		case a.Reserve == ReserveCustom:
			if err := validSteps(a.ReserveSteps, th.FinalCap); err != nil {
				return fmt.Errorf("%s: %w", a.AuthFile, err)
			}
		case len(a.ReserveSteps) > 0:
			return fmt.Errorf("%s: reserve_steps go only with a custom reserve", a.AuthFile)
		}
		if a.SessionCap < 0 || a.SessionCap > 100 {
			return fmt.Errorf("%s: session_cap must be within 0-100, got %v", a.AuthFile, a.SessionCap)
		}
		if a.Label != "" && !labelPattern.MatchString(a.Label) {
			return fmt.Errorf("label %q must be one word of up to 20 letters, digits, - or _", a.Label)
		}
	}
	taken := map[string]string{}
	for file, name := range c.Names() {
		if other, ok := taken[strings.ToLower(name)]; ok {
			return fmt.Errorf("accounts %s and %s are both named %q; give one a label", other, file, name)
		}
		taken[strings.ToLower(name)] = file
	}
	return nil
}

// validSteps: at least one step, each capping above 0 and at most the final cap, until between 0
// and 168 hours before the reset, no two at the same hours.
func validSteps(steps []ReserveStep, finalCap float64) error {
	if len(steps) == 0 {
		return fmt.Errorf("a reserve needs at least one step")
	}
	seen := map[float64]bool{}
	for _, s := range steps {
		switch {
		case s.Cap <= 0 || s.Cap > finalCap:
			return fmt.Errorf("a step's cap must be above 0 and at most the final cap %v, got %v", finalCap, s.Cap)
		case s.Hours <= 0 || s.Hours >= 7*24:
			return fmt.Errorf("a step ends between 0 and 168 hours before the reset, got %v", s.Hours)
		case seen[s.Hours]:
			return fmt.Errorf("two steps end %v hours before the reset", s.Hours)
		}
		seen[s.Hours] = true
	}
	return nil
}

var labelPattern = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,20}$`)

var authPrefix = regexp.MustCompile(`^(claude|codex)-[0-9a-f]+-`)

// emailOf is the e-mail inside a CPA auth file name, or the name itself.
func emailOf(authFile string) string {
	return strings.TrimSuffix(authPrefix.ReplaceAllString(authFile, ""), ".json")
}

// Names is each account's name, by auth file: its label, else the e-mail's local part (adnan for
// account-a@example.com), with the domain added when two unlabelled accounts share a local part
// (adnan-wapps, adnan-gmail). The Management Center, the HUD and their commands all use these.
func (c Config) Names() map[string]string {
	split := func(authFile string) (local, domain string) {
		e := emailOf(authFile)
		at := strings.LastIndex(e, "@")
		if at < 0 {
			return e, ""
		}
		return e[:at], strings.SplitN(e[at+1:], ".", 2)[0]
	}
	count := map[string]int{}
	for _, a := range c.Accounts {
		if a.Label == "" {
			local, _ := split(a.AuthFile)
			count[strings.ToLower(local)]++
		}
	}
	out := map[string]string{}
	for _, a := range c.Accounts {
		local, domain := split(a.AuthFile)
		switch {
		case a.Label != "":
			out[a.AuthFile] = a.Label
		case count[strings.ToLower(local)] > 1 && domain != "":
			out[a.AuthFile] = local + "-" + domain
		default:
			out[a.AuthFile] = local
		}
	}
	return out
}

func (c Config) Managed(authFile string) bool {
	for _, a := range c.Accounts {
		if a.AuthFile == authFile {
			return true
		}
	}
	return false
}
