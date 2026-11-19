package engine

import (
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// folderPinWorld is pinnedWorld with readings that make X the account that resets first: W has the
// most room and resets last, G keeps a reserve and resets in between. Sessions 1 and 2 are in
// -work-alpha.
func folderPinWorld(t *testing.T) *world {
	w := pinnedWorld(t)
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	return w
}

func (w *world) pinFolder(folder, account string) {
	w.t.Helper()
	if err := w.e.SetPin(state.PinFolder, folder, account); err != nil {
		w.t.Fatal(err)
	}
}

func TestAFolderPinInTheSetTakesAnExtraModelRequestEvenWhenAnotherAccountResetsFirst(t *testing.T) {
	w := folderPinWorld(t)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("setup: without a pin the first reset is chosen, got %q", got)
	}
	w.pinFolder("-work-alpha", W)
	if got := w.pinnedPick(2); got != W {
		t.Fatalf("the folder's pinned account is tried first, got %q", got)
	}
	if _, placed := w.e.Status().Affinity.Sessions[sidN(2)]; placed {
		t.Fatal("an extra model's pick is still not a placement of the session")
	}
}

func TestAFolderPinFromTheConfigTakesAnExtraModelRequest(t *testing.T) {
	w := newWorld(t, W, G, X)
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{W, G, X}, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.cfg.Affinity.FolderPins = map[string]string{"-work-alpha": G}
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	if got := w.pinnedPick(1); got != G {
		t.Fatalf("got %q", got)
	}
}

func TestAFolderPinThatCannotTakeTheRequestFallsBackToTheEarliestReset(t *testing.T) {
	cases := map[string]struct {
		pin   string
		setup func(w *world)
	}{
		"at its weekly cap":             {W, func(w *world) { w.use(W, 99, 140, 0) }},
		"at its 5-hour cap":             {W, func(w *world) { w.use(W, 10, 140, 98) }},
		"held back by its reserve only": {G, func(w *world) { w.use(G, 86, 90, 0) }}, // over G's reserve cap of 85
		"no fresh reading": {G, func(w *world) {
			w.clock.t = w.clock.t.Add(48 * time.Hour) // every reading is stale
			w.use(W, 10, 140, 0)
			w.use(X, 80, 20, 0) // fresh again; G stays stale
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := folderPinWorld(t)
			c.setup(w)
			w.pinFolder("-work-alpha", c.pin)
			if got := w.pinnedPick(1); got != X {
				t.Fatalf("the pin %s cannot take it: the earliest reset does, got %q", c.pin, got)
			}
		})
	}
}

func TestAFolderPinThatCPADoesNotOfferFallsBack(t *testing.T) {
	w := folderPinWorld(t)
	w.pinFolder("-work-alpha", W)
	if got := w.e.Pick(w.req(Extra, 1, "", G, X)).AuthID; got != X {
		t.Fatalf("got %q", got)
	}
}

func TestAFolderPinOutsideTheSetIsIgnored(t *testing.T) {
	w := newWorld(t, W, G, X)
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{G, X}, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	w.pinFolder("-work-alpha", W)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("got %q", got)
	}
}

func TestASessionPinInTheSetOutranksTheFolderPin(t *testing.T) {
	w := folderPinWorld(t)
	w.pinFolder("-work-alpha", W)
	if err := w.e.SetPin(state.PinSession, sidN(1), G); err != nil {
		t.Fatal(err)
	}
	if got := w.pinnedPick(1); got != G {
		t.Fatalf("the session's pin wins, got %q", got)
	}
	if got := w.pinnedPick(2); got != W {
		t.Fatalf("another session of the folder follows the folder's pin, got %q", got)
	}
}

func TestASessionPinThatCannotTakeTheRequestDoesNotPassItToTheFolderPin(t *testing.T) {
	w := folderPinWorld(t)
	w.pinFolder("-work-alpha", W)
	if err := w.e.SetPin(state.PinSession, sidN(1), G); err != nil {
		t.Fatal(err)
	}
	w.use(G, 86, 90, 0)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("as on the normal path the session pin alone decides; then the tiers rule, got %q", got)
	}
}

func TestASessionStaysOnItsAccountWhenItsFolderPinBecomesUsable(t *testing.T) {
	w := folderPinWorld(t)
	w.use(W, 99, 140, 0)
	w.pinFolder("-work-alpha", W)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("setup: the pin is full, the session starts on X, got %q", got)
	}
	w.use(W, 10, 140, 0)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("the session keeps its account and its prompt cache, got %q", got)
	}
	if got := w.pinnedPick(2); got != W {
		t.Fatalf("a session that starts now goes to the pin, got %q", got)
	}
}

func TestASessionOnItsFolderPinStaysThereWhileAnotherAccountResetsFirst(t *testing.T) {
	w := folderPinWorld(t)
	w.pinFolder("-work-alpha", W)
	for i := 0; i < 2; i++ {
		if got := w.pinnedPick(1); got != W {
			t.Fatalf("request %d: got %q", i, got)
		}
	}
}

func TestAFullAccountMovesASessionToTheFolderPinNotToTheFirstReset(t *testing.T) {
	w := folderPinWorld(t)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("setup: the session starts on X, got %q", got)
	}
	w.pinFolder("-work-alpha", W)
	w.use(X, 99, 20, 0)
	if got := w.pinnedPick(1); got != W {
		t.Fatalf("a session that has to move goes to the pin, got %q", got)
	}
	w.use(X, 5, 188, 0)
	w.use(G, 52, 10, 0) // G now resets first
	if got := w.pinnedPick(1); got != W {
		t.Fatalf("and stays there, got %q", got)
	}
}

func TestADetourFromAnAccountThatIsHeldBackForAWhileGoesToTheFolderPin(t *testing.T) {
	w := folderPinWorld(t)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("setup: the session starts on X, got %q", got)
	}
	w.pinFolder("-work-alpha", W)
	w.use(X, 80, 20, 98) // a 5-hour cap: a detour; G resets before W
	if got := w.pinnedPick(1); got != W {
		t.Fatalf("the detour goes to the folder's pin, got %q", got)
	}
	w.use(X, 80, 20, 5)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("then the session is back on X, got %q", got)
	}
}

func TestTheFolderPinOfAMainRepositoryHoldsForAnExtraModelRequestFromItsWorktrees(t *testing.T) {
	w := newWorld(t, W, G, X)
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{W, G, X}, ContextLength: 1000, MaxCompletionTokens: 100}}
	g := newGitTree(t)
	g.folders(w)
	w.start()
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	w.pinFolder(g.main, G)
	w.pinFolder(g.a, W)
	for n, want := range map[int]string{1: G, 2: W, 3: G} {
		if got := w.pinnedPick(n); got != want {
			t.Fatalf("session %d: got %q, want %q", n, got, want)
		}
	}
}

func TestTheFolderPinOfAHeaderTaggedSessionTakesAnExtraModelRequest(t *testing.T) {
	w := folderPinWorld(t)
	w.pinFolder(rootFolder, W)
	r := w.req(Extra, 7, "", W, G, X)
	r.Options.Headers["X-Wapps-Project-Root"] = []string{"/home/u/proj"}
	if got := w.e.Pick(r).AuthID; got != W {
		t.Fatalf("got %q", got)
	}
	if got := w.e.Pick(w.req(Extra, 8, "", W, G, X)).AuthID; got != X {
		t.Fatalf("a session with no project has no pin, got %q", got)
	}
}

func TestAFolderPinTakesARequestPinnedByTheAdvisorInTheSet(t *testing.T) {
	w := advisorWorld(t, false)
	w.pinFolder("-work-alpha", G)
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G, X)); got.AuthID != G || got.Reject {
		t.Fatalf("the advisor's accounts are X and G; the pin names G: %+v", got)
	}
	w = advisorWorld(t, false)
	w.pinFolder("-work-alpha", W)
	if got := w.e.Pick(w.withBeta(Opus, 1, advisorBeta, W, G, X)); got.AuthID != X {
		t.Fatalf("a pin outside the advisor's accounts is ignored: %+v", got)
	}
}

func TestTheFolderPinDoesNotMoveAnExtraModelRequestWhilePausedOrInShadowMode(t *testing.T) {
	for _, set := range []func(*config.Config){
		func(c *config.Config) { c.Paused = true },
		func(c *config.Config) { c.Mode = "shadow" },
	} {
		w := newWorld(t, W, G, X)
		w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{W, G, X}, ContextLength: 1000, MaxCompletionTokens: 100}}
		w.cfg.Affinity.FolderPins = map[string]string{"-work-alpha": W}
		set(&w.cfg)
		w.folder("-work-alpha", 1)
		w.start()
		w.use(W, 10, 140, 0)
		w.use(G, 52, 90, 0)
		w.use(X, 80, 20, 0)
		if got := w.pinnedPick(1); got != X {
			t.Fatalf("paused %v, mode %s: the placement rules are off, got %q", w.cfg.Paused, w.cfg.Mode, got)
		}
	}
}
