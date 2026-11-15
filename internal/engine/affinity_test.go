package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

const X = "claude-x.json" // a third account, for tests that need a choice

// sidN is a made-up Claude Code session id.
func sidN(n int) string { return fmt.Sprintf("%08d-2222-3333-4444-555555555555", n) }

type world struct {
	t        *testing.T
	projects string
	clock    *clock
	cfg      config.Config
	e        *Engine
}

// newWorld builds an engine with a projects directory. W has no reserve (cap 99), G keeps a
// default one (cap 85 while its reset is more than 48 h away).
func newWorld(t *testing.T, accounts ...string) *world {
	w := &world{t: t, projects: t.TempDir(), clock: &clock{t0}, cfg: testConfig(t)}
	if len(accounts) > 0 {
		w.cfg.Accounts = nil
		for _, a := range accounts {
			reserve := ""
			if a == G {
				reserve = config.ReserveDefault
			}
			w.cfg.Accounts = append(w.cfg.Accounts, config.Account{AuthFile: a, Reserve: reserve})
		}
	}
	w.cfg.Affinity.ProjectsDir = w.projects
	return w
}

func (w *world) start() *Engine {
	w.e = New(w.cfg, newHost(), w.clock.now, nil)
	return w.e
}

// folder puts a session's transcript file in a project folder; its content is never read.
func (w *world) folder(name string, sessions ...int) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Join(w.projects, name), 0o700); err != nil {
		w.t.Fatal(err)
	}
	for _, n := range sessions {
		if err := os.WriteFile(filepath.Join(w.projects, name, sidN(n)+".jsonl"), []byte("x\n"), 0o600); err != nil {
			w.t.Fatal(err)
		}
	}
}

// use sets an account's weekly and 5-hour readings, as a completed request would.
func (w *world) use(account string, week, resetH, fiveHour float64) {
	w.e.OnUsage(usage(account, Opus, headers(week, resetH, fiveHour, 3, w.clock.t)))
}

func (w *world) req(model string, session int, agent string, accounts ...string) pluginapi.SchedulerPickRequest {
	h := map[string][]string{"Content-Type": {"application/json"}}
	if session > 0 {
		h["X-Claude-Code-Session-Id"] = []string{sidN(session)}
	}
	if agent != "" {
		h["X-Claude-Code-Agent-Id"] = []string{agent}
	}
	r := pluginapi.SchedulerPickRequest{Providers: []string{"claude"}, Model: model, Options: pluginapi.SchedulerOptions{Headers: h}}
	for _, a := range accounts {
		r.Candidates = append(r.Candidates, pluginapi.SchedulerAuthCandidate{ID: a, Provider: "claude"})
	}
	return r
}

// pick asks for session n's main request on the opus bucket, with every account a candidate.
func (w *world) pick(session int) string {
	return w.e.Pick(w.req(Opus, session, "", W, G, X)).AuthID
}

func (w *world) reason(id string) string { return w.e.Status().Affinity.Sessions[id].Reason }

func (w *world) home(folder string) string {
	for _, h := range w.e.Status().Affinity.Homes {
		if h.Project == folder {
			return h.Account
		}
	}
	return ""
}

// twoAccounts: W has the most room, G resets first (so CPA's own order would choose G).
func (w *world) twoAccounts() {
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
}

func TestTwoSessionsOfOneFolderUseTheSameAccount(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.twoAccounts()
	first, second := w.pick(1), w.pick(2)
	if first != W || second != W {
		t.Fatalf("both sessions of the folder use its home, the account with the most room: %q %q", first, second)
	}
	if w.reason(sidN(1)) != ReasonProject || w.reason(sidN(2)) != ReasonProject {
		t.Fatalf("reasons: %q %q", w.reason(sidN(1)), w.reason(sidN(2)))
	}
	if got := w.e.Status().Affinity.Sessions[sidN(1)].Project; got != "-work-alpha" {
		t.Fatalf("the project is reported: %q", got)
	}
}

func TestAProjectStaysOnItsAccountWhileTheAccountHasRoom(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.twoAccounts()
	if w.pick(1) != W {
		t.Fatal("setup: the project starts on W")
	}
	// G now has far more room than W, but W is not full: the project never moves only to balance
	w.use(W, 80, 140, 0)
	w.use(G, 1, 90, 0)
	if got := w.pick(2); got != W {
		t.Fatalf("the project stays on W while W has room, got %q", got)
	}
	if w.home("-work-alpha") != W {
		t.Fatalf("home moved to %q", w.home("-work-alpha"))
	}
}

func TestAProjectMovesWhenItsAccountIsFullAndNotBefore(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	w.pick(1)
	w.use(W, 98.9, 140, 0) // just under W's cap of 99
	if got := w.pick(1); got != W {
		t.Fatalf("under the cap the project stays, got %q", got)
	}
	w.use(W, 99, 140, 0)
	if got := w.pick(1); got != G {
		t.Fatalf("at the weekly cap the project moves, got %q", got)
	}
	if w.home("-work-alpha") != G {
		t.Fatalf("the move is remembered: home is %q", w.home("-work-alpha"))
	}
	// the old account reopening does not pull the project back: it stays until G is full
	w.use(W, 5, 140, 0)
	if got := w.pick(1); got != G {
		t.Fatalf("the project stays on its new home, got %q", got)
	}
}

func TestAFiveHourCapIsADetourNotAMove(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	w.pick(1)
	w.use(W, 10, 140, 98) // W's 5-hour window is at its cap; its week is nearly empty
	if got := w.pick(1); got != G {
		t.Fatalf("the request goes to the other account, got %q", got)
	}
	if w.reason(sidN(1)) != ReasonFallback {
		t.Fatalf("reason %q", w.reason(sidN(1)))
	}
	if w.home("-work-alpha") != W {
		t.Fatalf("a detour keeps the home, got %q", w.home("-work-alpha"))
	}
	w.use(W, 10, 140, 5) // the window passed
	if got := w.pick(1); got != W || w.reason(sidN(1)) != ReasonProject {
		t.Fatalf("the project returns home, got %q (%s)", got, w.reason(sidN(1)))
	}
}

func TestAHomeThatCPAOffersNoCandidateForIsADetour(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	w.pick(1)
	got := w.e.Pick(w.req(Opus, 1, "", G)).AuthID
	if got != G || w.reason(sidN(1)) != ReasonFallback || w.home("-work-alpha") != W {
		t.Fatalf("got %q, reason %q, home %q", got, w.reason(sidN(1)), w.home("-work-alpha"))
	}
}

func TestAMovedProjectGoesToTheAccountWithTheMostHeadroom(t *testing.T) {
	w := newWorld(t, W, G, X)
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)  // room (85-52) = 33
	w.use(X, 40, 120, 0) // room (99-40) = 59
	if got := w.pick(1); got != W {
		t.Fatalf("a new project starts where the room is largest: %q", got)
	}
	w.use(W, 99, 140, 0)
	if got := w.pick(1); got != X {
		t.Fatalf("it moves to X (59) and not G (33), whose week resets first: %q", got)
	}
}

func TestHeadroomIsWeighedByPlan(t *testing.T) {
	w := newWorld(t, W, X)
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 50, 140, 0) // 49 room on a Pro
	w.use(X, 60, 140, 0) // 39 room on a Max 5x: 195
	w.e.mu.Lock()
	w.e.st.Account(W).Plan.Name, w.e.st.Account(X).Plan.Name = "pro", "max_5x"
	w.e.recompute(w.clock.t)
	w.e.mu.Unlock()
	if got := w.e.Pick(w.req(Opus, 1, "", W, X)).AuthID; got != X {
		t.Fatalf("a Max with less percent left has more room in Pro plans: %q", got)
	}
}

func TestEveryAccountFullKeepsTheHomeAndLetsTheExistingOrderDecide(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	w.pick(1)
	w.use(W, 99.5, 140, 0)
	w.use(G, 99.5, 90, 0)
	got := w.pick(1)
	if got != w.e.Pick(w.req(Opus, 0, "", W, G, X)).AuthID {
		t.Fatalf("with nothing usable the existing order answers: %q", got)
	}
	if w.home("-work-alpha") != W {
		t.Fatalf("the home does not move to an account that is also full: %q", w.home("-work-alpha"))
	}
}

func TestAFableWeekThatIsFullIsADetourWhileTheMainWeekHasRoom(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	fableProbe(w.e, W, 99.5, 140, w.clock.t)
	fableProbe(w.e, G, 0, 90, w.clock.t)
	if got := w.pick(1); got != W {
		t.Fatalf("main requests use the home: %q", got)
	}
	if got := w.e.Pick(w.req(Fable, 1, "", W, G)).AuthID; got != G {
		t.Fatalf("the Fable request detours to G: %q", got)
	}
	if w.home("-work-alpha") != W {
		t.Fatalf("a full Fable week does not move the project: %q", w.home("-work-alpha"))
	}
}

func TestFolderPin(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1, 2)
	w.folder("-work-beta", 3)
	w.start()
	w.twoAccounts()
	if err := w.e.SetPin(state.PinFolder, "-work-alpha", G); err != nil {
		t.Fatal(err)
	}
	if w.pick(1) != G || w.pick(2) != G || w.reason(sidN(1)) != ReasonFolderPin {
		t.Fatalf("the pin wins over the room: %q %q", w.pick(1), w.reason(sidN(1)))
	}
	if w.pick(3) != W {
		t.Fatal("another folder is not pinned")
	}
	if err := w.e.ClearPin(state.PinFolder, "-work-alpha"); err != nil {
		t.Fatal(err)
	}
	if w.pick(1) != W {
		t.Fatal("without the pin the project is placed by room")
	}
}

func TestSessionPin(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1, 2)
	w.start()
	w.twoAccounts()
	if err := w.e.SetPin(state.PinSession, sidN(1), G); err != nil {
		t.Fatal(err)
	}
	if w.pick(1) != G || w.reason(sidN(1)) != ReasonSessionPin {
		t.Fatalf("session 1 is pinned to G (%q)", w.reason(sidN(1)))
	}
	if w.pick(2) != W {
		t.Fatal("its sibling in the same folder keeps the project's home")
	}
	// a subagent of the pinned session follows the pin
	if got := w.e.Pick(w.req(Opus, 1, "ab12", W, G)).AuthID; got != G || w.reason(sidN(1)+"/ab12") != ReasonSessionPin {
		t.Fatalf("subagent %q (%s)", got, w.reason(sidN(1)+"/ab12"))
	}
	// a session pin outranks a folder pin
	if err := w.e.SetPin(state.PinFolder, "-work-alpha", W); err != nil {
		t.Fatal(err)
	}
	if w.pick(1) != G {
		t.Fatal("the session pin wins")
	}
}

func TestAPinHoldsUntilItsAccountIsFullThenFallsBackAndSaysSo(t *testing.T) {
	for _, kind := range []string{state.PinFolder, state.PinSession} {
		w := newWorld(t, W, G)
		w.folder("-work-alpha", 1)
		w.start()
		w.twoAccounts()
		key := map[string]string{state.PinFolder: "-work-alpha", state.PinSession: sidN(1)}[kind]
		if err := w.e.SetPin(kind, key, G); err != nil {
			t.Fatal(err)
		}
		w.use(G, 84.9, 90, 0)
		if w.pick(1) != G {
			t.Fatalf("%s: the pin holds below the cap", kind)
		}
		w.use(G, 85, 90, 0) // G's reserve cap
		if got := w.pick(1); got != W || w.reason(sidN(1)) != kind+"_pin_fallback" {
			t.Fatalf("%s: got %q (%s)", kind, got, w.reason(sidN(1)))
		}
		w.use(G, 10, 90, 0)
		if got := w.pick(1); got != G || w.reason(sidN(1)) != kind+"_pin" {
			t.Fatalf("%s: the pin returns once its account has room: %q (%s)", kind, got, w.reason(sidN(1)))
		}
	}
}

func TestAPinnedAccountThatIsOnlyHeldBackForAWhileIsAlsoAFallback(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	_ = w.e.SetPin(state.PinFolder, "-work-alpha", G)
	w.use(G, 52, 90, 99) // G's 5-hour window is at its cap
	if got := w.pick(1); got != W || w.reason(sidN(1)) != ReasonFolderPinFallback {
		t.Fatalf("got %q (%s)", got, w.reason(sidN(1)))
	}
}

func TestAnUnknownSessionIsLeftToTheExistingOrder(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	existing := w.e.Pick(w.req(Opus, 0, "", W, G)) // no session header at all
	if existing.AuthID != G {
		t.Fatalf("setup: CPA's order starts with G, the earliest reset: %+v", existing)
	}
	if got := w.pick(99); got != G { // a session no folder holds
		t.Fatalf("an unknown session is unchanged: %q", got)
	}
	if w.reason(sidN(99)) != ReasonNoProject {
		t.Fatalf("reason %q", w.reason(sidN(99)))
	}
	if len(w.e.Status().Affinity.Homes) != 0 {
		t.Fatal("an unknown session makes no home")
	}
	if w.e.Pick(w.req(Opus, 0, "", W, G)) != existing {
		t.Fatal("a request without a session is unchanged")
	}
}

func TestASubagentUsesItsParentsFolder(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1) // only the parent has a transcript file named after it
	w.start()
	w.twoAccounts()
	parent := w.pick(1)
	child := w.e.Pick(w.req(Opus, 1, "ab12", W, G)).AuthID
	grandchild := w.e.Pick(w.req(Opus, 1, "cd34", W, G)).AuthID
	if parent != W || child != W || grandchild != W {
		t.Fatalf("%q %q %q", parent, child, grandchild)
	}
	if v := w.e.Status().Affinity.Sessions[sidN(1)+"/ab12"]; v.Project != "-work-alpha" || v.Reason != ReasonProject {
		t.Fatalf("the subagent's own row: %+v", v)
	}
	// "main" is the main session, not a subagent
	w.e.Pick(w.req(Opus, 1, "main", W, G))
	if _, ok := w.e.Status().Affinity.Sessions[sidN(1)+"/main"]; ok {
		t.Fatal("agent id main is the main session")
	}
}

func TestAProjectsFirstRequestBeforeItsTranscriptExistsIsUnchangedAndTheNextOneIsPlaced(t *testing.T) {
	w := newWorld(t, W, G)
	w.start()
	w.twoAccounts()
	if w.pick(1) != G {
		t.Fatal("no transcript yet: the existing order answers")
	}
	w.folder("-work-alpha", 1)
	w.clock.t = w.clock.t.Add(time.Minute) // past the short memory of a miss
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	if w.pick(1) != W || w.reason(sidN(1)) != ReasonProject {
		t.Fatalf("placed once the file is there: %q", w.reason(sidN(1)))
	}
}

func TestShadowAndPausedDelegateWithoutPlacing(t *testing.T) {
	for name, mod := range map[string]func(*config.Config){
		"shadow": func(c *config.Config) { c.Mode = "shadow" },
		"paused": func(c *config.Config) { c.Paused = true },
	} {
		w := newWorld(t, W, G)
		mod(&w.cfg)
		w.folder("-work-alpha", 1)
		w.start()
		w.twoAccounts()
		if got := w.e.Pick(w.req(Opus, 1, "", W, G)); got != delegate {
			t.Fatalf("%s: %+v", name, got)
		}
		if len(w.e.Status().Affinity.Homes) != 0 {
			t.Fatalf("%s: nothing is placed", name)
		}
	}
}

func TestAPinnedAuthRequestIsStillNotRerouted(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	r := w.req(Opus, 1, "", W, G)
	r.Options.Metadata = map[string]any{"pinned_auth_id": G}
	if got := w.e.Pick(r).AuthID; got != G {
		t.Fatalf("CPA's own pin comes first: %q", got)
	}
}

func TestHomesAndPinsSurviveANewEngine(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.folder("-work-beta", 2)
	e := w.start()
	w.twoAccounts()
	w.pick(1)
	if err := e.SetPin(state.PinFolder, "-work-beta", G); err != nil { // saved at once
		t.Fatal(err)
	}
	e.Shutdown()
	again := New(w.cfg, newHost(), w.clock.now, nil)
	v := again.Status().Affinity
	if len(v.Homes) != 1 || v.Homes[0].Account != W || len(v.FolderPins) != 1 || v.FolderPins[0].Account != G || v.FolderPins[0].Source != SourceUI {
		t.Fatalf("%+v", v)
	}
}

func TestAPinChangeIsSavedAtOnce(t *testing.T) {
	w := newWorld(t, W, G)
	e := w.start()
	if err := e.SetPin(state.PinSession, sidN(1), G); err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(w.cfg.StatePath)
	if err != nil || s.Affinity.SessionPins[sidN(1)] != G {
		t.Fatalf("the file does not hold the pin: %+v %v", s, err)
	}
	if err := e.ClearPin(state.PinSession, sidN(1)); err != nil {
		t.Fatal(err)
	}
	if s, _ := state.Load(w.cfg.StatePath); len(s.Affinity.SessionPins) != 0 {
		t.Fatal("the file still holds the pin")
	}
}

func TestPinsFromTheConfigAreReadOnlyOnThePage(t *testing.T) {
	w := newWorld(t, W, G)
	w.cfg.Affinity.FolderPins = map[string]string{"-work-alpha": G}
	w.cfg.Affinity.SessionPins = map[string]string{sidN(1): G}
	w.folder("-work-alpha", 2)
	w.start()
	w.twoAccounts()
	if w.pick(2) != G || w.pick(1) != G {
		t.Fatal("config pins are in force")
	}
	if w.e.SetPin(state.PinFolder, "-work-alpha", W) == nil || w.e.ClearPin(state.PinFolder, "-work-alpha") == nil ||
		w.e.SetPin(state.PinSession, sidN(1), W) == nil || w.e.ClearPin(state.PinSession, sidN(1)) == nil {
		t.Fatal("a pin set in the config is changed in the config, not on the page")
	}
	v := w.e.Status().Affinity
	if len(v.FolderPins) != 1 || v.FolderPins[0].Source != SourceConfig || v.SessionPins[0].Source != SourceConfig {
		t.Fatalf("%+v", v)
	}
}

func TestPinsAreCheckedAndBounded(t *testing.T) {
	w := newWorld(t, W, G)
	e := w.start()
	for name, err := range map[string]error{
		"an account that is not configured": e.SetPin(state.PinFolder, "-work-alpha", "claude-other.json"),
		"a folder that is a path":           e.SetPin(state.PinFolder, "a/b", G),
		"a subagent id":                     e.SetPin(state.PinSession, sidN(1)+"/ab12", G),
		"an unknown kind":                   e.SetPin("project", "x", G),
	} {
		if err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	for i := 0; i < config.MaxSessionPins; i++ {
		if err := e.SetPin(state.PinSession, sidN(i+1), G); err != nil {
			t.Fatal(err)
		}
	}
	if e.SetPin(state.PinSession, sidN(config.MaxSessionPins+1), G) == nil {
		t.Fatal("a pin past the limit is refused")
	}
}

func TestHomesAndRememberedSessionsAreBounded(t *testing.T) {
	w := newWorld(t, W, G)
	n := MaxRememberedSessions + 50
	for i := 1; i <= n; i++ {
		w.folder(fmt.Sprintf("-work-%d", i), i)
	}
	w.start()
	w.twoAccounts()
	for i := 1; i <= n; i++ {
		w.pick(i)
	}
	v := w.e.Status().Affinity
	if len(v.Homes) != state.MaxHomes {
		t.Fatalf("homes: %d", len(v.Homes))
	}
	if len(v.Sessions) > MaxRememberedSessions {
		t.Fatalf("remembered sessions: %d", len(v.Sessions))
	}
	if _, ok := v.Sessions[sidN(n)]; !ok {
		t.Fatal("the newest session is remembered")
	}
}

func TestStatusReportsWhetherAPinnedOrHomeAccountIsFull(t *testing.T) {
	w := newWorld(t, W, G)
	w.folder("-work-alpha", 1)
	w.start()
	w.twoAccounts()
	w.pick(1)
	_ = w.e.SetPin(state.PinFolder, "-work-beta", W)
	w.use(W, 99, 140, 0)
	v := w.e.Status().Affinity
	if len(v.FolderPins) != 1 || !v.FolderPins[0].Full {
		t.Fatalf("pin: %+v", v.FolderPins)
	}
}

func TestAHomeOnAnAccountThatLeftTheConfigIsPlacedAgain(t *testing.T) {
	w := newWorld(t, W, G, X)
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 10, 140, 0)
	w.use(G, 52, 90, 0)
	w.use(X, 40, 120, 0)
	w.e.mu.Lock()
	w.e.st.SetHome("-work-alpha", "claude-gone.json", w.clock.t)
	w.e.mu.Unlock()
	if got := w.pick(1); got != W {
		t.Fatalf("a home that is not a configured account is full for good: %q", got)
	}
}
