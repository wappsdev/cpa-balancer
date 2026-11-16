package engine

import (
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

func TestUsageReachesTheSessionsOfTheStatus(t *testing.T) {
	c := &clock{t0}
	rec := history.NewRecorder(t.TempDir())
	e := New(testConfig(t), newHost(), c.now, rec)
	main := usage(G, Opus, headers(52, 90, 30, 3, t0))
	main.SessionID = "11111111-aaaa-bbbb-cccc-000000000001"
	main.Detail.InputTokens, main.Detail.OutputTokens = 100, 10
	sub := usage(W, Opus, headers(10, 140, 5, 4, t0))
	sub.SessionID, sub.ParentSessionID = "11111111-aaaa-bbbb-cccc-000000000002", main.SessionID
	sub.Detail.InputTokens = 7
	e.OnUsage(main)
	e.OnUsage(sub)
	e.OnUsage(usage(G, Opus, headers(52, 90, 30, 3, t0))) // no session id

	s := rec.Sessions(c.t)
	if len(s.FiveHour.Sessions) != 2 || s.FiveHour.Total.Requests != 3 {
		t.Fatalf("%+v", s.FiveHour)
	}
	var parent history.SessionUsage
	for _, row := range s.FiveHour.Sessions {
		if row.ID == main.SessionID {
			parent = row
		}
	}
	if parent.Own.InputTokens != 100 || parent.Total.InputTokens != 107 || len(parent.Subagents) != 1 || parent.Subagents[0].Accounts[W].Requests != 1 {
		t.Fatalf("parent %+v", parent)
	}
}

func TestTheStatusSumsUsePerProjectFromEveryRootAndNamesTheProjectOfEachListedSession(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-a", 1, 2)
	w.folder("-work-b", 3)
	use := func(session string, parent string, tokens int64) {
		rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: session, Parent: parent}, history.Traffic{Requests: 1, InputTokens: tokens})
	}
	use(sidN(1), "", 100)
	use(sidN(1)+"/ab", sidN(1), 20) // a subagent counts with its project
	use(sidN(2), "", 50)
	use(sidN(3), "", 7)
	use("99999999-2222-3333-4444-555555555555", "", 3) // a session no folder holds
	use("", "", 1)                                     // a request without a session

	v := w.e.Status()
	w.e.AddSessions(&v, rec.Sessions(w.clock.t))
	got := map[string]ProjectUsage{}
	for _, p := range v.Affinity.Projects {
		got[p.Project] = p
	}
	if a := got["-work-a"]; a.Week.Tokens != 170 || a.Week.Requests != 3 || a.FiveHour.Tokens != 170 || a.Week.Last.IsZero() {
		t.Fatalf("a project sums its sessions and their subagents: %+v", a)
	}
	// the world's two set-up requests came without a session id too
	if got["-work-b"].Week.Tokens != 7 || got[""].Week.Tokens != 4 || got[""].Week.Requests != 4 || len(got) != 3 {
		t.Fatalf("a session without a folder, and one without an id, are the group of no project: %+v", got)
	}
	if v.Sessions == nil || v.Affinity.SessionProjects[sidN(1)] != "-work-a" || v.Affinity.SessionProjects[sidN(3)] != "-work-b" {
		t.Fatalf("each listed root session names its folder: %+v", v.Affinity.SessionProjects)
	}
	if _, ok := v.Affinity.SessionProjects["99999999-2222-3333-4444-555555555555"]; ok {
		t.Fatal("a session with no folder is not named")
	}
}

func TestTheProjectSumsAreNotCutWithTheWeeksListOf30(t *testing.T) {
	w, rec := balanced(t)
	for i := 1; i <= 35; i++ {
		w.folder("-work-many", i)
		rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(i)}, history.Traffic{Requests: 1, InputTokens: 10})
	}
	v := w.e.Status()
	s := rec.Sessions(w.clock.t)
	w.e.AddSessions(&v, s)
	got := map[string]Use{}
	for _, p := range v.Affinity.Projects {
		got[p.Project] = p.Week
	}
	if s.Week.Other.Sessions != 5 || got["-work-many"].Tokens != 350 || got["-work-many"].Requests != 35 {
		t.Fatalf("the project counts all 35 sessions: %+v", v.Affinity.Projects)
	}
}
