package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// mythosConfig serves the extra model Extra (a Mythos id) through G.
func mythosConfig(t *testing.T) config.Config {
	c := testConfig(t)
	c.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{G}, ContextLength: 1000, MaxCompletionTokens: 100}}
	return c
}

func accountView(v View, authFile string) AccountView {
	for _, a := range v.Accounts {
		if a.AuthFile == authFile {
			return a
		}
	}
	return AccountView{}
}

// A Fable request's response carries the all-models week in its unified 7d header (Fable use
// counts toward it). A Mythos request draws on the same share, so its response is read the same way.
func TestAMythosRequestReadsTheAllModelsWeekLikeAFableRequest(t *testing.T) {
	for _, model := range []string{Fable, Extra, Extra + "(high)"} {
		e := New(mythosConfig(t), newHost(), (&clock{t0}).now, nil)
		e.OnUsage(usage(G, model, headers(40, 90, 5, 3, t0)))
		r, ok := accountView(e.Status(), G).Readings[quota.Main]
		if !ok || int(r.Util+0.5) != 40 || r.Source != "header" {
			t.Fatalf("%s: all-models week = %+v, %v", model, r, ok)
		}
	}
}

func TestWithoutExtraModelsAMythosNamedRequestReadsOnlyTheFiveHourWindow(t *testing.T) {
	e := New(testConfig(t), newHost(), (&clock{t0}).now, nil)
	e.OnUsage(usage(G, Extra, headers(40, 90, 5, 3, t0)))
	readings := accountView(e.Status(), G).Readings
	if _, ok := readings[quota.Main]; ok {
		t.Fatalf("a model in no bucket and no extra model leaves the week alone: %+v", readings)
	}
	if _, ok := readings[quota.Session]; !ok {
		t.Fatalf("the 5-hour window is read from every request: %+v", readings)
	}
}

// The Fable/Mythos share is read only from the usage endpoint, so a configuration that serves
// Mythos keeps that reading fresh even with no Fable bucket: a missing one makes the account due.
func TestAMythosModelKeepsTheFableShareReadingFresh(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra bool
		probe bool
	}{{"with an extra model", true, true}, {"without one", false, false}} {
		cfg := testConfig(t)
		cfg.Buckets.Fable = nil
		if c.extra {
			cfg.ExtraModels = mythosConfig(t).ExtraModels
		}
		h := newHost()
		e := New(cfg, h, (&clock{t0}).now, nil)
		e.refreshCredentials()
		e.OnUsage(usage(G, Opus, headers(40, 90, 5, 3, t0)))
		e.mu.Lock()
		e.st.Account(G).LastProbe = t0.Add(-time.Hour)
		e.mu.Unlock()
		e.probeDue(t0, nil)
		if got := slices.Contains(h.httpCalls, "tok-g-secret"); got != c.probe {
			t.Errorf("%s: probed = %v, want %v", c.name, got, c.probe)
		}
	}
}

func TestTheMeterMarksMythosAsDrawingOnTheFableLimit(t *testing.T) {
	e := New(mythosConfig(t), newHost(), (&clock{t0}).now, nil)
	v := e.Status()
	e.AddSessions(&v, history.Sessions{})
	if !slices.Equal(v.Meter.FableModels, []string{Fable, Extra}) {
		t.Fatalf("fable models = %v", v.Meter.FableModels)
	}
	plain := New(testConfig(t), newHost(), (&clock{t0}).now, nil)
	v = plain.Status()
	plain.AddSessions(&v, history.Sessions{})
	if !slices.Equal(v.Meter.FableModels, []string{Fable}) {
		t.Fatalf("without extra models: fable models = %v", v.Meter.FableModels)
	}
}

func TestTheFableShareWarnsAtTheThresholdAndNotBelow(t *testing.T) {
	e := New(mythosConfig(t), newHost(), (&clock{t0}).now, nil)
	fableProbe(e, W, 79.9, 50, t0)
	fableProbe(e, G, 80, 50, t0)
	warnings := e.Status().FableWarnings
	if len(warnings) != 1 || warnings[0].AuthFile != G || warnings[0].Util != 80 {
		t.Fatalf("warnings = %+v", warnings)
	}
	want := "Mythos/Fable share on claude-g at 80% — consider switching the advisor to a non-Fable model until the reset"
	if warnings[0].Message != want {
		t.Fatalf("message = %q, want %q", warnings[0].Message, want)
	}
	fableProbe(e, W, 93.6, 50, t0)
	if got := e.Status().FableWarnings; len(got) != 2 || !strings.Contains(got[0].Message, " claude-w at 94%") {
		t.Fatalf("one line per account, in the accounts' order: %+v", got)
	}
}

func TestTheFableWarningFollowsItsThreshold(t *testing.T) {
	cfg := testConfig(t)
	cfg.Thresholds.FableWarn = 95
	e := New(cfg, newHost(), (&clock{t0}).now, nil)
	fableProbe(e, G, 90, 50, t0)
	if got := e.Status().FableWarnings; len(got) != 0 {
		t.Fatalf("below a raised threshold: %+v", got)
	}
	fableProbe(e, G, 95, 50, t0)
	if got := e.Status().FableWarnings; len(got) != 1 {
		t.Fatalf("at a raised threshold: %+v", got)
	}
}

// After a reset the balancer estimates the new week at 0 until a real reading comes; an account
// with no Fable reading has nothing to warn about.
func TestNoFableWarningWithoutAReadingOrAfterTheReset(t *testing.T) {
	clk := &clock{t0}
	e := New(testConfig(t), newHost(), clk.now, nil)
	if got := e.Status().FableWarnings; len(got) != 0 {
		t.Fatalf("no reading: %+v", got)
	}
	fableProbe(e, G, 90, 1, t0)
	clk.t = t0.Add(2 * time.Hour)
	e.mu.Lock()
	e.recompute(clk.t)
	e.mu.Unlock()
	if got := e.Status().FableWarnings; len(got) != 0 {
		t.Fatalf("after the reset: %+v", got)
	}
}
