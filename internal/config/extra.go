package config

import (
	"fmt"
	"slices"
	"strings"
)

// ExtraModel is a Claude model that CPA's catalog does not have, served by the built-in claude
// executor through only the accounts given. The plugin registers it with CPA for the claude
// provider, and the scheduler sends a request for it to one of these accounts or refuses it: it
// never falls back to another account, which would answer 404 or spend its own quota.
//
// The capabilities are set here because a plugin cannot read CPA's model registry.
type ExtraModel struct {
	ID          string `yaml:"id" json:"id"`
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	// Accounts are the auth files allowed to serve the model, in the order they are tried.
	Accounts            []string `yaml:"accounts" json:"accounts"`
	ContextLength       int64    `yaml:"context_length" json:"context_length"`
	MaxCompletionTokens int64    `yaml:"max_completion_tokens" json:"max_completion_tokens"`
	// ThinkingLevels are the reasoning efforts the model takes; any means adaptive thinking (no
	// fixed budget, and it cannot be turned off). None means the model does not think.
	ThinkingLevels []string `yaml:"thinking_levels,omitempty" json:"thinking_levels,omitempty"`
}

// ExtraModel finds the extra model a request names. CPA hands the scheduler the model as the
// client sent it, so a thinking suffix ("(high)"), a context marker ("[1m]") and an auth prefix
// ("p/") are dropped first: an exact match would miss them and let the request go to any account.
func (c Config) ExtraModel(requested string) (ExtraModel, bool) {
	name := requested
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexAny(name, "(["); i >= 0 {
		name = name[:i]
	}
	for _, m := range c.ExtraModels {
		if m.ID == name {
			return m, true
		}
	}
	return ExtraModel{}, false
}

func (c Config) validateExtraModels() error {
	seen := map[string]bool{}
	for _, m := range c.ExtraModels {
		switch {
		case m.ID == "" || strings.ContainsAny(m.ID, "()[]/ \t"):
			return fmt.Errorf("extra_models: an id must be a plain model name, got %q", m.ID)
		case seen[m.ID]:
			return fmt.Errorf("extra_models: %s is listed twice", m.ID)
		case slices.Contains(c.Buckets.Main, m.ID) || slices.Contains(c.Buckets.Fable, m.ID):
			return fmt.Errorf("extra_models: %s is in a bucket; an extra model is pinned, not balanced", m.ID)
		case len(m.Accounts) == 0:
			return fmt.Errorf("extra_models: %s needs at least one account", m.ID)
		case m.ContextLength <= 0:
			return fmt.Errorf("extra_models: %s needs a positive context_length", m.ID)
		case m.MaxCompletionTokens <= 0:
			return fmt.Errorf("extra_models: %s needs a positive max_completion_tokens", m.ID)
		case slices.Contains(m.ThinkingLevels, ""):
			return fmt.Errorf("extra_models: %s has an empty entry in thinking_levels", m.ID)
		}
		seen[m.ID] = true
		accounts := map[string]bool{}
		for _, a := range m.Accounts {
			if !c.Managed(a) {
				return fmt.Errorf("extra_models: %s names an account that is not configured", m.ID)
			}
			if accounts[a] {
				return fmt.Errorf("extra_models: %s names an account twice", m.ID)
			}
			accounts[a] = true
		}
	}
	return nil
}
