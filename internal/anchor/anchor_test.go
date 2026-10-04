package anchor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func TestRequestIsPinnedOneTokenHaiku(t *testing.T) {
	req := Request("claude-w.json", false, "haiku-x", "sonnet-x")
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	if req.AuthID != "claude-w.json" || req.ForcedProvider != "claude" || req.EntryProtocol != "claude" || req.ExitProtocol != "claude" {
		t.Fatalf("%+v", req)
	}
	if req.Model != "haiku-x" || body["model"] != "haiku-x" || body["max_tokens"] != float64(1) {
		t.Fatalf("%v", body)
	}
	if main := Request("claude-w.json", true, "haiku-x", "sonnet-x"); main.Model != "sonnet-x" {
		t.Fatalf("%+v", main)
	}
}

func TestWindowReading(t *testing.T) {
	reset := now.Add(5 * time.Hour)
	resp := pluginapi.HostModelExecutionResponse{StatusCode: 200, Headers: http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.0"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {strconv.FormatInt(reset.Unix(), 10)},
	}}
	r, ok := WindowReading(resp, now)
	if !ok || r.Source != "anchor" || !r.ResetsAt.Equal(reset) || r.Util != 0 {
		t.Fatalf("%+v", r)
	}
	if _, ok := WindowReading(pluginapi.HostModelExecutionResponse{StatusCode: 200}, now); ok {
		t.Fatal("no headers, no reading")
	}
}
