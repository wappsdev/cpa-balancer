package main

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/transcripts"
	"github.com/wappsdev/cpa-balancer/internal/web"
)

func getTranscripts(rangeName string) pluginapi.ManagementResponse {
	return web.Handle(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/cpa-balancer/transcripts",
		Query: url.Values{"range": {rangeName}}}, pluginAPI{})
}

func TestTheTranscriptsRouteServesTheStoreAndNothingFromTheLiveHistory(t *testing.T) {
	const sid = "11111111-aaaa-4bbb-8ccc-000000000001"
	dir := filepath.Join(t.TempDir(), "transcripts")
	hour := time.Now().UTC().Add(-26 * time.Hour).Truncate(time.Hour)
	older := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	err := transcripts.Write(dir, map[transcripts.Key]history.Traffic{
		{Hour: hour, Session: sid, Model: "claude-opus-5-5"}:                       {Requests: 2, InputTokens: 10, OutputTokens: 5},
		{Hour: hour, Session: sid + "/ab12", Model: "claude-haiku-5-5"}:            {Requests: 1, InputTokens: 4},
		{Hour: hour, Session: sid, Model: "claude-fable-5-2", Advisor: true}:       {Requests: 1, InputTokens: 700},
		{Hour: older, Session: "22222222-aaaa-4bbb-8ccc-000000000002", Model: "m"}: {Requests: 9, InputTokens: 9},
	}, map[string]transcripts.Title{sid: {Text: "Made-up session name", Kind: transcripts.KindCustom}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	previous := transcript
	transcript = dir
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); transcript = previous; mu.Unlock() })

	res := getTranscripts("7d")
	if res.StatusCode != 200 || res.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", res.StatusCode, res.Headers)
	}
	var got struct {
		Total    history.Traffic `json:"total"`
		Advisor  history.Traffic `json:"advisor"`
		Sessions []struct {
			ID            string                     `json:"id"`
			Title         string                     `json:"title"`
			TitleKind     string                     `json:"title_kind"`
			Total         history.Traffic            `json:"total"`
			Models        map[string]history.Traffic `json:"models"`
			AdvisorModels map[string]history.Traffic `json:"advisor_models"`
			Subagents     []struct {
				ID string `json:"id"`
			} `json:"subagents"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatalf("%v: %s", err, res.Body)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != sid || got.Sessions[0].Total.Requests != 3 || got.Total.Requests != 3 ||
		got.Sessions[0].Models["claude-haiku-5-5"].Requests != 1 || got.Sessions[0].AdvisorModels["claude-fable-5-2"].InputTokens != 700 ||
		got.Advisor.InputTokens != 700 || len(got.Sessions[0].Subagents) != 1 || got.Sessions[0].Subagents[0].ID != sid+"/ab12" {
		t.Fatalf("7 days: %s", res.Body)
	}
	if got.Sessions[0].Title != "Made-up session name" || got.Sessions[0].TitleKind != "custom" {
		t.Fatalf("the session's title is served with its kind: %s", res.Body)
	}
	if strings.Contains(string(res.Body), "account") {
		t.Fatalf("a transcript names no account: %s", res.Body)
	}
	if month := getTranscripts("30d"); !strings.Contains(string(month.Body), `"requests":12`) {
		t.Fatalf("30 days: %s", month.Body)
	}
	if getTranscripts("90d").StatusCode != 400 || getTranscripts("").StatusCode != 400 {
		t.Fatal("only 7d and 30d")
	}
}

func TestTheTranscriptsRouteBeforeAnyConfigurationIsAnError(t *testing.T) {
	mu.Lock()
	previous := transcript
	transcript = ""
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); transcript = previous; mu.Unlock() })
	if res := getTranscripts("7d"); res.StatusCode != 400 {
		t.Fatalf("%d %s", res.StatusCode, res.Body)
	}
}
