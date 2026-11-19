//go:build sdkproof

// This test runs inside a copy of the CLIProxyAPI module (see run.sh), in the claude executor's
// package, because the functions it checks are internal to it. It proves the corpus the plugin's
// own test uses (advisor_bodies.json): for each body, "cpa" is what CPA's claude executor does,
// that is whether it adds the advisor beta on its own (a body tool whose type starts with advisor_,
// or the beta in the body's betas). A body in another client format ("format": openai-response for
// the Responses API, openai for chat completions) goes through CPA's own translator to Claude's
// format first, as the claude executor does. The plugin marks every body for which this is true. It also
// proves that the claude executor does not forward the plugin's marker header upstream.
package executor

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type advisorBody struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Body   string `json:"body"`
	CPA    bool   `json:"cpa"`
	Marked bool   `json:"marked"`
}

func TestProofAdvisorBodiesAsCPAReadsThem(t *testing.T) {
	raw, err := os.ReadFile("zz_advisor_bodies.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []advisorBody
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("corpus: %v", err)
	}
	for _, c := range cases {
		body := []byte(c.Body)
		if c.Format != "" && c.Format != "claude" {
			body = sdktranslator.TranslateRequest(sdktranslator.FromString(c.Format), sdktranslator.FormatClaude, "claude-opus-5-5", body, false)
			t.Logf("%s: translated to %s", c.Name, body)
		}
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
	if claudeAdvisorToolBeta != "advisor-tool-2026-03-01" || !slices.ContainsFunc(cases, func(c advisorBody) bool { return c.CPA && c.Format == "openai-response" }) {
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
