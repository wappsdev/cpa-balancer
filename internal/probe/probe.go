// Package probe queries the OAuth usage endpoint for accounts that are not serving traffic.
package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	UsageURL        = "https://api.anthropic.com/api/oauth/usage"
	MinInterval     = 10 * time.Minute
	StaleAfter      = 60 * time.Minute
	SwitchFreshness = 10 * time.Minute
)

// Due reports whether to probe now. reference is the observation time of the oldest weekly
// reading; zero means a weekly bucket has no data at all.
func Due(now, lastProbe, retryAfter, reference time.Time, resetEstimated, switchingTo bool) bool {
	if !retryAfter.IsZero() && now.Before(retryAfter) {
		return false
	}
	if !lastProbe.IsZero() && now.Sub(lastProbe) < MinInterval {
		return false
	}
	if switchingTo {
		return reference.IsZero() || now.Sub(reference) > SwitchFreshness
	}
	if resetEstimated {
		return true
	}
	stale := reference.IsZero() || now.Sub(reference) > StaleAfter
	return stale && (lastProbe.IsZero() || now.Sub(lastProbe) >= StaleAfter)
}

func AccessToken(authJSON []byte) (string, error) {
	var auth struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(authJSON, &auth); err != nil {
		return "", errors.New("auth file is not valid JSON")
	}
	if auth.AccessToken == "" {
		return "", errors.New("no access_token in auth file")
	}
	return auth.AccessToken, nil
}

func Request(accessToken string) pluginapi.HTTPRequest {
	return pluginapi.HTTPRequest{Method: http.MethodGet, URL: UsageURL, Headers: http.Header{
		"Authorization":  {"Bearer " + accessToken},
		"Anthropic-Beta": {"oauth-2025-04-20"},
		"Accept":         {"application/json"},
		"User-Agent":     {"cpa-balancer/0.2"},
	}}
}

type Result struct {
	Readings   quota.Readings
	RetryAfter time.Time
	Err        error
}

func Parse(resp pluginapi.HTTPResponse, now time.Time) Result {
	if resp.StatusCode != http.StatusOK {
		res := Result{Err: fmt.Errorf("HTTP %d", resp.StatusCode)}
		if seconds, err := strconv.Atoi(resp.Headers.Get("Retry-After")); err == nil {
			res.RetryAfter = now.Add(time.Duration(seconds) * time.Second)
		}
		return res
	}
	readings, err := quota.FromProbe(resp.Body, now)
	if err != nil {
		return Result{Err: fmt.Errorf("decode usage: %w", err)}
	}
	return Result{Readings: readings}
}

// ProfileURL names the account's subscription; its rate-limit tier sets how much quota 1% is.
const ProfileURL = "https://api.anthropic.com/api/oauth/profile"

func ProfileRequest(accessToken string) pluginapi.HTTPRequest {
	r := Request(accessToken)
	r.URL = ProfileURL
	return r
}

// Plan is a subscription and its weekly limit in Pro plans: a Max 20x has ten times a Pro's weekly
// limit (its 20x is the 5-hour limit), a Max 5x five times.
type Plan struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

// ParsePlan reads the plan from the profile; an unknown one is an error, never a guess.
func ParsePlan(body []byte) (Plan, error) {
	var p struct {
		Account struct {
			HasClaudePro bool `json:"has_claude_pro"`
		} `json:"account"`
		Organization struct {
			Type string `json:"organization_type"`
			Tier string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Plan{}, fmt.Errorf("decode profile: %w", err)
	}
	switch tier := p.Organization.Tier; {
	case strings.Contains(tier, "max_20x"):
		return Plan{"max_20x", WeightOf("max_20x")}, nil
	case strings.Contains(tier, "max_5x"):
		return Plan{"max_5x", WeightOf("max_5x")}, nil
	case p.Organization.Type == "claude_pro" || p.Account.HasClaudePro:
		return Plan{"pro", WeightOf("pro")}, nil
	}
	return Plan{}, fmt.Errorf("unknown plan %q / %q", p.Organization.Type, p.Organization.Tier)
}

// WeightOf is a plan's weekly limit in Pro plans, from its name, so a weight saved by an older
// version never outlives a correction; 0 for an unknown name.
func WeightOf(name string) float64 {
	switch name {
	case "max_20x":
		return 10
	case "max_5x":
		return 5
	case "pro":
		return 1
	}
	return 0
}
