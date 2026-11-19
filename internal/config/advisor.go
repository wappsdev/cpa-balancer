package config

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
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
// let such a request pass the pin. The marker stays in the request's headers (CPA's plugin host
// drops an after-auth interceptor's ClearHeaders), but CPA's claude executor forwards only its own
// list of client headers, so it never reaches the upstream.
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

// maxBodyDepth bounds the scan of a body: a body nested deeper is taken to carry the advisor.
const maxBodyDepth = 64

// AdvisorInBody reports whether a request body carries the advisor. CPA decides on the body after
// it is translated to Claude's format (internal/runtime/executor/claude_executor_request.go reads
// the translated "tools" and "betas"), and a translator may move a tool from anywhere (the
// Responses API's input[].additional_tools, for one), so the whole body in the client's format is
// read, with gjson as CPA does: any object, at any depth, whose "type", trimmed and in lower case,
// starts with "advisor_", and any "betas", one string or a list, with a string value that starts
// with prefix in any case. That may pin a request CPA would not give the advisor; it never misses
// one. It fails closed, reporting the advisor, for a body it cannot read the way CPA would: one
// that is not valid JSON or is nested deeper than maxBodyDepth, an object with "type" or "betas"
// twice or under an escaped name, "tools" or "betas" twice at the top, or a top-level "tools" or
// "betas" of another type. An empty body, and null, carry nothing.
func AdvisorInBody(body []byte, prefix string) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	if !gjson.ValidBytes(body) {
		return true
	}
	root := gjson.ParseBytes(body)
	if root.IsObject() {
		if ambiguous(root, "tools", "betas") {
			return true
		}
		if tools := root.Get("tools"); tools.Exists() && tools.Type != gjson.Null && !tools.IsArray() {
			return true
		}
		if betas := root.Get("betas"); betas.Exists() && betas.Type != gjson.Null && betas.Type != gjson.String && !betas.IsArray() {
			return true
		}
	}
	return advisorIn(root, prefix, 0)
}

// advisorIn scans a value of a body for the advisor; see AdvisorInBody.
func advisorIn(v gjson.Result, prefix string, depth int) bool {
	if depth > maxBodyDepth {
		return true
	}
	if !v.IsObject() && !v.IsArray() {
		return false
	}
	if v.IsObject() && ambiguous(v, "type", "betas") {
		return true
	}
	found := false
	v.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "type":
			found = value.Type == gjson.String && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value.String())), "advisor_")
		case "betas":
			found = betaIn(value, prefix)
		}
		found = found || advisorIn(value, prefix, depth+1)
		return !found
	})
	return found
}

// betaIn reports whether a "betas" value, one string or a list, holds a string that starts with
// prefix in any case; other values are skipped, as CPA finds no beta in them.
func betaIn(v gjson.Result, prefix string) bool {
	starts := func(s gjson.Result) bool {
		return s.Type == gjson.String && strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.String())), prefix)
	}
	if !v.IsArray() {
		return starts(v)
	}
	for _, b := range v.Array() {
		if starts(b) {
			return true
		}
	}
	return false
}

// ambiguous reports whether an object holds one of the names twice, or under an escaped spelling:
// readers then disagree on which value counts.
func ambiguous(object gjson.Result, names ...string) bool {
	seen := map[string]int{}
	odd := false
	object.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if slices.Contains(names, name) {
			seen[name]++
			odd = odd || key.Raw != `"`+name+`"` || seen[name] > 1
		}
		return !odd
	})
	return odd
}
