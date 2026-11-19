package engine

import (
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/state"
)

// fillWorld has three accounts: W and X without a reserve (cap 99), G with the default one.
func fillWorld(t *testing.T) *world {
	w := newWorld(t, W, G, X)
	w.cfg.Fill.Enabled = true
	return w
}

// usual is the case fill is for: W resets in 6 days and is barely used, G in 18 hours and is barely
// used, X in 10 hours with 10 points left under its cap.
func (w *world) usual() {
	w.use(W, 30, 144, 0)
	w.use(G, 5, 18, 0)
	w.use(X, 89, 10, 0)
}

func (w *world) pickOf(id string) SessionPick { return w.e.Status().Affinity.Sessions[id] }

func TestFillPutsANewProjectOnTheEarliestResetWithRoom(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	w.folder("-work-beta", 2)
	w.start()
	w.usual()
	if got := w.pick(1); got != X {
		t.Fatalf("the account that resets first and has room takes the project, got %q", got)
	}
	p := w.pickOf(sidN(1))
	if p.Reason != ReasonFill || p.Fill == nil || !p.Fill.ResetsAt.Equal(t0.Add(10*time.Hour)) || p.Fill.Room != 10 {
		t.Fatalf("the pick says it was fill, with the reset and the room: %+v %+v", p, p.Fill)
	}
	if w.home("-work-alpha") != X {
		t.Fatalf("the project's home is the fill account, got %q", w.home("-work-alpha"))
	}
	// X is full: the next new project goes to the next reset, never to the one 6 days away
	w.use(X, 99, 10, 0)
	if got := w.pick(2); got != G {
		t.Fatalf("with X full the next reset takes the project, got %q", got)
	}
}

func TestFillNeverPrefersAnAccountWithoutAReading(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 0, 144, 0) // W has the most room: only fill puts the project on G
	w.use(G, 5, 18, 0)  // X has no reading at all
	if got := w.pick(1); got != G || w.home("-work-alpha") != G || w.reason(sidN(1)) != ReasonFill {
		t.Fatalf("an account with no reading is never preferred: %q, home %q (%s)", got, w.home("-work-alpha"), w.reason(sidN(1)))
	}
}

func TestFillNeverPrefersAnAccountWithAStaleReading(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	w.start()
	w.use(X, 10, 20, 0)
	w.clock.t = w.clock.t.Add(7 * time.Hour) // X's reading is older than stale_hours
	w.use(W, 0, 144, 0)
	w.use(G, 5, 18, 0)
	if got := w.pick(1); got != G || w.home("-work-alpha") != G || w.reason(sidN(1)) != ReasonFill {
		t.Fatalf("a stale reading is never preferred: %q, home %q (%s)", got, w.home("-work-alpha"), w.reason(sidN(1)))
	}
}

func TestFolderAndSessionPinsWinOverFill(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-pinned", 1)
	w.folder("-work-alpha", 2)
	w.start()
	w.usual()
	if err := w.e.SetPin(state.PinFolder, "-work-pinned", W); err != nil {
		t.Fatal(err)
	}
	if err := w.e.SetPin(state.PinSession, sidN(2), G); err != nil {
		t.Fatal(err)
	}
	if got := w.pick(1); got != W || w.reason(sidN(1)) != ReasonFolderPin {
		t.Fatalf("a folder pin wins over fill: %q (%s)", got, w.reason(sidN(1)))
	}
	if got := w.pick(2); got != G || w.reason(sidN(2)) != ReasonSessionPin {
		t.Fatalf("a session pin wins over fill: %q (%s)", got, w.reason(sidN(2)))
	}
}

func TestAFillHomeStaysUntilItsAccountIsFull(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1, 2, 3)
	w.start()
	w.usual()
	if w.pick(1) != X {
		t.Fatal("setup: the project starts on X")
	}
	// G now resets before X, but X has room: a new session of the project stays on its home
	w.use(G, 5, 8, 0)
	if got := w.pick(2); got != X || w.reason(sidN(2)) != ReasonProject {
		t.Fatalf("the home stays while its account has room: %q (%s)", got, w.reason(sidN(2)))
	}
	w.use(X, 99, 10, 0)
	if got := w.pick(3); got != G || w.reason(sidN(3)) != ReasonFill {
		t.Fatalf("once the home is full, fill places the project again: %q (%s)", got, w.reason(sidN(3)))
	}
}

// pastReset turns X's week over: 11 hours on, X has a fresh week, and G resets first.
func (w *world) pastReset() {
	w.clock.t = w.clock.t.Add(11 * time.Hour)
	w.use(W, 30, 133, 0)
	w.use(G, 5, 7, 0)
	w.use(X, 1, 167, 0) // the next reset, a week after the one 10 hours from the start
}

func TestANewSessionPlacesTheProjectAgainOnceItsHomeHasReset(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.usual()
	if w.pick(1) != X {
		t.Fatal("setup: the project starts on X")
	}
	w.pastReset()
	if got := w.pick(2); got != G || w.reason(sidN(2)) != ReasonFill {
		t.Fatalf("a session that starts after the home's reset goes to fill: %q (%s)", got, w.reason(sidN(2)))
	}
	if w.home("-work-alpha") != G {
		t.Fatalf("the project's home moved to G, got %q", w.home("-work-alpha"))
	}
}

func TestARunningSessionIsNeverMovedByFill(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.usual()
	if w.pick(1) != X {
		t.Fatal("setup: the project starts on X")
	}
	w.pastReset()
	w.pick(2) // a new session moves the project's home to G
	if got := w.pick(1); got != X {
		t.Fatalf("the running session keeps its account and its prompt cache, got %q", got)
	}
	// nor does a new subagent of the running session move it
	if got := w.e.Pick(w.req(Opus, 1, "ab12", W, G, X)).AuthID; got != X {
		t.Fatalf("a subagent of a running session follows it, got %q", got)
	}
}

func TestARunningSessionMovesWhenItsAccountIsFull(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	w.start()
	w.usual()
	if w.pick(1) != X {
		t.Fatal("setup: the project starts on X")
	}
	w.use(X, 99, 10, 0)
	if got := w.pick(1); got != G {
		t.Fatalf("a full account cannot take the session: it goes to the next reset, got %q", got)
	}
}

func TestANewSessionPlacesTheProjectAgainWhenItsHomeIsHeldBack(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.usual()
	if w.pick(1) != X {
		t.Fatal("setup: the project starts on X")
	}
	w.use(X, 89, 10, 98) // X's 5-hour window is at its cap
	if got := w.pick(2); got != G || w.reason(sidN(2)) != ReasonFill {
		t.Fatalf("a session that starts while the home is held back goes to fill: %q (%s)", got, w.reason(sidN(2)))
	}
	if got := w.pick(1); got == X {
		t.Fatal("the held back account cannot take the running session's request")
	}
	if w.reason(sidN(1)) != ReasonFallback {
		t.Fatalf("the running session takes a detour, got %s", w.reason(sidN(1)))
	}
}

func TestRebalanceAgreesWithFill(t *testing.T) {
	w := fillWorld(t)
	for i, name := range []string{"-work-a", "-work-b", "-work-c"} {
		w.folder(name, i+1)
	}
	w.start()
	w.usual()
	for _, name := range []string{"-work-a", "-work-b", "-work-c"} {
		w.e.st.SetHome(name, W, w.clock.t) // placed before fill, on the account that resets last
	}
	res := w.e.Rebalance(true)
	if len(res.Plan) != 3 {
		t.Fatalf("the plan holds the three projects: %+v", res.Plan)
	}
	for _, m := range res.Plan {
		if m.To != X {
			t.Fatalf("the plan puts every project on the account fill would choose: %+v", res.Plan)
		}
	}
	if homes := w.e.Status().Affinity.Homes; len(homes) != 3 {
		t.Fatalf("three homes: %+v", homes)
	}
	for _, h := range w.e.Status().Affinity.Homes {
		if h.Fill == nil || h.Fill.Room != 10 {
			t.Fatalf("a home the plan moved says what fill saw: %+v", h)
		}
	}
	w.folder("-work-new", 9)
	if got := w.pick(9); got != X {
		t.Fatalf("fill chooses the plan's account for a new project, got %q", got)
	}
	again := w.e.Rebalance(false)
	if len(again.Plan) != 4 {
		t.Fatalf("the second plan holds the four projects: %+v", again.Plan)
	}
	for _, m := range again.Plan {
		if m.From != m.To {
			t.Fatalf("a second plan moves nothing: %+v", again.Plan)
		}
	}
}

func TestFillOffPlacesANewProjectByRoomAsBefore(t *testing.T) {
	for _, on := range []bool{false, true} {
		w := newWorld(t, W, G)
		w.cfg.Fill.Enabled = on
		w.folder("-work-alpha", 1)
		w.start()
		w.twoAccounts() // W has the most room, G resets first
		want := W
		if on {
			want = G
		}
		if got := w.pick(1); got != want {
			t.Fatalf("fill %v: got %q, want %q", on, got, want)
		}
		if !on && w.reason(sidN(1)) != ReasonProject {
			t.Fatalf("with fill off the reason is the project home, got %s", w.reason(sidN(1)))
		}
	}
}

func TestARebuiltEngineKeepsWhereRunningSessionsStarted(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.usual()
	if w.pick(1) != X {
		t.Fatal("setup: the project starts on X")
	}
	// a config change builds a new engine from the saved state
	w.e.Shutdown()
	old := w.e
	w.start()
	w.e.Inherit(old)
	if w.reason(sidN(1)) != ReasonFill {
		t.Fatalf("the picks are kept: %q", w.reason(sidN(1)))
	}
	w.pastReset()
	w.pick(2) // a new session moves the project's home to G
	if got := w.pick(1); got != X {
		t.Fatalf("the running session keeps its account across the rebuild, got %q", got)
	}
}

func TestFillSkipsAnAccountWithLessThanTheMinimumRoom(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	for i, name := range []string{"-work-a", "-work-b"} {
		w.folder(name, i+10)
	}
	w.start()
	w.use(W, 30, 144, 0)
	w.use(G, 5, 18, 0)
	w.use(X, 98, 10, 0) // 1 point of room under its cap of 99: below min_room 10
	if got := w.pick(1); got != G || w.reason(sidN(1)) != ReasonFill {
		t.Fatalf("an account with 1%% room is not a fill target: %q (%s)", got, w.reason(sidN(1)))
	}
	for _, name := range []string{"-work-a", "-work-b"} {
		w.e.st.SetHome(name, W, w.clock.t)
	}
	res := w.e.Rebalance(false)
	if len(res.Plan) != 3 {
		t.Fatalf("plan: %+v", res.Plan)
	}
	for _, m := range res.Plan {
		if m.To != G {
			t.Fatalf("the rebalance keeps the same floor: %+v", res.Plan)
		}
	}
}

func TestBelowTheMinimumRoomEverywhereTheBalancingRuleDecides(t *testing.T) {
	w := newWorld(t, W, X)
	w.cfg.Fill.Enabled = true
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 98.5, 144, 0)
	w.use(X, 98, 10, 0)
	if got := w.pick(1); got != X || w.reason(sidN(1)) != ReasonProject {
		t.Fatalf("the most room takes the project, not by fill: %q (%s)", got, w.reason(sidN(1)))
	}
}

func TestAnEstimateAfterAResetIsNotAFreshReadingForFill(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	w.start()
	w.use(X, 50, 10, 0)
	w.clock.t = w.clock.t.Add(11 * time.Hour) // X reset: its reading is now an estimate, 167 h away
	w.use(W, 30, 167.5, 0)
	w.use(G, 99, 100, 0) // full
	if got := w.pick(1); got != W {
		t.Fatalf("an estimate is not preferred over a real reading, got %q", got)
	}
}

func TestFillConsidersOnlyTheAccountsCPAOffers(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-alpha", 1)
	w.start()
	w.usual()
	if got := w.e.Pick(w.req(Opus, 1, "", W, G)).AuthID; got != G || w.reason(sidN(1)) != ReasonFill || w.home("-work-alpha") != G {
		t.Fatalf("X is not offered: fill places the project on G, got %q (%s), home %q", got, w.reason(sidN(1)), w.home("-work-alpha"))
	}
}

func TestAPinThatFallsBackUsesFillsOrder(t *testing.T) {
	w := fillWorld(t)
	w.folder("-work-pinned", 1)
	w.start()
	w.usual()
	if err := w.e.SetPin(state.PinFolder, "-work-pinned", W); err != nil {
		t.Fatal(err)
	}
	w.use(W, 30, 144, 98) // the pinned account's 5-hour window is at its cap
	if got := w.pick(1); got != X || w.reason(sidN(1)) != ReasonFolderPinFallback {
		t.Fatalf("the fallback takes the first reset with room: %q (%s)", got, w.reason(sidN(1)))
	}
}
