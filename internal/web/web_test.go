package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHandle(t *testing.T) {
	status := func() any { return map[string]string{"mode": "apply"} }
	html := Handle(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/cpa-balancer/status"}, status)
	if html.StatusCode != 200 || !strings.HasPrefix(html.Headers.Get("Content-Type"), "text/html") || !bytes.Contains(html.Body, []byte("managementKey")) {
		t.Fatalf("page: %d %v", html.StatusCode, html.Headers)
	}
	api := Handle(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/cpa-balancer/status"}, status)
	if api.StatusCode != 200 || string(api.Body) != `{"mode":"apply"}` {
		t.Fatalf("api: %d %s", api.StatusCode, api.Body)
	}
	if Handle(pluginapi.ManagementRequest{Path: "/v0/management/other"}, status).StatusCode != 404 {
		t.Fatal("unknown path must be 404")
	}
}
