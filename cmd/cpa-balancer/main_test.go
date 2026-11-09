package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/engine"
)

func configureYAML(t *testing.T, yaml string) {
	t.Helper()
	req, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml)})
	if raw := configure(req); len(raw) == 0 {
		t.Fatal("empty response")
	}
}

func TestRejectedConfigKeepsTheEngineAndReportsTheError(t *testing.T) {
	dir := t.TempDir()
	valid := fmt.Sprintf("state_path: %s/state.json\naccounts: [{auth_file: a.json}]\nbuckets: {main: [m]}\n", dir)
	configureYAML(t, valid)
	t.Cleanup(shutdown)
	first := engineNow()
	if first == nil || recorder == nil {
		t.Fatal("a valid config must build an engine and the recorder")
	}
	rec := recorder

	configureYAML(t, valid+"thresholds: {final_cap: 80}\n")
	v, ok := pluginAPI{}.Status().(engine.View)
	if !ok || v.ConfigError == "" || engineNow() != first {
		t.Fatalf("invalid config must keep the engine and report: %+v", v)
	}

	configureYAML(t, valid+"mode: shadow\n")
	v = pluginAPI{}.Status().(engine.View)
	if v.ConfigError != "" || v.Mode != "shadow" || engineNow() == first || recorder != rec {
		t.Fatalf("a valid config must clear the error and keep the recorder: %+v", v)
	}
	if _, err := (pluginAPI{}).History("24h"); err != nil {
		t.Fatal(err)
	}
}
