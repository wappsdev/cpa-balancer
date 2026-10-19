package web

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fakeAPI struct {
	ran       int
	rechecked []string
}

func (f *fakeAPI) Status() any { return map[string]string{"mode": "apply"} }
func (f *fakeAPI) Run() any    { f.ran++; return map[string]string{"mode": "shadow"} }
func (f *fakeAPI) Recheck(authFile string) any {
	f.rechecked = append(f.rechecked, authFile)
	return []map[string]string{{"auth_file": authFile}}
}
func (f *fakeAPI) History(r string) (any, error) {
	if r != "7d" {
		return nil, errors.New("range must be one of 24h, 7d, 30d, 90d")
	}
	return map[string]int{"step": 1800}, nil
}
func (f *fakeAPI) Credentials() (any, error) {
	return ClaudeCredentials([]pluginapi.HostAuthFileEntry{
		{ID: "claude-a.json", Provider: "claude", Email: "a@x", Status: "active", AuthIndex: "7", Path: "/secret/path"},
		{ID: "codex-b.json", Provider: "codex"},
	}), nil
}

func call(api API, method, path string, query url.Values) pluginapi.ManagementResponse {
	return Handle(pluginapi.ManagementRequest{Method: method, Path: path, Query: query}, api)
}

func TestAssets(t *testing.T) {
	api := &fakeAPI{}
	for p, want := range map[string]string{
		"/status": "text/html", "/app.js": "text/javascript", "/app.css": "text/css",
		"/uplot.js": "text/javascript", "/uplot.css": "text/css",
	} {
		res := call(api, "GET", "/v0/resource/plugins/cpa-balancer"+p, nil)
		if res.StatusCode != 200 || !strings.HasPrefix(res.Headers.Get("Content-Type"), want) || len(res.Body) == 0 {
			t.Fatalf("%s: %d %v", p, res.StatusCode, res.Headers)
		}
	}
	page := call(api, "GET", "/v0/resource/plugins/cpa-balancer/status", nil)
	for _, ref := range []string{`src="uplot.js"`, `href="uplot.css"`, `src="app.js"`, `href="app.css"`} {
		if !bytes.Contains(page.Body, []byte(ref)) {
			t.Fatalf("page must link %s relatively", ref)
		}
	}
	if call(api, "GET", "/v0/resource/plugins/cpa-balancer/LICENSE", nil).StatusCode != 404 {
		t.Fatal("only listed resources are served")
	}
}

func TestRoutes(t *testing.T) {
	api := &fakeAPI{}
	base := "/v0/management/plugins/cpa-balancer"
	if res := call(api, "GET", base+"/status", nil); res.StatusCode != 200 || string(res.Body) != `{"mode":"apply"}` {
		t.Fatalf("status: %d %s", res.StatusCode, res.Body)
	}
	if res := call(api, "GET", base+"/history", url.Values{"range": {"7d"}}); res.StatusCode != 200 || string(res.Body) != `{"step":1800}` {
		t.Fatalf("history: %d %s", res.StatusCode, res.Body)
	}
	if res := call(api, "GET", base+"/history", url.Values{"range": {"1y"}}); res.StatusCode != 400 {
		t.Fatalf("bad range: %d", res.StatusCode)
	}
	if res := call(api, "POST", base+"/run", nil); res.StatusCode != 200 || api.ran != 1 {
		t.Fatalf("run: %d ran=%d", res.StatusCode, api.ran)
	}
	if call(api, "GET", base+"/run", nil).StatusCode != 404 {
		t.Fatal("run must be POST only")
	}
	res := call(api, "GET", base+"/credentials", nil)
	if res.StatusCode != 200 || string(res.Body) != `[{"auth_file":"claude-a.json","email":"a@x","status":"active"}]` {
		t.Fatalf("credentials: %d %s", res.StatusCode, res.Body)
	}
	if call(api, "GET", "/v0/management/other", nil).StatusCode != 404 {
		t.Fatal("unknown path must be 404")
	}
}

func TestRegistrationListsEveryRouteAndResource(t *testing.T) {
	reg := Registration()
	if len(reg["routes"].([]map[string]any)) != 5 || len(reg["resources"].([]map[string]any)) != len(resources) {
		t.Fatalf("%+v", reg)
	}
	menus := 0
	for _, r := range reg["resources"].([]map[string]any) {
		if r["Menu"] != nil {
			menus++
		}
	}
	if menus != 1 {
		t.Fatalf("exactly one menu entry, got %d", menus)
	}
}

func TestRecheckRoute(t *testing.T) {
	api := &fakeAPI{}
	base := "/v0/management/plugins/cpa-balancer"
	if res := call(api, "POST", base+"/recheck", nil); res.StatusCode != 200 || string(res.Body) != `[{"auth_file":""}]` {
		t.Fatalf("all accounts: %d %s", res.StatusCode, res.Body)
	}
	if res := call(api, "POST", base+"/recheck", url.Values{"auth_file": {"claude-a.json"}}); res.StatusCode != 200 {
		t.Fatalf("one account: %d", res.StatusCode)
	}
	if strings.Join(api.rechecked, ",") != ",claude-a.json" {
		t.Fatalf("rechecked %q", api.rechecked)
	}
	if call(api, "GET", base+"/recheck", nil).StatusCode != 404 {
		t.Fatal("recheck must be POST only")
	}
}
