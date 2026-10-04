// Package web serves the Management Center page and its JSON API.
package web

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	PluginID     = "cpa-balancer"
	ResourcePath = "/status"                          // relative to /v0/resource/plugins/<id>
	RoutePath    = "/plugins/" + PluginID + "/status" // relative to /v0/management
)

//go:embed status.html
var page []byte

func Registration() map[string]any {
	return map[string]any{
		"routes": []map[string]any{{"Method": http.MethodGet, "Path": RoutePath}},
		"resources": []map[string]any{{"Path": ResourcePath, "Menu": "Account Balancer",
			"Description": "Which Claude account each quota bucket uses, and why."}},
	}
}

// Handle answers management.handle for the page and the status API.
func Handle(req pluginapi.ManagementRequest, status func() any) pluginapi.ManagementResponse {
	switch req.Path {
	case "/v0/resource/plugins/" + PluginID + ResourcePath:
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK,
			Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}}, Body: page}
	case "/v0/management" + RoutePath:
		body, err := json.Marshal(status())
		if err != nil {
			return pluginapi.ManagementResponse{StatusCode: http.StatusInternalServerError, Body: []byte(err.Error())}
		}
		return pluginapi.ManagementResponse{StatusCode: http.StatusOK,
			Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: body}
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Body: []byte("not found")}
}
