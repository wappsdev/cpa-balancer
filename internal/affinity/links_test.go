package affinity

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAWorktreeFoundOnceKeepsItsProjectAfterTheDirectoryIsGone(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	wt := tr.worktree(repo, "shop-work", "a")
	key := EncodePath(wt)
	first := NewRepos(time.Now)
	first.Of(key)
	links, changed := first.Take()
	if !changed || len(links) != 1 || links[0] != (Link{Folder: key, Project: EncodePath(repo)}) {
		t.Fatalf("the worktree is remembered: %+v %v", links, changed)
	}
	if _, changed := first.Take(); changed {
		t.Fatal("nothing changed since the last Take")
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	second := NewRepos(time.Now) // a restart: only the stored links are known
	second.Restore(links)
	if got := second.Of(key); got.Project != EncodePath(repo) || !got.Worktree {
		t.Fatalf("the stored answer is used when the folder is gone: %+v", got)
	}
}

func TestOnlyWorktreesAreRemembered(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	r := NewRepos(time.Now)
	r.Of(EncodePath(repo))
	r.Of("-no-such-folder-at-all")
	if links, changed := r.Take(); changed || len(links) != 0 {
		t.Fatalf("%+v", links)
	}
}

func TestTheRememberedWorktreesAreBoundedAndTheLeastRecentlyUsedGoFirst(t *testing.T) {
	r := NewRepos(time.Now)
	var links []Link
	for i := 0; i < MaxLinks; i++ {
		links = append(links, Link{Folder: fmt.Sprintf("-x-w-%d", i), Project: "-x-repo"})
	}
	r.Restore(links)
	r.Of("-x-w-0") // used again: now the most recent
	r.mu.Lock()
	r.links.remember("-x-w-new", "-x-repo")
	r.mu.Unlock()
	got, _ := r.Take()
	if len(got) != MaxLinks {
		t.Fatalf("links: %d", len(got))
	}
	for _, l := range got {
		if l.Folder == "-x-w-1" {
			t.Fatal("the least recently used link is dropped")
		}
	}
	if got[len(got)-1].Folder != "-x-w-new" || got[len(got)-2].Folder != "-x-w-0" {
		t.Fatalf("the order is least to most recent: %+v", got[len(got)-2:])
	}
}

func TestRestoreSkipsLinksTheBoundsForbid(t *testing.T) {
	r := NewRepos(time.Now)
	r.Restore([]Link{{Folder: "a/b", Project: "-x-repo"}, {Folder: "-x-w", Project: ""}, {Folder: "-x-w2", Project: "-x-repo"}})
	if got, changed := r.Take(); changed || len(got) != 1 {
		t.Fatalf("a restored link is not a change, and only the valid one stays: %+v", got)
	}
	if got := r.Of("-x-w2"); got.Project != "-x-repo" || !got.Worktree {
		t.Fatalf("%+v", got)
	}
	if r.Of("a/b").Project != "a/b" {
		t.Fatal("an invalid folder is its own project")
	}
}
