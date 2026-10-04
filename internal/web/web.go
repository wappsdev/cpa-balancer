// Package web serves the Management Center page and its JSON API.
package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	PluginID     = "cpa-balancer"
	resourceBase = "/v0/resource/plugins/" + PluginID
	routeBase    = "/v0/management/plugins/" + PluginID
	pagePath     = "/status" // the menu entry; serves index.html
)

// API is what the routes need from the running plugin.
type API interface {
	Status() any
	History(rangeName string) (any, error)
	Run() any
	Credentials() (any, error)
}

//go:embed assets
var assets embed.FS

// resources maps resource paths to embedded files; the page links its assets relatively.
var resources = map[string]string{
	pagePath:     "assets/index.html",
	"/app.js":    "assets/app.js",
	"/app.css":   "assets/app.css",
	"/uplot.js":  "assets/uPlot.iife.min.js",
	"/uplot.css": "assets/uPlot.min.css",
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

var routes = []struct{ method, path string }{
	{http.MethodGet, "/status"},
	{http.MethodGet, "/history"},
	{http.MethodPost, "/run"},
	{http.MethodGet, "/credentials"},
}

func Registration() map[string]any {
	var rs []map[string]any
	for _, r := range routes {
		rs = append(rs, map[string]any{"Method": r.method, "Path": "/plugins/" + PluginID + r.path})
	}
	var res []map[string]any
	for p := range resources {
		entry := map[string]any{"Path": p}
		if p == pagePath {
			entry["Menu"] = "Account Balancer"
			entry["Description"] = "Which Claude account each quota bucket uses, its history and settings."
		}
		res = append(res, entry)
	}
	return map[string]any{"routes": rs, "resources": res}
}

// Handle answers management.handle for the page, its assets and the JSON API.
func Handle(req pluginapi.ManagementRequest, api API) pluginapi.ManagementResponse {
	if p, found := strings.CutPrefix(req.Path, resourceBase); found {
		if file, known := resources[p]; known {
			body, err := assets.ReadFile(file)
			if err != nil {
				return text(http.StatusInternalServerError, err.Error())
			}
			return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Body: body, Headers: http.Header{
				"Content-Type": {contentTypes[path.Ext(file)]}, "Cache-Control": {"no-store"}}}
		}
	}
	switch req.Method + " " + strings.TrimPrefix(req.Path, routeBase) {
	case "GET /status":
		return jsonResponse(api.Status(), nil)
	case "GET /history":
		return jsonResponse(api.History(req.Query.Get("range")))
	case "POST /run":
		return jsonResponse(api.Run(), nil)
	case "GET /credentials":
		return jsonResponse(api.Credentials())
	}
	return text(http.StatusNotFound, "not found")
}

func jsonResponse(v any, err error) pluginapi.ManagementResponse {
	if err != nil {
		return text(http.StatusBadRequest, err.Error())
	}
	body, err := json.Marshal(v)
	if err != nil {
		return text(http.StatusInternalServerError, err.Error())
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Body: body,
		Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}}
}

func text(code int, msg string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{StatusCode: code, Body: []byte(msg),
		Headers: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}}
}

// Credential is a Claude credential CPA knows about, for the "add account" picker.
type Credential struct {
	AuthFile string `json:"auth_file"`
	Email    string `json:"email,omitempty"`
	Status   string `json:"status,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// ClaudeCredentials keeps only Claude credentials and only the fields the picker shows.
func ClaudeCredentials(files []pluginapi.HostAuthFileEntry) []Credential {
	out := []Credential{}
	for _, f := range files {
		if f.Provider == "claude" {
			out = append(out, Credential{AuthFile: f.ID, Email: f.Email, Status: f.Status, Disabled: f.Disabled})
		}
	}
	return out
}
