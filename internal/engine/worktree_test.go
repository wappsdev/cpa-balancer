package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/affinity"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/probe"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// gitTree is a made-up repository "shop" with two worktrees, one in a sibling "shop-work" directory
// and one in "shop/.claude/worktrees", all under one temp directory, and the folder key of each.
type gitTree struct {
	repo, siblings, inside string // directories
	main, a, b             string // project folder keys
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// linked makes dir a git worktree of repo, the way git does.
func linked(t *testing.T, repo, dir string) {
	t.Helper()
	gitdir := filepath.Join(repo, ".git", "worktrees", filepath.Base(dir))
	write(t, filepath.Join(gitdir, "commondir"), "../..\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+gitdir+"\n")
}

func newGitTree(t *testing.T) gitTree {
	t.Helper()
	base := t.TempDir()
	g := gitTree{repo: filepath.Join(base, "shop"), siblings: filepath.Join(base, "shop-work", "feature-a"), inside: filepath.Join(base, "shop", ".claude", "worktrees", "feature-b")}
	mkdir(t, filepath.Join(g.repo, ".git"))
	linked(t, g.repo, g.siblings)
	linked(t, g.repo, g.inside)
	g.main, g.a, g.b = affinity.EncodePath(g.repo), affinity.EncodePath(g.siblings), affinity.EncodePath(g.inside)
	return g
}

func (g gitTree) folders(w *world) {
	w.folder(g.main, 1)
	w.folder(g.a, 2, 4)
	w.folder(g.b, 3)
}

func TestTheSessionsOfWorktreesShareTheHomeOfTheirMainRepository(t *testing.T) {
	w := newWorld(t, W, G)
	g := newGitTree(t)
	g.folders(w)
	w.start()
	w.twoAccounts()
	if w.pick(1) != W {
		t.Fatal("setup: the project starts on W, the account with the most room")
	}
	// G now has far more room: a separate project would be placed there, a worktree stays with the repository
	w.use(W, 80, 140, 0)
	w.use(G, 1, 90, 0)
	for n := 2; n <= 3; n++ {
		if got := w.pick(n); got != W {
			t.Fatalf("session %d of a worktree goes to the project's home, got %q", n, got)
		}
		if w.reason(sidN(n)) != ReasonProject {
			t.Fatalf("session %d: %q", n, w.reason(sidN(n)))
		}
	}
	homes := w.e.Status().Affinity.Homes
	if len(homes) != 1 || homes[0].Project != g.main {
		t.Fatalf("one home, the main repository's: %+v", homes)
	}
	if got := w.e.Status().Affinity.Sessions[sidN(2)].Project; got != g.a {
		t.Fatalf("a session still reports its own folder: %q", got)
	}
}

func TestAFolderPinOnTheMainRepositoryHoldsForItsWorktrees(t *testing.T) {
	w := newWorld(t, W, G)
	g := newGitTree(t)
	g.folders(w)
	w.start()
	w.twoAccounts()
	if err := w.e.SetPin(state.PinFolder, g.main, G); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		if got := w.pick(n); got != G || w.reason(sidN(n)) != ReasonFolderPin {
			t.Fatalf("session %d: %q %q", n, got, w.reason(sidN(n)))
		}
	}
}

func TestAFolderPinOnAWorktreeStillWinsForThatWorktree(t *testing.T) {
	w := newWorld(t, W, G)
	g := newGitTree(t)
	g.folders(w)
	w.start()
	w.twoAccounts()
	if err := w.e.SetPin(state.PinFolder, g.main, G); err != nil {
		t.Fatal(err)
	}
	if err := w.e.SetPin(state.PinFolder, g.a, W); err != nil {
		t.Fatal(err)
	}
	if got := w.pick(2); got != W {
		t.Fatalf("the worktree's own pin wins, got %q", got)
	}
	if got := w.pick(3); got != G {
		t.Fatalf("a worktree without a pin follows the repository's, got %q", got)
	}
	if got := w.pick(1); got != G {
		t.Fatalf("the repository's own sessions too, got %q", got)
	}
}

func TestAFolderThatIsGoneIsItsOwnProject(t *testing.T) {
	w := newWorld(t, W, G)
	key := affinity.EncodePath(filepath.Join(t.TempDir(), "gone", "feature"))
	w.folder(key, 1)
	w.start()
	w.twoAccounts()
	if w.pick(1) == "" || w.home(key) == "" {
		t.Fatal("a folder that cannot be found is placed as a project of its own")
	}
}

func TestAHomeStoredUnderAWorktreeFolderIsNoLongerAProject(t *testing.T) {
	w, rec := balanced(t)
	g := newGitTree(t)
	g.folders(w)
	w.homed(W, g.main, g.a, g.b)
	// the engine has seen the folders (as the first request of each would have)
	for n := 1; n <= 3; n++ {
		w.pickAmong(n)
	}
	w.spent(rec, 1, 10)
	plan := w.e.Rebalance(false)
	if !planned(plan, g.main) || planned(plan, g.a) || planned(plan, g.b) {
		t.Fatalf("only the main repository is a project to place: %+v", plan.Plan)
	}
}

func TestTheLoadOfWorktreesCountsForTheirMainRepository(t *testing.T) {
	w, rec := balanced(t)
	g := newGitTree(t)
	g.folders(w)
	w.spent(rec, 1, 100)
	w.spent(rec, 2, 20)
	w.spent(rec, 3, 7)
	w.e.mu.RLock()
	defer w.e.mu.RUnlock()
	if len(w.e.loads) != 1 || w.e.loads[g.main] != 127 {
		t.Fatalf("one project with its own and its worktrees' tokens: %v", w.e.loads)
	}
}

func TestTheStatusAddsUpAProjectFromItsOwnSessionsAndItsWorktrees(t *testing.T) {
	w, rec := balanced(t)
	g := newGitTree(t)
	g.folders(w)
	gone := affinity.EncodePath(filepath.Join(t.TempDir(), "gone"))
	w.folder(gone, 5)
	use := func(session int, tokens int64) {
		rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(session)}, history.Traffic{Requests: 1, InputTokens: tokens})
	}
	use(1, 100)
	use(2, 20)
	use(4, 3) // a second session of the same worktree
	use(3, 7)
	use(5, 9)
	v := w.e.Status()
	w.e.AddSessions(&v, rec.Sessions(w.clock.t))
	aff := v.Affinity

	if aff.ProjectOf[g.a] != g.main || aff.ProjectOf[g.b] != g.main || aff.ProjectOf[g.main] != g.main {
		t.Fatalf("every folder maps to its project: %v", aff.ProjectOf)
	}
	got := map[string]ProjectUsage{}
	for _, p := range aff.Projects {
		got[p.Project] = p
	}
	if _, ok := got[""]; !ok || len(got) != 3 { // the world's set-up requests came without a session id
		t.Fatalf("the repository (with its worktrees), the folder that is gone, and no project: %+v", aff.Projects)
	}
	p := got[g.main]
	if p.Week.Tokens != 130 || p.Week.Requests != 4 || p.FiveHour.Tokens != 130 {
		t.Fatalf("the project is its own use plus its worktrees': %+v", p)
	}
	if len(p.Worktrees) != 2 || p.Worktrees[0].Project != g.a || p.Worktrees[1].Project != g.b {
		t.Fatalf("one entry per worktree, by name: %+v", p.Worktrees)
	}
	a, b := p.Worktrees[0], p.Worktrees[1]
	if a.Week.Tokens != 23 || a.Week.Requests != 2 || b.Week.Tokens != 7 || a.Name != "feature-a" || a.Path != g.siblings || b.Name != "feature-b" {
		t.Fatalf("a worktree shows its own use, its name and its directory: %+v %+v", a, b)
	}
	if own := p.Week.Tokens - a.Week.Tokens - b.Week.Tokens; own != 100 {
		t.Fatalf("the project's own sessions are what is left: %d", own)
	}
	if q := got[gone]; q.Week.Tokens != 9 || len(q.Worktrees) != 0 || aff.ProjectOf[gone] != gone {
		t.Fatalf("a folder that is gone is a project of its own: %+v", q)
	}
	if aff.SessionProjects[sidN(2)] != g.a || aff.SessionProjects[sidN(1)] != g.main {
		t.Fatalf("a session is still listed with its own folder: %v", aff.SessionProjects)
	}
	if aff.Names[g.main] == "" {
		t.Fatalf("the project has a display name: %v", aff.Names)
	}
}

func TestARemovedWorktreeStillJoinsItsRepositoryAfterARestart(t *testing.T) {
	w := newWorld(t, W, G)
	g := newGitTree(t)
	g.folders(w)
	w.start()
	for _, f := range []string{g.a, g.b} {
		w.e.repos.Of(f)
	}
	w.e.Tick() // saves the state
	if err := os.RemoveAll(g.siblings); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(g.inside); err != nil {
		t.Fatal(err)
	}
	w.start() // a restart: the engine knows the folders only from the state file
	for _, f := range []string{g.a, g.b} {
		if got := w.e.repos.Of(f); got.Project != g.main || !got.Worktree {
			t.Fatalf("%s: %+v", f, got)
		}
	}
}

func TestTheStatusMarksAGoneFolderPutWithARepositoryByItsContainer(t *testing.T) {
	w, rec := balanced(t)
	g := newGitTree(t)
	g.folders(w)
	gone := affinity.EncodePath(filepath.Join(filepath.Dir(g.siblings), "feature-z")) // beside feature-a; not on the disk
	removed := affinity.EncodePath(filepath.Join(t.TempDir(), "elsewhere"))
	w.folder(gone, 5)
	w.folder(removed, 6)
	for _, n := range []int{1, 2, 3, 5, 6} {
		rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(n)}, history.Traffic{Requests: 1, InputTokens: 10})
	}
	v := w.e.Status()
	w.e.AddSessions(&v, rec.Sessions(w.clock.t))
	aff := v.Affinity
	if aff.ProjectOf[gone] != g.main || !aff.ProjectInferred[gone] {
		t.Fatalf("the gone folder joins the repository and is marked: %v %v", aff.ProjectOf, aff.ProjectInferred)
	}
	if aff.ProjectInferred[g.a] || aff.ProjectInferred[g.main] || aff.ProjectInferred[removed] || aff.ProjectOf[removed] != removed {
		t.Fatalf("a folder that was found, or has no container, is not inferred: %v", aff.ProjectInferred)
	}
	var names []string
	for _, p := range aff.Projects {
		if p.Project == g.main {
			for _, wt := range p.Worktrees {
				names = append(names, wt.Name)
			}
		}
	}
	if len(names) != 3 || names[0] != "feature-a" || names[1] != "feature-b" || names[2] != "feature-z" {
		t.Fatalf("an inferred worktree has a name from its folder: %v", names)
	}
}

func TestTheUltraPercentOfAProjectIncludesItsWorktrees(t *testing.T) {
	w, rec := balanced(t)
	g := newGitTree(t)
	g.folders(w)
	w.e.st.Account(W).Plan = probe.Plan{Name: "max_20x", Weight: 10}
	at := func(n int) time.Time { return w.clock.t.Add(time.Duration(n)*history.SlotLength + time.Minute) }
	levels := func(n int, main float64) {
		rec.Snapshot(at(n), nil, map[string]history.Levels{W: {Main: &main}})
	}
	use := func(session int, in int64) {
		rec.AddTraffic(at(3), history.Source{Account: W, Model: Opus, Session: sidN(session)}, history.Traffic{Requests: 1, InputTokens: in})
	}
	levels(1, 10)
	levels(2, 99)
	use(1, 2_000_000)
	use(2, 1_000_000)
	use(3, 1_000_000)
	levels(3, 4)
	levels(4, 20) // 20 points over 4M units: 5 per million
	v := w.e.Status()
	w.e.AddSessions(&v, w.e.ReadSessions(at(4)))
	var p ProjectUsage
	for _, q := range v.Affinity.Projects {
		if q.Project == g.main {
			p = q
		}
	}
	if len(p.Worktrees) != 2 {
		t.Fatalf("%+v", v.Affinity.Projects)
	}
	if !near(pointer(p.PlanShare7d), 20) || !near(pointer(p.Worktrees[0].PlanShare7d), 5) || !near(pointer(p.Worktrees[1].PlanShare7d), 5) {
		t.Fatalf("the project is 10 for itself and 5 for each worktree: %v, %+v", pointer(p.PlanShare7d), p.Worktrees)
	}
}
