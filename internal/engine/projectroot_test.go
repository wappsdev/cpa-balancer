package engine

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

const rootFolder = "-home-u-proj" // the folder name of /home/u/proj

// rootReq is session n's main request on the opus bucket with the project-root header set to root.
func (w *world) rootReq(session int, root string) pluginapi.SchedulerPickRequest {
	r := w.req(Opus, session, "", W, G, X)
	r.Options.Headers["X-Wapps-Project-Root"] = []string{root}
	return r
}

// served records the request a pick led to, as CPA's usage hook would.
func (w *world) served(account string, session int, tokens int64) {
	rec := usage(account, Opus, nil)
	rec.SessionID = sidN(session)
	rec.Detail.InputTokens = tokens
	w.e.OnUsage(rec)
}

func (w *world) groups(rec *history.Recorder) (map[string]string, map[string]ProjectUsage) {
	v := w.e.Status()
	w.e.AddSessions(&v, rec.Sessions(w.clock.t))
	groups := map[string]ProjectUsage{}
	for _, p := range v.Affinity.Projects {
		groups[p.Project] = p
	}
	return v.Affinity.SessionProjects, groups
}

func TestASessionWithNoTranscriptIsPlacedAndGroupedUnderTheProjectRootOfItsHeader(t *testing.T) {
	w, rec := balanced(t)
	if got := w.e.Pick(w.rootReq(1, "/home/u/proj")).AuthID; got == "" {
		t.Fatal("the session is placed")
	}
	if v := w.e.Status().Affinity.Sessions[sidN(1)]; v.Project != rootFolder || v.Reason != ReasonProject {
		t.Fatalf("placed by its project home: %+v", v)
	}
	if len(w.e.Status().Affinity.Homes) != 1 || w.e.Status().Affinity.Homes[0].Project != rootFolder {
		t.Fatalf("the header's folder is a project home: %+v", w.e.Status().Affinity.Homes)
	}
	w.served(W, 1, 100)
	w.served(W, 2, 5) // a session with no header
	sessions, groups := w.groups(rec)
	if sessions[sidN(1)] != rootFolder || groups[rootFolder].Week.Tokens != 100 {
		t.Fatalf("grouped under its project: %v %+v", sessions, groups)
	}
	if _, ok := sessions[sidN(2)]; ok || groups[""].Week.Tokens != 5 {
		t.Fatalf("an untagged session stays in no project: %v %+v", sessions, groups)
	}
}

func TestATranscriptWinsOverTheHeader(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-alpha", 1)
	w.e.Pick(w.rootReq(1, "/home/u/proj"))
	if v := w.e.Status().Affinity.Sessions[sidN(1)]; v.Project != "-work-alpha" {
		t.Fatalf("the transcript's folder is the project: %+v", v)
	}
	w.served(W, 1, 100)
	sessions, groups := w.groups(rec)
	if sessions[sidN(1)] != "-work-alpha" || groups["-work-alpha"].Week.Tokens != 100 {
		t.Fatalf("%v %+v", sessions, groups)
	}
	if _, ok := groups[rootFolder]; ok {
		t.Fatalf("the header's folder holds nothing: %+v", groups)
	}
}

func TestAHeaderThatIsNotAnAbsolutePathLeavesTheSessionInNoProject(t *testing.T) {
	w, rec := balanced(t)
	w.e.Pick(w.rootReq(1, "home/u/proj"))
	if v := w.e.Status().Affinity.Sessions[sidN(1)]; v.Reason != ReasonNoProject || v.Project != "" {
		t.Fatalf("%+v", v)
	}
	w.served(W, 1, 100)
	sessions, groups := w.groups(rec)
	if len(sessions) != 0 || groups[""].Week.Tokens != 100 {
		t.Fatalf("%v %+v", sessions, groups)
	}
}

func TestAHeaderGroupsASessionEvenWhenNothingIsPlaced(t *testing.T) {
	for name, mod := range map[string]func(*config.Config){
		"shadow": func(c *config.Config) { c.Mode = "shadow" },
		"paused": func(c *config.Config) { c.Paused = true },
	} {
		w := newWorld(t, W, X)
		mod(&w.cfg)
		rec := history.NewRecorder(t.TempDir())
		w.e = New(w.cfg, newHost(), w.clock.now, rec)
		if got := w.e.Pick(w.rootReq(1, "/home/u/proj")); got != delegate {
			t.Fatalf("%s: %+v", name, got)
		}
		if len(w.e.Status().Affinity.Homes) != 0 {
			t.Fatalf("%s: nothing is placed", name)
		}
		w.served(W, 1, 100)
		if sessions, _ := w.groups(rec); sessions[sidN(1)] != rootFolder {
			t.Fatalf("%s: %v", name, sessions)
		}
	}
}

func TestAHeaderFolderFollowsItsProjectsPin(t *testing.T) {
	w, _ := balanced(t)
	if err := w.e.SetPin(state.PinFolder, rootFolder, X); err != nil {
		t.Fatal(err)
	}
	if got := w.e.Pick(w.rootReq(1, "/home/u/proj")).AuthID; got != X {
		t.Fatalf("pinned: %q", got)
	}
	if got := w.e.Status().Affinity.Sessions[sidN(1)].Reason; got != ReasonFolderPin {
		t.Fatalf("reason %q", got)
	}
}

func TestAHeaderFolderSurvivesARestartThroughTheHistory(t *testing.T) {
	dir := t.TempDir()
	w := newWorld(t, W, X)
	rec := history.NewRecorder(dir)
	w.e = New(w.cfg, newHost(), w.clock.now, rec)
	w.use(W, 10, 140, 0)
	w.use(X, 10, 140, 0)
	w.e.Pick(w.rootReq(1, "/home/u/proj"))
	w.served(W, 1, 100)
	if err := rec.Flush(w.clock.t.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}

	// a new process: no memory, the same history directory, still no transcript
	rec = history.NewRecorder(dir)
	w.e = New(w.cfg, newHost(), w.clock.now, rec)
	sessions, groups := w.groups(rec)
	if sessions[sidN(1)] != rootFolder || groups[rootFolder].Week.Tokens != 100 {
		t.Fatalf("grouped after a restart: %v %+v", sessions, groups)
	}
}
