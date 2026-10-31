package probe

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func ago(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }

func TestDueRules(t *testing.T) {
	var zero time.Time
	cases := []struct {
		name                        string
		last, retry, ref            time.Time
		resetEstimated, switchingTo bool
		want                        bool
	}{
		{"fresh", zero, zero, ago(5), false, false, false},
		{"stale", zero, zero, ago(61), false, false, true},
		{"missing", zero, zero, zero, false, false, true},
		{"retry-after", zero, now.Add(time.Minute), ago(61), false, false, false},
		{"spacing", ago(9), zero, ago(61), false, false, false},
		{"hourly", ago(30), zero, ago(61), false, false, false},
		{"hour passed", ago(61), zero, ago(61), false, false, true},
		{"switch stale", zero, zero, ago(11), false, true, true},
		{"switch fresh", zero, zero, ago(9), false, true, false},
		{"after reset", zero, zero, ago(5), true, false, true},
	}
	for _, c := range cases {
		if got := Due(now, c.last, c.retry, c.ref, c.resetEstimated, c.switchingTo); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestAccessToken(t *testing.T) {
	if tok, err := AccessToken([]byte(`{"access_token":"secret-123"}`)); err != nil || tok != "secret-123" {
		t.Fatal(tok, err)
	}
	if _, err := AccessToken([]byte(`{}`)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRequestAndParse(t *testing.T) {
	req := Request("secret-123")
	if req.URL != UsageURL || req.Headers.Get("Authorization") != "Bearer secret-123" || req.Headers.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatalf("%+v", req)
	}
	ok := Parse(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"limits":[{"kind":"weekly_all","percent":52,"resets_at":"2026-10-07T07:00:00+00:00"}]}`)}, now)
	if ok.Err != nil || ok.Readings[quota.Main].Util != 52 {
		t.Fatalf("%+v", ok)
	}
	limited := Parse(pluginapi.HTTPResponse{StatusCode: 429, Headers: http.Header{"Retry-After": {"300"}}}, now)
	if limited.Err == nil || !limited.RetryAfter.Equal(now.Add(5*time.Minute)) || strings.Contains(limited.Err.Error(), "secret") {
		t.Fatalf("%+v", limited)
	}
}

func TestParsePlanReadsTheRateLimitTier(t *testing.T) {
	for body, want := range map[string]Plan{
		`{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`: {"max_20x", 10},
		`{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_5x"}}`:  {"max_5x", 5},
		`{"account":{"has_claude_pro":true},"organization":{"organization_type":"claude_pro"}}`:          {"pro", 1},
	} {
		got, err := ParsePlan([]byte(body))
		if err != nil || got != want {
			t.Fatalf("%s: got %+v %v, want %+v", body, got, err, want)
		}
	}
	if _, err := ParsePlan([]byte(`{"organization":{"organization_type":"claude_team"}}`)); err == nil {
		t.Fatal("an unknown plan must be an error, not a guess")
	}
}
