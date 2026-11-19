package engine

import (
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/config"
)

// pinnedWorld pins Extra to W, G and X, in that order in the config. W has no reserve and resets
// last, G keeps a reserve (cap 85 while its reset is more than 48 h away), X resets first.
func pinnedWorld(t *testing.T) *world {
	w := newWorld(t, W, G, X)
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{W, G, X}, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.folder("-work-alpha", 1, 2)
	w.start()
	return w
}

func (w *world) pinnedPick(session int) string {
	return w.e.Pick(w.req(Extra, session, "", W, G, X)).AuthID
}

func TestThePinnedAccountsAreTakenByTheirReset(t *testing.T) {
	w := pinnedWorld(t)
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	if got := w.pinnedPick(0); got != X {
		t.Fatalf("the pinned account that resets first takes the request, got %q", got)
	}
	w.use(X, 99, 20, 0)
	if got := w.pinnedPick(0); got != G {
		t.Fatalf("then the next reset, got %q", got)
	}
}

func TestAnAccountWithoutAReadingIsTakenLast(t *testing.T) {
	w := pinnedWorld(t) // W has no reading
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	if got := w.pinnedPick(0); got != X {
		t.Fatalf("an account with a reading comes first, got %q", got)
	}
	w.use(G, 99, 90, 0)
	w.use(X, 99, 20, 0)
	if got := w.pinnedPick(0); got != W {
		t.Fatalf("an account with no reading is tried before a refusal, got %q", got)
	}
}

func TestAFullOrCappedPinnedAccountIsSkipped(t *testing.T) {
	w := pinnedWorld(t)
	w.use(W, 10, 140, 0)
	w.use(G, 86, 90, 0)  // over its reserve's cap of 85
	w.use(X, 10, 20, 98) // its 5-hour window is at the cap
	if got := w.pinnedPick(0); got != W {
		t.Fatalf("an account held by its reserve or its 5-hour cap is skipped, got %q", got)
	}
	w.use(W, 99, 140, 0)
	w.use(X, 99, 20, 0)
	if got := w.pinnedPick(0); got != G {
		t.Fatalf("the reserve is released when no account has room under its caps, got %q", got)
	}
}

func TestASessionStaysOnItsPinnedAccountUntilItIsFull(t *testing.T) {
	w := pinnedWorld(t)
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 80, 20, 0)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("setup: session 1 starts on X, got %q", got)
	}
	w.use(G, 52, 10, 0) // G now resets first
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("the session keeps its account and its prompt cache, got %q", got)
	}
	if got := w.pinnedPick(2); got != G {
		t.Fatalf("a new session takes the first reset, got %q", got)
	}
	w.use(X, 80, 20, 98) // a 5-hour cap: a detour for the request, the session keeps X
	if got := w.pinnedPick(1); got != G {
		t.Fatalf("a request X cannot take now goes to the next account, got %q", got)
	}
	w.use(X, 80, 20, 5)
	if got := w.pinnedPick(1); got != X {
		t.Fatalf("after the detour the session is back on X, got %q", got)
	}
	w.use(X, 99, 20, 0)
	if got := w.pinnedPick(1); got != G {
		t.Fatalf("once X is full the session moves, got %q", got)
	}
	w.use(X, 5, 188, 0) // X reset; the session stays where it moved
	if got := w.pinnedPick(1); got != G {
		t.Fatalf("the session stays on its new account, got %q", got)
	}
}

func TestAllPinnedAccountsHardFullIsARefusal(t *testing.T) {
	w := pinnedWorld(t)
	w.use(W, 99, 140, 0)
	w.use(G, 99, 90, 0)
	w.use(X, 10, 20, 98) // 5-hour cap
	if got := w.e.Pick(w.req(Extra, 1, "", W, G, X)); !refused(got) {
		t.Fatalf("pick = %+v, want the refusal", got)
	}
}

func TestOnlyReservesHoldingThePinnedAccountsBackUsesOneWithRoomUnderTheFinalCap(t *testing.T) {
	w := newWorld(t, W, G, X)
	reserve := config.Account{Reserve: config.ReserveCustom, ReserveSteps: []config.ReserveStep{{Cap: 50, Hours: 48}}}
	w.cfg.Accounts = nil
	for _, a := range []string{W, G, X} {
		r := reserve
		r.AuthFile = a
		w.cfg.Accounts = append(w.cfg.Accounts, r)
	}
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: []string{W, G, X}, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.start()
	w.use(W, 60, 140, 0)
	w.use(G, 99, 70, 0) // at the final cap: hard full
	w.use(X, 70, 90, 0)
	// W and X are over their reserve (50 more than 48 h before the reset), still under the final cap
	if got := w.pinnedPick(0); got != X {
		t.Fatalf("the first reset with room under the final cap takes it, got %q", got)
	}
}
