package engine

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

const (
	Advisor     = "claude-mythos-9-2" // the advisor's model, served by G and X only
	advisorBeta = "oauth-2025-04-20,advisor-tool-2026-03-01,fine-grained-tool-streaming-2025-05-14"
)

// advisorWorld pins the advisor to X and G; X resets first of them. W has the most room and resets
// first of all, so both the balancing rule and fill would choose W.
func advisorWorld(t *testing.T, fill bool) *world {
	w := newWorld(t, W, G, X)
	w.cfg.Fill.Enabled = fill
	w.cfg.ExtraModels = []config.ExtraModel{
		{ID: Extra, Accounts: []string{G}, ContextLength: 1000, MaxCompletionTokens: 100},
		{ID: Advisor, Accounts: []string{X, G}, ContextLength: 1000, MaxCompletionTokens: 100},
	}
	w.cfg.AdvisorPin = &config.AdvisorPin{Model: Advisor, BetaPrefix: config.DefaultAdvisorBeta}
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 10, 15, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	return w
}

// withBeta is a request of session n (0: none) for model with the given anthropic-beta value.
func (w *world) withBeta(model string, session int, beta string, accounts ...string) pluginapi.SchedulerPickRequest {
	r := w.req(model, session, "", accounts...)
	if beta != "" {
		r.Options.Headers["anthropic-beta"] = []string{beta}
	}
	return r
}

func advisorRefused(r pluginapi.SchedulerPickResponse) bool {
	return r.Handled && r.Reject && r.AuthID == "" && r.RejectCode == ExtraModelUnavailable && strings.Contains(r.RejectReason, Advisor)
}

func TestARequestWithTheAdvisorGoesOnlyToTheAdvisorsAccounts(t *testing.T) {
	w := advisorWorld(t, false)
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G, X)); got.AuthID != X || got.Reject {
		t.Fatalf("the advisor's first reset takes it: %+v", got)
	}
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G)); got.AuthID != G || got.Reject {
		t.Fatalf("the next of its accounts takes it while the first is out: %+v", got)
	}
	if _, placed := w.e.Status().Affinity.Sessions[sidN(1)]; placed {
		t.Fatal("a pinned request is not a placement of the session")
	}
}

func TestTheAdvisorBetaMatchesInAnyCaseAndWithSpaces(t *testing.T) {
	w := advisorWorld(t, false)
	for _, beta := range []string{"Advisor-Tool-2026-03-01", "oauth-2025-04-20 ,  ADVISOR-TOOL-2026-03-01 , x"} {
		r := w.withBeta(Opus, 1, "", W, G, X)
		r.Options.Headers["Anthropic-Beta"] = []string{beta}
		if got := w.e.Pick(r).AuthID; got != X {
			t.Fatalf("%q: got %q, want the advisor's account", beta, got)
		}
	}
}

func TestARequestWithoutTheAdvisorIsPlacedAsBefore(t *testing.T) {
	w := advisorWorld(t, false)
	if got := w.e.Pick(w.withBeta(Opus, 1, "oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14", W, G, X)).AuthID; got != W {
		t.Fatalf("the project goes to the account with the most room, got %q", got)
	}
	if w.reason(sidN(1)) != ReasonProject {
		t.Fatalf("reason %s", w.reason(sidN(1)))
	}
}

func TestTheAdvisorIsRefusedWhenNoneOfItsAccountsIsAvailable(t *testing.T) {
	w := advisorWorld(t, false)
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W)); !advisorRefused(got) {
		t.Fatalf("pick = %+v, want the extra model's refusal naming the advisor's model", got)
	}
	r := w.withBeta(Opus, 1, advisorBeta, W, G, X)
	r.Options.Metadata = map[string]any{"pinned_auth_id": W}
	if got := w.e.Pick(r); !advisorRefused(got) {
		t.Fatalf("CPA's own pin to another account is refused: %+v", got)
	}
}

func TestSessionAndFolderPinsDoNotMoveTheAdvisor(t *testing.T) {
	w := advisorWorld(t, false)
	if err := w.e.SetPin(state.PinFolder, "-work-alpha", W); err != nil {
		t.Fatal(err)
	}
	if err := w.e.SetPin(state.PinSession, sidN(1), W); err != nil {
		t.Fatal(err)
	}
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G, X)).AuthID; got != X {
		t.Fatalf("session and folder pins must not move the advisor, got %q", got)
	}
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W)); !advisorRefused(got) {
		t.Fatalf("pinned elsewhere and without the advisor's accounts: %+v", got)
	}
}

func TestFillDoesNotMoveTheAdvisor(t *testing.T) {
	w := advisorWorld(t, true)
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G, X)).AuthID; got != X {
		t.Fatalf("fill would choose W; the advisor goes to its first reset, got %q", got)
	}
	if got := w.e.Pick(w.withBeta(Opus, 1, "", W, G, X)).AuthID; got != W {
		t.Fatalf("without the advisor fill places the session, got %q", got)
	}
}

func TestTheAdvisorHoldsWhilePausedAndInShadowMode(t *testing.T) {
	for _, set := range []func(*config.Config){func(c *config.Config) { c.Paused = true }, func(c *config.Config) { c.Mode = "shadow" }} {
		w := newWorld(t, W, G, X)
		w.cfg.ExtraModels = []config.ExtraModel{{ID: Advisor, Accounts: []string{X}, ContextLength: 1000, MaxCompletionTokens: 100}}
		w.cfg.AdvisorPin = &config.AdvisorPin{Model: Advisor, BetaPrefix: config.DefaultAdvisorBeta}
		set(&w.cfg)
		w.start()
		if got := w.e.Pick(w.withBeta(Opus, 0, advisorBeta, W, G, X)).AuthID; got != X {
			t.Fatalf("got %q", got)
		}
	}
}

func TestAnExtraModelWithTheAdvisorIsStillPinnedToItsOwnAccounts(t *testing.T) {
	w := advisorWorld(t, false)
	if got := w.e.Pick(w.withBeta(Extra, 1, advisorBeta, W, G, X)).AuthID; got != G {
		t.Fatalf("the extra model's own accounts serve it, got %q", got)
	}
	if got := w.e.Pick(w.withBeta(Extra, 1, advisorBeta, W, X)); !refused(got) {
		t.Fatalf("without its own account the extra model is refused: %+v", got)
	}
}

func TestWithoutAnAdvisorPinTheBetaChangesNothing(t *testing.T) {
	w := advisorWorld(t, false)
	w.cfg.AdvisorPin = nil
	w.start()
	w.use(W, 10, 15, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G, X)).AuthID; got != W {
		t.Fatalf("the request is placed as any other, got %q", got)
	}
}

func TestTheStatusShowsTheAdvisorPinAndWarnsWhenAllItsAccountsAreOut(t *testing.T) {
	w := advisorWorld(t, false)
	v := w.e.Status().AdvisorPin
	if v == nil || v.Model != Advisor || v.BetaPrefix != config.DefaultAdvisorBeta || len(v.Accounts) != 2 || v.Accounts[0].AuthFile != X {
		t.Fatalf("advisor pin view = %+v", v)
	}
	w.e.Tick() // the host lists W and G only: X is not loaded
	if v := w.e.Status().AdvisorPin; v.AllOut || v.Accounts[0].State != "not loaded" || v.Accounts[1].State != "ready" {
		t.Fatalf("one account ready is no warning: %+v", v)
	}
	w.use(G, 90, 30, 0) // at its reserve's cap (90 under 48 h before the reset), under the final cap
	if v := w.e.Status().AdvisorPin; v.AllOut || v.Accounts[1].State != "reserve" {
		t.Fatalf("an account held only by its reserve still takes the advisor: %+v", v)
	}
	w.use(G, 99, 30, 0) // at the final cap
	if v := w.e.Status().AdvisorPin; !v.AllOut || v.Accounts[1].State != "full" {
		t.Fatalf("every account full or held back is a warning: %+v", v)
	}
}
