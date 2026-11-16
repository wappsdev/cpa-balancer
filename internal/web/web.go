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
	maxPinBody   = 4096      // the longest body: a pin is a kind, a name of up to 255 bytes and an auth file
)

// API is what the routes need from the running plugin.
type API interface {
	Status() any
	History(rangeName string) (any, error)
	Outlook() (any, error)
	Transcripts(rangeName string) (any, error) // read-only: what Claude Code's transcripts say, apart from the live history
	Run() any
	Recheck(authFile string) any // "" = every account
	// Pin pins a folder ("folder") or a main session ("session") to an account; "" unpins. The
	// names travel in the body: a folder name is user data and a URL reaches access logs.
	Pin(kind, key, account string) error
	// Rebalance plans a fresh placement of every project without a folder pin, and carries it out
	// when apply is set. The plan names folders, so it is an answer, never a URL.
	Rebalance(apply bool) (any, error)
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
	{http.MethodGet, "/outlook"},
	{http.MethodGet, "/transcripts"},
	{http.MethodPost, "/run"},
	{http.MethodPost, "/recheck"},
	{http.MethodPost, "/pin"},
	{http.MethodPost, "/rebalance"},
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
	case "GET /outlook":
		return jsonResponse(api.Outlook())
	case "GET /transcripts":
		return jsonResponse(api.Transcripts(req.Query.Get("range")))
	case "POST /run":
		return jsonResponse(api.Run(), nil)
	case "POST /recheck":
		return jsonResponse(api.Recheck(req.Query.Get("auth_file")), nil)
	case "POST /pin":
		var body struct{ Kind, Key, Account string }
		if len(req.Body) > maxPinBody || json.Unmarshal(req.Body, &body) != nil {
			return text(http.StatusBadRequest, "a pin needs a JSON body of kind, key and account")
		}
		if err := api.Pin(body.Kind, body.Key, body.Account); err != nil {
			return text(http.StatusBadRequest, err.Error())
		}
		return jsonResponse(api.Status(), nil)
	case "POST /rebalance":
		var body struct{ Apply bool }
		if len(req.Body) > maxPinBody || json.Unmarshal(req.Body, &body) != nil {
			return text(http.StatusBadRequest, "a rebalance needs a JSON body, {\"apply\": true} or {\"apply\": false}")
		}
		return jsonResponse(api.Rebalance(body.Apply))
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
