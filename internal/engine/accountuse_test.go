package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/probe"
)

// bothMetered is metered's world with a meter on the Pro account too: W (Ultra) climbs to 20 points
// over 5M units (4 per million) and X (Pro) to 30 points over 5M (6 per million), in windows that
// opened inside the 7 days.
func bothMetered(t *testing.T) (*world, history.Sessions, View) {
	t.Helper()
	w, rec := balanced(t)
	w.e.st.Account(W).Plan = probe.Plan{Name: "max_20x", Weight: 10}
	w.e.st.Account(X).Plan = probe.Plan{Name: "pro", Weight: 1}
	w.folder("-work-a", 1, 2)
	w.folder("-work-b", 3)
	at := func(n int) time.Time { return w.clock.t.Add(time.Duration(n)*history.SlotLength + time.Minute) }
	levels := func(n int, wMain, xMain float64) {
		rec.Snapshot(at(n), nil, map[string]history.Levels{W: {Main: &wMain}, X: {Main: &xMain}})
	}
	use := func(n int, account string, session int, in int64) {
		src := history.Source{Account: account, Model: Opus}
		if session > 0 {
			src.Session = sidN(session)
		}
		rec.AddTraffic(at(n), src, history.Traffic{Requests: 1, InputTokens: in})
	}
	levels(1, 10, 40)
	levels(2, 99, 90)
	use(3, W, 1, 2_000_000)
	use(3, W, 3, 2_000_000)
	use(3, W, 0, 1_000_000) // a request without a session id
	use(3, X, 1, 4_000_000)
	use(3, X, 3, 1_000_000)
	levels(3, 4, 6)
	levels(4, 20, 30)
	s := w.e.ReadSessions(at(4))
	v := w.e.Status()
	w.e.AddSessions(&v, s)
	return w, s, v
}

func projectsOf(v View) map[string]ProjectUsage {
	out := map[string]ProjectUsage{}
	for _, p := range v.Affinity.Projects {
		out[p.Project] = p
	}
	return out
}

func TestAProjectIsSplitByAccountWithTokensAndThePercentOfEachAccountsOwnWindow(t *testing.T) {
	_, _, v := bothMetered(t)
	a := projectsOf(v)["-work-a"].Accounts
	if got := a[W]; got.Tokens7d != 2_000_000 || got.Tokens5h != 2_000_000 || !near(pointer(got.Share7d), 8) || !near(pointer(got.Share5h), 8) {
		t.Fatalf("W (Ultra): 2M at 4 per million: %+v", got)
	}
	if got := a[X]; got.Tokens7d != 4_000_000 || !near(pointer(got.Share7d), 24) {
		t.Fatalf("X (Pro): 4M at 6 per million, its own factor: %+v", got)
	}
	// the Ultra figures the page already had do not change
	if p := projectsOf(v)["-work-a"]; !near(pointer(p.PlanShare7d), 8) || p.Elsewhere7d != 4_000_000 {
		t.Fatalf("%+v", p)
	}
}

func TestTheProjectsOfAnAccountAddUpToItsMeterLessWhatCameFromOutside(t *testing.T) {
	_, _, v := bothMetered(t)
	sum := map[string]float64{}
	for _, p := range v.Affinity.Projects { // every project, and the group of no project
		for account, use := range p.Accounts {
			sum[account] += pointer(use.Share7d)
		}
	}
	if !near(sum[W], 20) || !near(sum[X], 30) {
		t.Fatalf("each account's rows add up to its meter: %v", sum)
	}
}

func TestASessionRowCarriesItsUseOfEachAccount(t *testing.T) {
	_, s, _ := bothMetered(t)
	for _, row := range s.Week.Sessions {
		if row.ID != sidN(1) {
			continue
		}
		if got := row.ByAccount[X]; got.Tokens7d != 4_000_000 || !near(pointer(got.Share7d), 24) {
			t.Fatalf("%+v", row.ByAccount)
		}
		if got := row.ByAccount[W]; got.Tokens7d != 2_000_000 || !near(pointer(got.Share7d), 8) {
			t.Fatalf("%+v", row.ByAccount)
		}
		return
	}
	t.Fatal("no row")
}

func TestAProAccountWithNoFactorShowsItsTokensAndNoPercent(t *testing.T) {
	_, _, v := metered(t) // X has no meter readings in this world
	got := projectsOf(v)["-work-a"].Accounts[X]
	if got.Tokens7d != 5_000_000 || got.Share7d != nil || got.Share5h != nil {
		t.Fatalf("%+v", got)
	}
}

func TestTheAccountCardListsItsTopProjectsOfTheWeekWithTheirPercent(t *testing.T) {
	_, _, v := bothMetered(t)
	for _, a := range v.Accounts {
		if a.AuthFile != W {
			continue
		}
		if len(a.ByProject) != 3 || a.ByProject[0].Project != "-work-a" {
			t.Fatalf("%+v", a.ByProject)
		}
		first, last := a.ByProject[0], a.ByProject[2]
		if first.Tokens7d != 2_000_000 || !near(pointer(first.Share7d), 8) || last.Project != "" || last.Tokens7d != 1_000_000 || !near(pointer(last.Share7d), 4) {
			t.Fatalf("largest first, the group of no project among them: %+v", a.ByProject)
		}
		return
	}
	t.Fatal("no account")
}

func TestTheAccountCardListIsCutAtTheTopFiveAndSaysHowManyMore(t *testing.T) {
	w, _ := balanced(t)
	var roots []history.RootUse
	for i := 1; i <= 7; i++ {
		w.folder(fmt.Sprintf("-work-p%d", i), i)
		roots = append(roots, history.RootUse{ID: sidN(i), Total: history.Traffic{Requests: 1, InputTokens: int64(i) * 1000},
			Share: history.Share{ByAccount: map[string]history.AccountPart{W: {Units: float64(i) * 1000, Points: float64(i), Priced: true}}}})
	}
	v := w.e.Status()
	w.e.AddSessions(&v, history.Sessions{Week: history.SessionWindow{AllRoots: roots}, Factors: map[string]history.Calibration{W: {Known: true}}})
	for _, a := range v.Accounts {
		if a.AuthFile == W {
			if len(a.ByProject) != 5 || a.MoreProjects != 2 || a.ByProject[0].Project != "-work-p7" || a.ByProject[4].Project != "-work-p3" {
				t.Fatalf("%+v more %d", a.ByProject, a.MoreProjects)
			}
			return
		}
	}
	t.Fatal("no account")
}
