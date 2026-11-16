package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/probe"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// balanced is a world of two accounts with the same plan and the same room (W and X, neither with a
// reserve), and a recorder, so the engine can read what each project used.
func balanced(t *testing.T) (*world, *history.Recorder) {
	t.Helper()
	w := newWorld(t, W, X)
	rec := history.NewRecorder(t.TempDir())
	w.e = New(w.cfg, newHost(), w.clock.now, rec)
	w.use(W, 10, 140, 0)
	w.use(X, 10, 140, 0)
	return w, rec
}

// spent records tokens a project's session used, then lets the engine read them.
func (w *world) spent(rec *history.Recorder, session int, tokens int64) {
	rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(session)}, history.Traffic{Requests: 1, InputTokens: tokens})
	w.e.refreshLoads(w.clock.t)
}

// homesOn counts the project homes per account.
func (w *world) homesOn() map[string]int {
	out := map[string]int{}
	for _, h := range w.e.Status().Affinity.Homes {
		out[h.Account]++
	}
	return out
}

func (w *world) pickAmong(session int) string {
	return w.e.Pick(w.req(Opus, session, "", W, X)).AuthID
}

func TestNewProjectsOfTwoEqualAccountsSplitTwoToOneNotThreeToNothing(t *testing.T) {
	w, _ := balanced(t)
	for i, name := range []string{"-work-a", "-work-b", "-work-c"} {
		w.folder(name, i+1)
	}
	for i := 1; i <= 3; i++ {
		w.pickAmong(i)
	}
	if got := w.homesOn(); got[W]+got[X] != 3 || got[W] < 1 || got[X] < 1 {
		t.Fatalf("three projects on two equal accounts: %v", got)
	}
}

func TestTheLoadOfTheProjectsAlreadyHomedThereIsCounted(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-big", 10)
	w.folder("-work-small", 11)
	w.folder("-work-new", 12)
	w.e.st.SetHome("-work-big", W, w.clock.t)
	w.e.st.SetHome("-work-small", X, w.clock.t)
	w.spent(rec, 10, 100000)
	w.spent(rec, 11, 100)
	// one home on each account, so a count alone would tie and CPA's order would put the project on W
	if got := w.pickAmong(12); got != X {
		t.Fatalf("the account with the lighter projects takes the new one, got %q", got)
	}
}

func TestAFolderPinnedProjectsLoadCountsOnItsPinnedAccount(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-pinned", 10)
	w.folder("-work-new", 12)
	if err := w.e.SetPin(state.PinFolder, "-work-pinned", W); err != nil {
		t.Fatal(err)
	}
	w.spent(rec, 10, 100000) // the pinned project never gets a home while its pin has room
	if got := w.pickAmong(12); got != X {
		t.Fatalf("W carries the pinned project, so the new one goes to X, got %q", got)
	}
}

func TestAProjectWithNoRecordedUseCountsAsAnAverageProject(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-used", 10)
	w.folder("-work-idle", 11)
	w.folder("-work-new", 12)
	w.e.st.SetHome("-work-used", W, w.clock.t)
	w.e.st.SetHome("-work-idle", X, w.clock.t)
	w.spent(rec, 10, 5000)
	// X holds a project that has used nothing yet, which counts as an average one, not as no load:
	// the loads are even, so the tie goes to CPA's order
	if got := w.pickAmong(12); got != W {
		t.Fatalf("even loads: CPA's order decides, got %q", got)
	}
}

func TestTickReadsTheProjectLoadsOnceInAWhile(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-big", 10)
	rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(10)}, history.Traffic{Requests: 1, InputTokens: 777})
	w.e.Tick()
	if w.e.loads["-work-big"] != 777 {
		t.Fatalf("loads after a tick: %v", w.e.loads)
	}
	rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(10)}, history.Traffic{Requests: 1, InputTokens: 1})
	w.e.Tick()
	if w.e.loads["-work-big"] != 777 {
		t.Fatalf("the history is not read again within %v: %v", loadsTTL, w.e.loads)
	}
	w.clock.t = w.clock.t.Add(loadsTTL)
	w.e.Tick()
	if w.e.loads["-work-big"] != 778 {
		t.Fatalf("read again after %v: %v", loadsTTL, w.e.loads)
	}
}

func TestAPlacementStillGoesToTheAccountWithTheMostHeadroomWhenNothingIsHomedYet(t *testing.T) {
	w, _ := balanced(t)
	w.use(W, 60, 140, 0)
	w.folder("-work-a", 1)
	if got := w.pickAmong(1); got != X {
		t.Fatalf("the first project takes the account with more room, got %q", got)
	}
}

// homed puts projects on an account the way earlier requests would have.
func (w *world) homed(account string, projects ...string) {
	for _, p := range projects {
		w.e.st.SetHome(p, account, w.clock.t)
	}
}

func planned(plan RebalanceResult, project string) bool {
	for _, m := range plan.Plan {
		if m.Project == project {
			return true
		}
	}
	return false
}

func moves(plan RebalanceResult) int {
	n := 0
	for _, m := range plan.Plan {
		if m.From != m.To {
			n++
		}
	}
	return n
}

func TestARebalancePlanLeavesAFolderPinnedProjectWhereItIs(t *testing.T) {
	w, _ := balanced(t)
	w.homed(W, "-work-a", "-work-b", "-work-c", "-work-d")
	if err := w.e.SetPin(state.PinFolder, "-work-b", W); err != nil {
		t.Fatal(err)
	}
	plan := w.e.Rebalance(false)
	if planned(plan, "-work-b") || plan.Pinned != 1 || len(plan.Plan) != 3 {
		t.Fatalf("the pinned project is not in the plan: %+v", plan)
	}
	if plan.Applied || w.homesOn()[X] != 0 {
		t.Fatal("a plan changes nothing")
	}
	w.e.Rebalance(true)
	// the pinned project counts as load on W, so W keeps one more and X takes two
	if got := w.homesOn(); got[W] != 2 || got[X] != 2 {
		t.Fatalf("pinned project counts as load on W: %v", got)
	}
	if w.home("-work-b") != W {
		t.Fatal("the pinned project is still on its account")
	}
}

func TestApplyingARebalanceTwiceChangesNothingTheSecondTime(t *testing.T) {
	w, rec := balanced(t)
	for i, p := range []string{"-work-a", "-work-b", "-work-c", "-work-d", "-work-e"} {
		w.folder(p, i+1)
		w.homed(W, p)
		w.spent(rec, i+1, int64(1000*(i+1)))
	}
	first := w.e.Rebalance(true)
	if !first.Applied || moves(first) == 0 {
		t.Fatalf("five projects on one of two equal accounts move: %+v", first)
	}
	homes := w.homesOn()
	if homes[W] == 5 || homes[X] == 0 {
		t.Fatalf("the load is split: %v", homes)
	}
	if again := w.e.Rebalance(false); moves(again) != 0 {
		t.Fatalf("a second plan has nothing to move: %+v", again)
	}
	w.e.Rebalance(true)
	if after := w.homesOn(); after[W] != homes[W] || after[X] != homes[X] {
		t.Fatalf("a second apply changed the homes: %v then %v", homes, after)
	}
}

func TestARebalanceWeighsProjectsByTheirUse(t *testing.T) {
	w, rec := balanced(t)
	for i, p := range []string{"-work-a", "-work-b", "-work-c", "-work-d"} {
		w.folder(p, i+1)
		w.homed(W, p)
	}
	w.spent(rec, 1, 90000) // one project far heavier than the other three together
	w.spent(rec, 2, 3000)
	w.spent(rec, 3, 3000)
	w.spent(rec, 4, 3000)
	w.e.Rebalance(true)
	heavy, others := w.home("-work-a"), 0
	for _, p := range []string{"-work-b", "-work-c", "-work-d"} {
		if w.home(p) != heavy {
			others++
		}
	}
	if others != 3 {
		t.Fatalf("the heavy project has an account to itself: %v", w.homesOn())
	}
}

func TestARebalanceThatFindsTheLayoutBalancedMovesNothing(t *testing.T) {
	w, _ := balanced(t)
	w.homed(W, "-work-a", "-work-c")
	w.homed(X, "-work-b", "-work-d")
	if plan := w.e.Rebalance(false); moves(plan) != 0 || len(plan.Plan) != 4 {
		t.Fatalf("no churn on a tie: %+v", plan)
	}
	w.homed(X, "-work-a", "-work-c") // the same layout with the accounts swapped
	w.homed(W, "-work-b", "-work-d")
	if plan := w.e.Rebalance(false); moves(plan) != 0 {
		t.Fatalf("no churn on a tie: %+v", plan)
	}
}

func TestARebalanceDoesNotEmptyAnAccountThatIsOnlyInADetour(t *testing.T) {
	w, _ := balanced(t)
	w.homed(W, "-work-a", "-work-c")
	w.homed(X, "-work-b", "-work-d")
	w.use(W, 10, 140, 99.5) // W's 5-hour window is over its cap: a detour, W is not full
	if plan := w.e.Rebalance(false); moves(plan) != 0 {
		t.Fatalf("W is held back for a while, its projects stay: %+v", plan)
	}
}

func TestARebalanceNeverMovesAProjectOntoAnAccountThatCannotTakeRequestsNow(t *testing.T) {
	w, _ := balanced(t)
	w.homed(X, "-work-a", "-work-b", "-work-c", "-work-d")
	w.use(W, 10, 140, 99.5) // W is in a 5-hour detour: not full, but every request to it would be a fallback
	plan := w.e.Rebalance(true)
	if got := w.homesOn(); got[X] != 4 || got[W] != 0 || moves(plan) != 0 {
		t.Fatalf("W has the room but cannot take a project now: %v %+v", got, plan)
	}
	w.use(W, 10, 140, 0) // the detour ends
	w.e.Rebalance(true)
	if got := w.homesOn(); got[W] != 2 || got[X] != 2 {
		t.Fatalf("W takes its share once it can: %v", got)
	}
}

// threeAccounts is the owner's live mix: a Max 20x account with most of its week left (W, 21% of
// cap 100), a Max 20x one nearly used (X, 73% of cap 90) and a Pro with little room (Y, 2% of cap 45).
func threeAccounts(t *testing.T) (*world, *history.Recorder) {
	t.Helper()
	const Y = "claude-y.json"
	w := newWorld(t, W, X, Y)
	w.cfg.Thresholds.FinalCap = 100
	for i := range w.cfg.Accounts {
		switch w.cfg.Accounts[i].AuthFile {
		case X:
			w.cfg.Accounts[i].Reserve, w.cfg.Accounts[i].ReserveSteps = config.ReserveCustom, []config.ReserveStep{{Cap: 90, Hours: 0}}
		case Y:
			w.cfg.Accounts[i].Reserve, w.cfg.Accounts[i].ReserveSteps = config.ReserveCustom, []config.ReserveStep{{Cap: 45, Hours: 0}}
		}
	}
	rec := history.NewRecorder(t.TempDir())
	w.e = New(w.cfg, newHost(), w.clock.now, rec)
	w.e.st.Account(W).Plan = probe.Plan{Name: "max_20x", Weight: 10}
	w.e.st.Account(X).Plan = probe.Plan{Name: "max_20x", Weight: 10}
	w.e.st.Account(Y).Plan = probe.Plan{Name: "pro", Weight: 1}
	w.use(W, 21, 100, 0)
	w.use(X, 73, 40, 0)
	w.use(Y, 2, 40, 0)
	return w, rec
}

func TestARebalanceSpreadsTwoHeavyAndSixteenLightProjectsByHeadroom(t *testing.T) {
	w, rec := threeAccounts(t)
	for i := 1; i <= 18; i++ {
		p := fmt.Sprintf("-work-%02d", i)
		w.folder(p, i)
		w.homed(W, p) // every project starts on the account with the most room, as the old rule put them
		tokens := int64(40000)
		if i <= 2 {
			tokens = 4000000
		}
		w.spent(rec, i, tokens)
	}
	plan := w.e.Rebalance(true)
	got := w.homesOn()
	for _, a := range plan.Accounts {
		t.Logf("plan: %s %s share %.2f projects %d", a.Account, a.State, a.Share, a.Projects)
	}
	if got[X] == 0 || got["claude-y.json"] == 0 || got[W] == 18 {
		t.Fatalf("the load is spread over the accounts by their room: %v", got)
	}
	if len(plan.Accounts) != 3 || plan.Accounts[0].Projects != got[W] || plan.Accounts[1].State != "usable" || plan.Accounts[0].Share < 0.78 || plan.Accounts[0].Share > 0.8 {
		t.Fatalf("the plan says how each account took part: %+v", plan.Accounts)
	}
	if got[W] < got[X] || got[X] < got["claude-y.json"] {
		t.Fatalf("the counts follow the room: %v", got)
	}
	if w.home("-work-01") != W || w.home("-work-02") != W {
		t.Fatalf("the heavy projects go where the room is: %v %v", w.home("-work-01"), w.home("-work-02"))
	}
}

func TestAPlanThatMovesNothingBecauseTheOtherAccountsAreInADetourSaysSo(t *testing.T) {
	w, _ := threeAccounts(t)
	for i := 1; i <= 6; i++ {
		w.homed(W, fmt.Sprintf("-work-%02d", i))
	}
	w.use(X, 73, 40, 99.5) // both other accounts are over their 5-hour cap
	w.use("claude-y.json", 2, 40, 99.5)
	plan := w.e.Rebalance(false)
	if moves(plan) != 0 || plan.Accounts[1].State != "detour" || plan.Accounts[2].State != "detour" || plan.Accounts[0].Projects != 6 {
		t.Fatalf("%+v", plan)
	}
}

func TestARebalanceMovesProjectsOffAnAccountThatIsFull(t *testing.T) {
	w, _ := balanced(t)
	w.homed(W, "-work-a", "-work-b")
	w.use(W, 99.5, 140, 0)
	plan := w.e.Rebalance(true)
	if got := w.homesOn(); got[X] != 2 || got[W] != 0 {
		t.Fatalf("a full account is no candidate: %v %+v", got, plan)
	}
}

func TestARebalanceWithNoCandidateKeepsEveryProject(t *testing.T) {
	w, _ := balanced(t)
	w.homed(W, "-work-a")
	w.use(W, 99.5, 140, 0)
	w.use(X, 99.5, 140, 0)
	if plan := w.e.Rebalance(true); moves(plan) != 0 || w.home("-work-a") != W {
		t.Fatalf("nowhere to go: %+v", plan)
	}
}

func TestARebalanceIsSavedAndKeepsFolderNamesOutOfTheLog(t *testing.T) {
	w, _ := balanced(t)
	w.homed(W, "-secret-folder-a", "-secret-folder-b")
	w.e.Rebalance(true)
	for _, line := range w.e.host.(*fakeHost).logs {
		if strings.Contains(line, "secret-folder") {
			t.Fatalf("a folder name reached the log: %q", line)
		}
	}
	reloaded := New(w.cfg, newHost(), w.clock.now, nil)
	homes := reloaded.st.Affinity.Homes
	if len(homes) != 2 || homes["-secret-folder-a"].Account == homes["-secret-folder-b"].Account {
		t.Fatalf("the moves were saved: %+v", homes)
	}
}
