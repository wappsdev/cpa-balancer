package history

import (
	"testing"
	"time"
)

func tagged(account, model, session, parent, project string) Source {
	return Source{Account: account, Model: model, Session: session, Parent: parent, Project: project}
}

func TestARootSessionKeepsTheProjectItsRequestsWereTaggedWith(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, tagged("a", "m", "s1", "", "-home-u-proj"), tok(1, 1, 1))
	r.AddTraffic(t0.Add(time.Minute), src("a", "m", "s1", ""), tok(1, 1, 1)) // an untagged request keeps the tag
	r.AddTraffic(t0, src("a", "m", "s2", ""), tok(1, 1, 1))
	w := window(r, 0)
	if s1 := find(w, "s1"); s1 == nil || s1.Project != "-home-u-proj" {
		t.Fatalf("%+v", s1)
	}
	if s2 := find(w, "s2"); s2 == nil || s2.Project != "" {
		t.Fatalf("an untagged session has no project: %+v", s2)
	}
	roots := map[string]string{}
	for _, root := range w.AllRoots {
		roots[root.ID] = root.Project
	}
	if roots["s1"] != "-home-u-proj" || roots["s2"] != "" {
		t.Fatalf("every root carries its project: %v", roots)
	}
}

func TestASubagentsProjectIsTheRootsWhenTheRootHasNoRequestOfItsOwn(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, tagged("a", "m", "s1/ab", "s1", "-home-u-proj"), tok(1, 1, 1))
	if s1 := find(window(r, 0), "s1"); s1 == nil || s1.Project != "-home-u-proj" {
		t.Fatalf("%+v", s1)
	}
}

func TestTheLatestTagOfASessionWins(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, tagged("a", "m", "s1", "", "-old"), tok(1, 1, 1))
	r.AddTraffic(t0.Add(10*time.Minute), tagged("a", "m", "s1", "", "-new"), tok(1, 1, 1))
	if s1 := find(window(r, 0), "s1"); s1 == nil || s1.Project != "-new" {
		t.Fatalf("%+v", s1)
	}
}

func TestAProjectSurvivesTheDiskAndStaysOffTheStatusJSON(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	r.AddTraffic(t0, tagged("a", "m", "s1", "", "-home-u-proj"), tok(1, 1, 1))
	if err := r.Flush(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	w := NewRecorder(dir).sessionWindow(t0.Add(-time.Hour), t0.Add(time.Hour), 0)
	if s1 := find(w, "s1"); s1 == nil || s1.Project != "-home-u-proj" {
		t.Fatalf("%+v", s1)
	}
	if len(w.AllRoots) != 1 || w.AllRoots[0].Project != "-home-u-proj" {
		t.Fatalf("%+v", w.AllRoots)
	}
}
