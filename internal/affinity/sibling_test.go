package affinity

import (
	"testing"
	"time"
)

func TestAGoneSiblingWorktreeJoinsTheLiveRepositoryItIsNamedAfter(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	gone := EncodePath(tr.path("shop-fix")) // the directory does not exist
	r := NewRepos(time.Now)
	r.Of(EncodePath(repo))
	got := r.Of(gone)
	if got.Project != EncodePath(repo) || !got.Worktree || !got.Inferred {
		t.Fatalf("the gone sibling joins its repository, marked as inferred: %+v", got)
	}
	if got := r.Peek(gone); got.Project != EncodePath(repo) || !got.Inferred {
		t.Fatalf("Peek knows it too: %+v", got)
	}
	if links, _ := r.Take(); len(links) != 0 {
		t.Fatalf("an inferred mapping is not remembered: %+v", links)
	}
}

func TestAGoneSiblingAskedBeforeTheRepositoryIsResolvedIsInferredAfterTheTTL(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	gone := EncodePath(tr.path("shop-fix"))
	clock := time.Now()
	r := NewRepos(func() time.Time { return clock })
	if got := r.Of(gone); got.Worktree {
		t.Fatalf("nothing is known yet: %+v", got)
	}
	r.Of(EncodePath(repo))
	clock = clock.Add(repoTTL + time.Second)
	if got := r.Of(gone); got.Project != EncodePath(repo) || !got.Inferred {
		t.Fatalf("a check after the TTL re-runs the inference: %+v", got)
	}
}

func TestAnExistingFolderNamedAfterARepositoryStaysSeparate(t *testing.T) {
	tr := newTree(t)
	tr.repo("shop")
	web := tr.repo("shop-web")
	r := NewRepos(time.Now)
	r.Of(EncodePath(tr.path("shop")))
	got := r.Of(EncodePath(web))
	if got.Project != EncodePath(web) || got.Worktree || got.Inferred {
		t.Fatalf("a live repository resolves itself: %+v", got)
	}
}

func TestTheLongestLiveRepositoryNameWins(t *testing.T) {
	tr := newTree(t)
	tr.repo("shop")
	api := tr.repo("shop-api")
	r := NewRepos(time.Now)
	r.Of(EncodePath(tr.path("shop")))
	r.Of(EncodePath(api))
	got := r.Of(EncodePath(tr.path("shop-api-v2")))
	if got.Project != EncodePath(api) || !got.Inferred {
		t.Fatalf("shop-api-v2 belongs to shop-api: %+v", got)
	}
}

func TestAGoneFolderWithNoLiveRepositoryPrefixStaysSeparate(t *testing.T) {
	tr := newTree(t)
	tr.repo("shop")
	r := NewRepos(time.Now)
	r.Of(EncodePath(tr.path("shop")))
	for _, key := range []string{
		EncodePath(tr.path("cart-fix")),
		EncodePath(tr.path("shopfix")),
		EncodePath(tr.path("x", "shop-fix")), // not the same parent directory
	} {
		if got := r.Of(key); got.Project != key || got.Inferred {
			t.Errorf("%s: %+v", key, got)
		}
	}
}

func TestOnlyAMainRepositoryIsAPrefixCandidate(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	wt := tr.worktree(repo, "shop-a")
	tr.dir("notes")
	r := NewRepos(time.Now)
	r.Of(EncodePath(wt))
	r.Of(EncodePath(tr.path("notes")))
	for _, p := range []string{"shop-a-fix", "notes-fix"} {
		key := EncodePath(tr.path(p))
		if got := r.Of(key); got.Project != key || got.Inferred {
			t.Errorf("%s: a worktree or a plain folder is not a repository: %+v", p, got)
		}
	}
}

func TestAStoredLinkIsPreferredOverAPrefixInference(t *testing.T) {
	tr := newTree(t)
	tr.repo("shop")
	tr.repo("other")
	gone := EncodePath(tr.path("shop-fix"))
	r := NewRepos(time.Now)
	r.Restore([]Link{{Folder: gone, Project: EncodePath(tr.path("other"))}})
	r.Of(EncodePath(tr.path("shop")))
	got := r.Of(gone)
	if got.Project != EncodePath(tr.path("other")) || got.Inferred {
		t.Fatalf("the stored mapping wins: %+v", got)
	}
}
