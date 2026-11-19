package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const advisorPin = "advisor_pin: {model: claude-mythos-9-1}\n"

func TestTheAdvisorPinNamesAnExtraModelAndHasADefaultPrefix(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne + advisorPin))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdvisorPin == nil || cfg.AdvisorPin.Model != "claude-mythos-9-1" || cfg.AdvisorPin.BetaPrefix != "advisor-tool-" {
		t.Fatalf("advisor pin = %+v", cfg.AdvisorPin)
	}
	cfg, err = Parse([]byte(extraBase + extraOne + "advisor_pin: {model: claude-mythos-9-1, beta_prefix: Advisor-Tool-2026}\n"))
	if err != nil || cfg.AdvisorPin.BetaPrefix != "advisor-tool-2026" {
		t.Fatalf("a given prefix is kept, in lower case: %+v %v", cfg.AdvisorPin, err)
	}
}

func TestNoAdvisorPinByDefault(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdvisorPin != nil {
		t.Fatalf("advisor pin = %+v", cfg.AdvisorPin)
	}
	if _, ok := cfg.AdvisorModel(map[string][]string{"Anthropic-Beta": {"advisor-tool-2026-03-01"}}); ok {
		t.Fatal("without an advisor pin no request is pinned by its beta")
	}
}

func TestTheAdvisorPinIsChecked(t *testing.T) {
	for _, bad := range []string{
		"advisor_pin: {model: claude-mythos-7-7}\n",                             // not an extra model
		"advisor_pin: {beta_prefix: advisor-tool-}\n",                           // no model
		"advisor_pin: {model: claude-mythos-9-1, beta_prefix: \"a b\"}\n",       // a space
		"advisor_pin: {model: claude-mythos-9-1, beta_prefix: \"x,advisor\"}\n", // a comma
	} {
		if _, err := Parse([]byte(extraBase + extraOne + bad)); err == nil || !strings.Contains(err.Error(), "advisor_pin") {
			t.Fatalf("%q must be refused, got %v", bad, err)
		}
	}
}

func TestTheAdvisorMarkerPinsARequestLikeTheBeta(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne + advisorPin))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{AdvisorMarker, "x-cpa-balancer-advisor"} {
		if m, ok := cfg.AdvisorModel(map[string][]string{key: {"1"}}); !ok || m.ID != "claude-mythos-9-1" {
			t.Fatalf("%s: got %+v %v", key, m, ok)
		}
	}
}

// advisorBodies is the corpus tools/sdkproof checks against CPA's own reading of a body (`cpa`: CPA
// adds the advisor beta). The plugin marks (`marked`) every body CPA would, and fails closed on a
// body it cannot read as CPA does: invalid JSON, duplicate or escaped keys, an unexpected type.
func TestTheAdvisorIsFoundInTheBodyWhereverCPAWouldFindIt(t *testing.T) {
	raw, err := os.ReadFile("../../tools/sdkproof/advisor_bodies.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string `json:"name"`
		Body   string `json:"body"`
		CPA    bool   `json:"cpa"`
		Marked bool   `json:"marked"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) < 20 {
		t.Fatalf("corpus: %d cases, %v", len(cases), err)
	}
	for _, c := range cases {
		if c.CPA && !c.Marked {
			t.Fatalf("%s: the corpus must mark every body CPA enables the advisor for", c.Name)
		}
		if got := AdvisorInBody([]byte(c.Body), DefaultAdvisorBeta); got != c.Marked {
			t.Fatalf("%s: marked = %v, want %v", c.Name, got, c.Marked)
		}
	}
}

func TestTheAdvisorBetaIsFoundInAnyValueAndAnyCase(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne + advisorPin))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []map[string][]string{
		{"Anthropic-Beta": {"advisor-tool-2026-03-01"}},
		{"anthropic-beta": {"oauth-2025-04-20, Advisor-Tool-2026-03-01 ,fine-grained-tool-streaming-2025-05-14"}},
		{"ANTHROPIC-BETA": {"oauth-2025-04-20", "  ADVISOR-TOOL-2026-03-01"}},
	} {
		if m, ok := cfg.AdvisorModel(h); !ok || m.ID != "claude-mythos-9-1" {
			t.Fatalf("%v: got %+v %v", h, m, ok)
		}
	}
	for _, h := range []map[string][]string{
		nil,
		{"Anthropic-Beta": {"oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14"}},
		{"Anthropic-Beta": {"x-advisor-tool-2026-03-01"}}, // the prefix starts a value, nowhere else
		{"X-Other": {"advisor-tool-2026-03-01"}},
	} {
		if _, ok := cfg.AdvisorModel(h); ok {
			t.Fatalf("%v must not match", h)
		}
	}
}
