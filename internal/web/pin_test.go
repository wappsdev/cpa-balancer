package web

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func post(api API, path, body string) pluginapi.ManagementResponse {
	return Handle(pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/cpa-balancer" + path, Body: []byte(body)}, api)
}

func TestPinRouteTakesTheFolderInTheBodyAndAnswersWithTheStatus(t *testing.T) {
	api := &fakeAPI{}
	res := post(api, "/pin", `{"kind":"folder","key":"-work-alpha","account":"claude-a.json"}`)
	if res.StatusCode != 200 || string(res.Body) != `{"mode":"apply"}` {
		t.Fatalf("%d %s", res.StatusCode, res.Body)
	}
	post(api, "/pin", `{"kind":"session","key":"abc","account":""}`)
	if strings.Join(api.pins, ",") != "folder/-work-alpha/claude-a.json,session/abc/" {
		t.Fatalf("%q", api.pins)
	}
}

func TestPinRouteRefusesWhatItCannotRead(t *testing.T) {
	api := &fakeAPI{}
	for name, body := range map[string]string{
		"not json":            `nope`,
		"empty":               ``,
		"an account refused":  `{"kind":"folder","key":"k","account":"claude-bad.json"}`,
		"a body past a bound": `{"kind":"folder","key":"` + strings.Repeat("a", 5000) + `","account":"x"}`,
	} {
		if res := post(api, "/pin", body); res.StatusCode != 400 {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	if len(api.pins) != 1 {
		t.Fatalf("only the call the API refused reached it: %q", api.pins)
	}
	if call(api, "GET", "/v0/management/plugins/cpa-balancer/pin", nil).StatusCode != 404 {
		t.Fatal("pin is POST only")
	}
}
