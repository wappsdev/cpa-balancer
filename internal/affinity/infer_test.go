package affinity

import (
	"testing"
	"time"
)

func TestAGoneFolderInTheContainerOfAResolvedWorktreeJoinsItsRepository(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	tr.worktree(repo, "shop-work", "a")
	gone := EncodePath(tr.path("shop-work", "b")) // never existed on this machine's disk now
	insideGone := EncodePath(tr.path("shop", ".claude", "worktrees", "c"))
	r := NewRepos(time.Now)
	r.Of(EncodePath(tr.path("shop-work", "a")))
	r.Of(EncodePath(tr.path("shop", ".claude", "worktrees", "other")))
	got := r.Of(gone)
	if got.Project != EncodePath(repo) || !got.Worktree || !got.Inferred {
		t.Fatalf("the gone folder joins the repository, marked as inferred: %+v", got)
	}
	if got := r.Peek(gone); got.Project != EncodePath(repo) || !got.Inferred {
		t.Fatalf("Peek knows it too: %+v", got)
	}
	if got := r.Of(insideGone); got.Inferred {
		t.Fatalf("a container with no resolved worktree infers nothing: %+v", got)
	}
	if links, _ := r.Take(); len(links) != 1 {
		t.Fatalf("an inferred mapping is not remembered, only the resolved worktree: %+v", links)
	}
}

func TestAGoneFolderAskedBeforeItsSiblingIsResolvedIsInferredAfterTheTTL(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	sibling := tr.worktree(repo, "shop-work", "a")
	gone := EncodePath(tr.path("shop-work", "b"))
	clock := time.Now()
	r := NewRepos(func() time.Time { return clock })
	if got := r.Of(gone); got.Worktree {
		t.Fatalf("nothing is known yet: %+v", got)
	}
	r.Of(EncodePath(sibling))
	clock = clock.Add(repoTTL + time.Second)
	if got := r.Of(gone); got.Project != EncodePath(repo) || !got.Inferred {
		t.Fatalf("a check after the TTL re-runs the inference, with no disk read: %+v", got)
	}
}

func TestAnInferredAnswerIsDroppedWhenTheContainerBecomesAmbiguous(t *testing.T) {
	clock := time.Now()
	r := NewRepos(func() time.Time { return clock })
	r.Restore([]Link{{Folder: "-x-scratch-work-a", Project: "-x-shop"}})
	if got := r.Of("-x-scratch-work-c"); !got.Inferred {
		t.Fatalf("%+v", got)
	}
	r.mu.Lock()
	r.links.remember("-x-scratch-work-b", "-x-blog")
	r.mu.Unlock()
	clock = clock.Add(repoTTL + time.Second)
	if got := r.Of("-x-scratch-work-c"); got.Worktree || got.Inferred {
		t.Fatalf("%+v", got)
	}
}

func TestInferenceUsesTheStoredLinksAfterARestart(t *testing.T) {
	r := NewRepos(time.Now)
	r.Restore([]Link{{Folder: "-x-shop-work-a", Project: "-x-shop"}})
	if got := r.Of("-x-shop-work-b"); got.Project != "-x-shop" || !got.Inferred {
		t.Fatalf("%+v", got)
	}
	if got := r.Of("-x-shop-worktrees-b"); got.Inferred {
		t.Fatalf("another container: %+v", got)
	}
}

func TestAContainerWithTwoRepositoriesInfersNothing(t *testing.T) {
	r := NewRepos(time.Now)
	r.Restore([]Link{{Folder: "-x-scratch-work-a", Project: "-x-shop"}, {Folder: "-x-scratch-work-b", Project: "-x-blog"}})
	if got := r.Of("-x-scratch-work-c"); got.Project != "-x-scratch-work-c" || got.Worktree || got.Inferred {
		t.Fatalf("ambiguous: the folder stays separate: %+v", got)
	}
}

func TestAFolderOutsideAnyWorktreeContainerIsNotInferred(t *testing.T) {
	r := NewRepos(time.Now)
	r.Restore([]Link{{Folder: "-x-shop-work-a", Project: "-x-shop"}})
	for _, key := range []string{"-x-notes", "-x-shop", "-x-other-work-a"} {
		if got := r.Of(key); got.Project != key || got.Inferred {
			t.Errorf("%s: %+v", key, got)
		}
	}
}

func TestAFolderThatCannotBeFoundOnTheDiskButIsRememberedIsNotInferred(t *testing.T) {
	r := NewRepos(time.Now)
	r.Restore([]Link{{Folder: "-x-shop-work-a", Project: "-x-shop"}, {Folder: "-x-shop-work-b", Project: "-x-shop"}})
	if got := r.Of("-x-shop-work-b"); got.Inferred {
		t.Fatalf("a stored answer is a fact: %+v", got)
	}
}
