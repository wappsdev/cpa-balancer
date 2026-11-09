// Package anchor builds the 1-token request that starts an account's 5-hour window.
package anchor

import (
	"encoding/json"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// Request is sent through host.model.execute pinned to the account; CPA applies its own Claude
// request shaping, including the Claude Code system prompt main models require.
func Request(authID string, useMainModel bool, model, fallbackModel string) pluginapi.HostModelExecutionRequest {
	if useMainModel {
		model = fallbackModel
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	return pluginapi.HostModelExecutionRequest{
		EntryProtocol: "claude", ExitProtocol: "claude", Model: model, Body: body,
		ForcedProvider: "claude", AuthID: authID,
	}
}

// WindowReading reads the 5-hour window from the anchor response headers.
func WindowReading(resp pluginapi.HostModelExecutionResponse, now time.Time) (quota.Reading, bool) {
	r, ok := quota.FromHeaders(resp.Headers, "", nil, nil, now)[quota.Session]
	r.Source = "anchor"
	return r, ok
}
