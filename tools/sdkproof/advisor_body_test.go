//go:build sdkproof

// This test runs inside a copy of the CLIProxyAPI module (see run.sh), in the claude executor's
// package, because the functions it checks are internal to it. It proves the corpus the plugin's
// own test uses (advisor_bodies.json): for each body, "cpa" is what CPA's claude executor does,
// that is whether it adds the advisor beta on its own (a body tool whose type starts with advisor_,
// or the beta in the body's betas). The plugin marks every body for which this is true. It also
// proves that the claude executor does not forward the plugin's marker header upstream.
package executor

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"
)

func TestProofAdvisorBodiesAsCPAReadsThem(t *testing.T) {
	raw, err := os.ReadFile("zz_advisor_bodies.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string `json:"name"`
		Body   string `json:"body"`
		CPA    bool   `json:"cpa"`
		Marked bool   `json:"marked"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("corpus: %v", err)
	}
	for _, c := range cases {
		body := []byte(c.Body)
		betas, _ := extractAndRemoveBetas(body)
		requested := claudeRequestedBetas("", betas)
		got := requested[claudeAdvisorToolBeta] || claudeBodyHasAdvisorTool(body)
		if got != c.CPA {
			t.Errorf("%s: CPA adds the advisor beta = %v, the corpus says %v", c.Name, got, c.CPA)
		}
		if got && !c.Marked {
			t.Errorf("%s: CPA adds the advisor beta, so the plugin must mark the body", c.Name)
		}
	}
	if claudeAdvisorToolBeta != "advisor-tool-2026-03-01" || !slices.ContainsFunc(cases, func(c struct {
		Name   string `json:"name"`
		Body   string `json:"body"`
		CPA    bool   `json:"cpa"`
		Marked bool   `json:"marked"`
	}) bool {
		return c.CPA
	}) {
		t.Fatalf("the corpus is built on CPA's advisor beta %q", claudeAdvisorToolBeta)
	}
}

func TestProofTheClaudeExecutorDoesNotForwardTheAdvisorMarker(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		upstream := http.Header{}
		copyClaudeCallerFingerprintHeaders(upstream, http.Header{
			"X-Cpa-Balancer-Advisor": {"1"}, "Anthropic-Beta": {"oauth-2025-04-20"}, "X-Claude-Code-Session-Id": {"s"},
		}, confirmed)
		if upstream.Get("X-Cpa-Balancer-Advisor") != "" || upstream.Get("Anthropic-Beta") == "" {
			t.Fatalf("Claude Code %v: upstream headers %v, want the beta and no marker", confirmed, upstream)
		}
	}
}
