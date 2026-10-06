// Package config parses the plugin's block from plugins.configs.cpa-balancer.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Account struct {
	AuthFile string `yaml:"auth_file" json:"auth_file"`
	Reserve  bool   `yaml:"reserve" json:"reserve"`
	// ExtraUsage: the account has extra-usage credits on, so it keeps answering past its plan
	// limits. It is used only when no account has plan quota left.
	ExtraUsage bool `yaml:"extra_usage" json:"extra_usage"`
}

type Thresholds struct {
	ReserveCapFar   float64 `yaml:"reserve_cap_far" json:"reserve_cap_far"`
	ReserveCapNear  float64 `yaml:"reserve_cap_near" json:"reserve_cap_near"`
	FinalCap        float64 `yaml:"final_cap" json:"final_cap"`
	SessionCap      float64 `yaml:"session_cap" json:"session_cap"`
	MinDwellMinutes int     `yaml:"min_dwell_minutes" json:"min_dwell_minutes"`
	StaleHours      float64 `yaml:"stale_hours" json:"stale_hours"`
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
			ReserveCapFar: 85, ReserveCapNear: 90, FinalCap: 99, SessionCap: 98,
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
	}{{"reserve_cap_far", th.ReserveCapFar}, {"reserve_cap_near", th.ReserveCapNear}, {"final_cap", th.FinalCap}, {"session_cap", th.SessionCap}} {
		if p.value < 0 || p.value > 100 {
			return fmt.Errorf("%s must be within 0-100, got %v", p.name, p.value)
		}
	}
	if th.ReserveCapFar > th.ReserveCapNear || th.ReserveCapNear > th.FinalCap {
		return fmt.Errorf("caps must satisfy reserve_cap_far <= reserve_cap_near <= final_cap, got %v / %v / %v",
			th.ReserveCapFar, th.ReserveCapNear, th.FinalCap)
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
	}
	return nil
}

func (c Config) Managed(authFile string) bool {
	for _, a := range c.Accounts {
		if a.AuthFile == authFile {
			return true
		}
	}
	return false
}
