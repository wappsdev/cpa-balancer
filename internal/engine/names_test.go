package engine

import (
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

const aliceHome = "/Users/alice"

func TestFolderNames(t *testing.T) {
	cases := []struct {
		name    string
		folders []string
		want    map[string]string
	}{
		{"home prefix and containers go", []string{"-Users-alice-Documents-Projects-shop"}, map[string]string{"-Users-alice-Documents-Projects-shop": "shop"}},
		{"a work folder that holds projects is a container", []string{"-Users-alice-work-shop"}, map[string]string{"-Users-alice-work-shop": "shop"}},
		{"a sibling -work folder holds worktrees", []string{"-Users-alice-Documents-Projects-shop-work-tv-app"}, map[string]string{"-Users-alice-Documents-Projects-shop-work-tv-app": "shop / tv-app"}},
		{"a repo name with dashes stays whole", []string{"-Users-alice-Documents-Projects-photo-sync-app"}, map[string]string{"-Users-alice-Documents-Projects-photo-sync-app": "photo-sync-app"}},
		{"containers match without case", []string{"-Users-alice-DEVELOPER-Src-shop"}, map[string]string{"-Users-alice-DEVELOPER-Src-shop": "shop"}},
		{"a folder outside home drops Volumes and the disk", []string{"-Volumes-Data-development-api"}, map[string]string{"-Volumes-Data-development-api": "api"}},
		{"a folder outside home with a container first", []string{"-work-alpha"}, map[string]string{"-work-alpha": "alpha"}},
		{"a claude worktree", []string{"-Users-alice-Documents-Projects-shop--claude-worktrees-fix-cart"}, map[string]string{"-Users-alice-Documents-Projects-shop--claude-worktrees-fix-cart": "shop / fix-cart"}},
		{"a plain worktree", []string{"-Users-alice-Documents-Projects-shop-worktrees-fix-cart"}, map[string]string{"-Users-alice-Documents-Projects-shop-worktrees-fix-cart": "shop / fix-cart"}},
		{"the home itself falls back to the key", []string{"-Users-alice"}, map[string]string{"-Users-alice": "-Users-alice"}},
		{"only containers keep the last segment", []string{"-Users-alice-Documents-Projects"}, map[string]string{"-Users-alice-Documents-Projects": "Projects"}},
		{"an unknown shape stays as it is", []string{"weird", "-opt-tools"}, map[string]string{"weird": "weird", "-opt-tools": "opt-tools"}},
		{"no project has no name", []string{""}, map[string]string{}},
		{"same name: add back the nearest dropped segment",
			[]string{"-Users-alice-Documents-Projects-shop", "-Users-alice-Developer-shop", "-Users-alice-Documents-Projects-blog"},
			map[string]string{"-Users-alice-Documents-Projects-shop": "Projects-shop", "-Users-alice-Developer-shop": "Developer-shop", "-Users-alice-Documents-Projects-blog": "blog"}},
		{"same name in home and on a volume",
			[]string{"-Users-alice-Documents-Projects-shop", "-Volumes-Data-development-shop"},
			map[string]string{"-Users-alice-Documents-Projects-shop": "Projects-shop", "-Volumes-Data-development-shop": "development-shop"}},
		{"a worktree of the same name is told apart already",
			[]string{"-Users-alice-Developer-shop", "-Users-alice-Developer-shop--claude-worktrees-shop"},
			map[string]string{"-Users-alice-Developer-shop": "shop", "-Users-alice-Developer-shop--claude-worktrees-shop": "shop / shop"}},
	}
	for _, c := range cases {
		got := FolderNames(c.folders, aliceHome)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v", c.name, got)
			continue
		}
		for k, w := range c.want {
			if got[k] != w {
				t.Errorf("%s: %q is %q, want %q", c.name, k, got[k], w)
			}
		}
	}
}

func TestFolderNamesWithoutAHome(t *testing.T) {
	got := FolderNames([]string{"-Users-alice-Documents-shop"}, "")
	if got["-Users-alice-Documents-shop"] != "Users-alice-Documents-shop" {
		t.Fatalf("%v", got)
	}
}

func TestFolderNamesAreNeverEmptyForANonEmptyKey(t *testing.T) {
	for _, f := range []string{"-", "--", "-Users-alice-", "-Users-alice--claude-worktrees-x", "-Volumes-Data"} {
		if FolderNames([]string{f}, aliceHome)[f] == "" {
			t.Errorf("%q has no name", f)
		}
	}
}

func TestAHomeWithOtherCharactersInItsNameIsCutOffToo(t *testing.T) {
	got := FolderNames([]string{"-Users-al-ice-Documents-shop"}, "/Users/al_ice")
	if got["-Users-al-ice-Documents-shop"] != "shop" {
		t.Fatal(got)
	}
}

func TestTheStatusNamesEveryFolderItShows(t *testing.T) {
	w, rec := balanced(t)
	w.folder("-work-a", 1)
	w.cfg.Affinity.FolderPins = map[string]string{"-work-pinned": W}
	w.e = New(w.cfg, newHost(), w.clock.now, rec)
	rec.AddTraffic(w.clock.t, history.Source{Account: W, Model: Opus, Session: sidN(1)}, history.Traffic{Requests: 1, InputTokens: 5})
	v := w.e.Status()
	if v.Affinity.Names["-work-pinned"] != "pinned" {
		t.Fatalf("a pin is named before the sessions are added: %v", v.Affinity.Names)
	}
	w.e.AddSessions(&v, rec.Sessions(w.clock.t))
	n := v.Affinity.Names
	if n["-work-a"] != "a" || n["-work-pinned"] != "pinned" {
		t.Fatalf("project rows and pins are named: %v", n)
	}
	for _, p := range v.Affinity.Projects {
		if _, ok := n[p.Project]; !ok && p.Project != "" {
			t.Fatalf("project %q has no name", p.Project)
		}
	}
	if _, ok := n[""]; ok {
		t.Fatal("no project is not a folder")
	}
}
