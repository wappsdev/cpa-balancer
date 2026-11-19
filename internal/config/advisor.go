package config

import (
	"encoding/json"
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

// AdvisorMarker is the header the plugin's request interceptor adds, before an account is chosen,
// to a request whose body carries the advisor. CPA adds the advisor beta itself, after the pick,
// when the body declares an advisor tool or lists the beta in "betas", so the header alone would
// let such a request pass the pin. The interceptor removes the marker again once the account is
// chosen, so it never reaches the upstream.
const AdvisorMarker = "X-Cpa-Balancer-Advisor"

// AdvisorModel is the extra model whose accounts a request must use because it carries the advisor
// tool. See AdvisorRequested.
func (c Config) AdvisorModel(headers map[string][]string) (ExtraModel, bool) {
	p := c.AdvisorPin
	if p == nil || !AdvisorRequested(headers, p.BetaPrefix) {
		return ExtraModel{}, false
	}
	return c.ExtraModel(p.Model)
}

// AdvisorRequested reports whether request headers carry the advisor: the marker, or an
// anthropic-beta value (a header may hold several, comma-separated, and come more than once) that
// starts with prefix, in any case.
func AdvisorRequested(headers map[string][]string, prefix string) bool {
	for key, values := range headers {
		switch {
		case strings.EqualFold(key, AdvisorMarker):
			return true
		case strings.EqualFold(key, "anthropic-beta"):
			for _, value := range values {
				for _, beta := range strings.Split(value, ",") {
					if strings.HasPrefix(strings.ToLower(strings.TrimSpace(beta)), prefix) {
						return true
					}
				}
			}
		}
	}
	return false
}

// AdvisorInBody reports whether a request body carries the advisor as CPA's claude executor finds
// it: a tool whose type starts with "advisor_" (trimmed, in any case), or a value of "betas" (a
// list, or one string) that starts with prefix.
func AdvisorInBody(body []byte, prefix string) bool {
	var payload struct {
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
		Betas json.RawMessage `json:"betas"`
	}
	if json.Unmarshal(body, &payload) != nil {
		// a body whose tools are not a list is read field by field below
		var loose map[string]json.RawMessage
		if json.Unmarshal(body, &loose) != nil {
			return false
		}
		payload.Betas = loose["betas"]
	}
	for _, t := range payload.Tools {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Type)), "advisor_") {
			return true
		}
	}
	var betas []string
	if json.Unmarshal(payload.Betas, &betas) != nil {
		var one string
		if json.Unmarshal(payload.Betas, &one) != nil {
			return false
		}
		betas = []string{one}
	}
	for _, b := range betas {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(b)), prefix) {
			return true
		}
	}
	return false
}
