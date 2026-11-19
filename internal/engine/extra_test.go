package engine

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
)

const Extra = "claude-extra-1"

// extraWorld pins Extra to G, the account the balancer would not choose (W has the most room).
func extraWorld(t *testing.T) *world {
	w := newWorld(t, W, G, X)
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{G}, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	return w
}

func refused(r pluginapi.SchedulerPickResponse) bool {
	return r.Handled && r.Reject && r.AuthID == "" && r.RejectCode == ExtraModelUnavailable &&
		strings.Contains(r.RejectReason, Extra)
}

func TestAnExtraModelGoesOnlyToItsAccount(t *testing.T) {
	w := extraWorld(t)
	for _, model := range []string{Extra, Extra + "(high)", "p/" + Extra} {
		if got := w.e.Pick(w.req(model, 0, "", W, G, X)); got.AuthID != G || !got.Handled || got.Reject {
			t.Fatalf("%s: pick = %+v, want %s", model, got, G)
		}
	}
}

func TestAnExtraModelIsRefusedWithoutItsAccount(t *testing.T) {
	w := extraWorld(t)
	if got := w.e.Pick(w.req(Extra, 0, "", W, X)); !refused(got) {
		t.Fatalf("pick = %+v, want a refusal naming the model", got)
	}
	if got := w.e.Pick(w.req(Extra, 0, "")); !refused(got) {
		t.Fatalf("no candidates: pick = %+v, want a refusal", got)
	}
}

func TestAnExtraModelTakesItsAccountsInOrder(t *testing.T) {
	w := newWorld(t, W, G, X)
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{X, G}, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.start()
	if got := w.e.Pick(w.req(Extra, 0, "", W, G, X)).AuthID; got != X {
		t.Fatalf("the first account is tried first, got %q", got)
	}
	if got := w.e.Pick(w.req(Extra, 0, "", W, G)).AuthID; got != G {
		t.Fatalf("the next account takes it while the first is out, got %q", got)
	}
}

func TestAnExtraModelIgnoresSessionAndFolderPins(t *testing.T) {
	w := extraWorld(t)
	if err := w.e.SetPin("folder", "-work-alpha", W); err != nil {
		t.Fatal(err)
	}
	if err := w.e.SetPin("session", sidN(1), X); err != nil {
		t.Fatal(err)
	}
	if got := w.e.Pick(w.req(Extra, 1, "", W, G, X)).AuthID; got != G {
		t.Fatalf("session and folder pins must not move an extra model, got %q", got)
	}
	if _, placed := w.e.Status().Affinity.Sessions[sidN(1)]; placed {
		t.Fatal("an extra model's pick is not a placement of the session")
	}
	if got := w.e.Pick(w.req(Extra, 1, "", W, X)); !refused(got) {
		t.Fatalf("pinned elsewhere and without its account: pick = %+v, want a refusal", got)
	}
}

func TestAnExtraModelIgnoresCPAsOwnPinToAnotherAccount(t *testing.T) {
	w := extraWorld(t)
	r := w.req(Extra, 0, "", W, G, X)
	r.Options.Metadata = map[string]any{"pinned_auth_id": W}
	if got := w.e.Pick(r); !refused(got) {
		t.Fatalf("a request CPA pinned to another account: pick = %+v, want a refusal", got)
	}
	r.Options.Metadata["pinned_auth_id"] = G
	if got := w.e.Pick(r).AuthID; got != G {
		t.Fatalf("a request CPA pinned to the model's account goes there, got %q", got)
	}
}

func TestAnExtraModelIsPinnedWhilePausedAndInShadowMode(t *testing.T) {
	for _, set := range []func(*config.Config){
		func(c *config.Config) { c.Paused = true },
		func(c *config.Config) { c.Mode = "shadow" },
	} {
		w := newWorld(t, W, G)
		w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{G}, ContextLength: 1000, MaxCompletionTokens: 100}}
		set(&w.cfg)
		w.start()
		if got := w.e.Pick(w.req(Extra, 0, "", W, G)).AuthID; got != G {
			t.Fatalf("paused %v, mode %s: got %q", w.cfg.Paused, w.cfg.Mode, got)
		}
		if got := w.e.Pick(w.req(Extra, 0, "", W)); !refused(got) {
			t.Fatalf("paused %v, mode %s: pick = %+v, want a refusal", w.cfg.Paused, w.cfg.Mode, got)
		}
	}
}

func TestOtherModelsAreNotTouchedByExtraModels(t *testing.T) {
	plain := newWorld(t, W, G, X)
	plain.folder("-work-alpha", 1)
	plain.start()
	plain.twoAccounts()
	w := extraWorld(t)
	for _, session := range []int{0, 1} {
		want := plain.e.Pick(plain.req(Opus, session, "", W, G, X))
		if got := w.e.Pick(w.req(Opus, session, "", W, G, X)); got != want || got.AuthID == "" {
			t.Fatalf("session %d: a bucket model is balanced as before: got %+v, want %+v", session, got, want)
		}
	}
}
