package config

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultAdvisorBeta starts the anthropic-beta value Claude Code sends with a request that carries
// the advisor tool.
const DefaultAdvisorBeta = "advisor-tool-"

// AdvisorPin pins every request that carries the advisor tool to the accounts of an extra model.
// Claude Code sends the advisor inside the main request, as a server-side tool with its own model;
// the scheduler never sees the body, only the anthropic-beta header that turns the tool on. So a
// request with that beta goes to the extra model's accounts or is refused, like the extra model
// itself, and the list of accounts allowed to use the model stays in one place.
type AdvisorPin struct {
	// Model is the id of the extra model the advisor uses; its accounts serve the request.
	Model string `yaml:"model" json:"model"`
	// BetaPrefix starts the anthropic-beta value that marks the request; DefaultAdvisorBeta if empty.
	BetaPrefix string `yaml:"beta_prefix,omitempty" json:"beta_prefix,omitempty"`
}

var betaPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// normalizeAdvisorPin fills in the default prefix and keeps it in lower case, as it is compared.
func (c *Config) normalizeAdvisorPin() {
	if p := c.AdvisorPin; p != nil {
		p.BetaPrefix = strings.ToLower(strings.TrimSpace(p.BetaPrefix))
		if p.BetaPrefix == "" {
			p.BetaPrefix = DefaultAdvisorBeta
		}
	}
}

func (c Config) validateAdvisorPin() error {
	p := c.AdvisorPin
	if p == nil {
		return nil
	}
	if !betaPattern.MatchString(p.BetaPrefix) {
		return fmt.Errorf("advisor_pin: beta_prefix must be one beta name or its start, got %q", p.BetaPrefix)
	}
	for _, m := range c.ExtraModels {
		if m.ID == p.Model {
			return nil
		}
	}
	return fmt.Errorf("advisor_pin: model must be the id of one of the extra_models, got %q", p.Model)
}

// AdvisorModel is the extra model whose accounts a request must use because it carries the advisor
// tool: one of its anthropic-beta values (a header may hold several, comma-separated, and come more
// than once) starts with the pin's prefix, in any case.
func (c Config) AdvisorModel(headers map[string][]string) (ExtraModel, bool) {
	p := c.AdvisorPin
	if p == nil {
		return ExtraModel{}, false
	}
	for key, values := range headers {
		if !strings.EqualFold(key, "anthropic-beta") {
			continue
		}
		for _, value := range values {
			for _, beta := range strings.Split(value, ",") {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(beta)), p.BetaPrefix) {
					return c.ExtraModel(p.Model)
				}
			}
		}
	}
	return ExtraModel{}, false
}
