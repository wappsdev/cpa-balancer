// Package probe queries the OAuth usage endpoint for accounts that are not serving traffic.
package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
