package affinity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tree is a made-up directory tree for the tests; every path is under one temp directory.
type tree struct {
	t    *testing.T
	root string
}

func newTree(t *testing.T) *tree { return &tree{t: t, root: t.TempDir()} }

func (tr *tree) path(parts ...string) string {
	return filepath.Join(append([]string{tr.root}, parts...)...)
}

func (tr *tree) dir(parts ...string) string {
	tr.t.Helper()
	p := tr.path(parts...)
	if err := os.MkdirAll(p, 0o700); err != nil {
		tr.t.Fatal(err)
	}
	return p
}

func (tr *tree) file(content string, parts ...string) string {
	tr.t.Helper()
	p := tr.path(parts...)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		tr.t.Fatal(err)
	}
	return p
}

// repo makes a main repository: a directory with a .git directory.
func (tr *tree) repo(parts ...string) string {
	tr.t.Helper()
	tr.dir(append(append([]string{}, parts...), ".git")...)
	return tr.path(parts...)
}

// worktree makes a worktree of repo at the given path, the way git does: a .git file in the
// worktree, and the worktree's git directory under the repository's .git/worktrees with a
// commondir file.
func (tr *tree) worktree(repo string, at ...string) string {
	tr.t.Helper()
	dir := tr.path(at...)
	gitdir := filepath.Join(repo, ".git", "worktrees", filepath.Base(dir))
	if err := os.MkdirAll(gitdir, 0o700); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		tr.t.Fatal(err)
	}
	tr.file("gitdir: "+gitdir+"\n", append(append([]string{}, at...), ".git")...)
	return dir
}

func TestEncodePathMakesEveryOtherCharacterADash(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b.c/d_e f": "-a-b-c-d-e-f",
		"/a-b/.hidden": "-a-b--hidden",
		"/Ünï/x":       "--n--x",
	} {
		if got := EncodePath(in); got != want {
			t.Errorf("EncodePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAMainRepositoryIsItsOwnProject(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	key := EncodePath(repo)
	got := NewRepos(time.Now).Of(key)
	if got.Project != key || got.Worktree || got.Path != repo {
		t.Fatalf("%+v", got)
	}
}

func TestWorktreesInEveryPlaceBelongToTheirMainRepository(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("work", "shop")
	siblings := tr.worktree(repo, "work", "shop-work", "feature-a")
	inside := tr.worktree(repo, "work", "shop", ".claude", "worktrees", "feature-b")
	elsewhere := tr.worktree(repo, "scratch", "feature-c")
	r := NewRepos(time.Now)
	for _, dir := range []string{siblings, inside, elsewhere} {
		got := r.Of(EncodePath(dir))
		if got.Project != EncodePath(repo) || !got.Worktree || got.Path != dir {
			t.Errorf("%s: %+v", dir, got)
		}
	}
}

func TestDashesAndDotsInsideNamesResolveWithTheRightSplit(t *testing.T) {
	tr := newTree(t)
	// decoys: shorter names that match the start of the key but lead nowhere
	tr.dir("my")
	tr.dir("my-photo")
	tr.dir("my-photo.app", "v2-x")
	repo := tr.repo("my-photo.app-v2")
	wt := tr.worktree(repo, "my-photo.app-v2", ".claude", "worktrees", "fix-login.v2")
	got := NewRepos(time.Now).Of(EncodePath(wt))
	if got.Project != EncodePath(repo) || got.Path != wt {
		t.Fatalf("%+v", got)
	}
}

func TestAFolderThatDoesNotExistStaysItsOwnProject(t *testing.T) {
	tr := newTree(t)
	key := EncodePath(tr.path("gone", "feature"))
	got := NewRepos(time.Now).Of(key)
	if got.Project != key || got.Worktree || got.Path != "" {
		t.Fatalf("%+v", got)
	}
	for _, k := range []string{"weird", "-", "--", "-no-such-root-dir-at-all"} {
		if got := NewRepos(time.Now).Of(k); got.Project != k || got.Worktree {
			t.Errorf("%q: %+v", k, got)
		}
	}
}

func TestAFolderThatIsNotARepositoryStaysItsOwnProject(t *testing.T) {
	tr := newTree(t)
	dir := tr.dir("notes")
	key := EncodePath(dir)
	if got := NewRepos(time.Now).Of(key); got.Project != key || got.Worktree || got.Path != dir {
		t.Fatalf("%+v", got)
	}
}

func TestASubmoduleIsNotAWorktree(t *testing.T) {
	tr := newTree(t)
	super := tr.repo("super")
	tr.dir("super", ".git", "modules", "lib")
	tr.file("gitdir: "+filepath.Join(super, ".git", "modules", "lib")+"\n", "super", "lib", ".git")
	key := EncodePath(tr.path("super", "lib"))
	if got := NewRepos(time.Now).Of(key); got.Project != key || got.Worktree {
		t.Fatalf("a submodule is its own project: %+v", got)
	}
}

func TestAWorktreeWithoutACommondirFileAndWithARelativeGitdir(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	tr.dir("shop", ".git", "worktrees", "x")
	tr.file("gitdir: ../../shop/.git/worktrees/x\n\n", "linked", "x", ".git")
	key := EncodePath(tr.path("linked", "x"))
	if got := NewRepos(time.Now).Of(key); got.Project != EncodePath(repo) || !got.Worktree {
		t.Fatalf("%+v", got)
	}
}

func TestAGitFileThatIsNotUsableLeavesTheFolderItsOwnProject(t *testing.T) {
	tr := newTree(t)
	for name, content := range map[string]string{
		"empty":   "",
		"junk":    "hello\n",
		"huge":    "gitdir: " + strings.Repeat("a", 10000),
		"nowhere": "gitdir: /no/such/place/.git/worktrees/x\n",
	} {
		tr.file(content, name, ".git")
		key := EncodePath(tr.path(name))
		if got := NewRepos(time.Now).Of(key); got.Project != key || got.Worktree {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestAnAnswerIsKeptUntilTheTTLThenChecked(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	wt := tr.worktree(repo, "shop-work", "a")
	clock := time.Now()
	r := NewRepos(func() time.Time { return clock })
	key := EncodePath(wt)
	if !r.Of(key).Worktree {
		t.Fatal("setup")
	}
	tr.file("not a git file\n", "shop-work", "a", ".git")
	if !r.Of(key).Worktree {
		t.Fatal("within the TTL the answer is kept")
	}
	clock = clock.Add(repoTTL + time.Second)
	if got := r.Of(key); got.Worktree || got.Project != key {
		t.Fatalf("after the TTL the folder is checked again: %+v", got)
	}
}

func TestAWorktreeRemovedAfterwardsKeepsItsProject(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	wt := tr.worktree(repo, "shop-work", "a")
	clock := time.Now()
	r := NewRepos(func() time.Time { return clock })
	key := EncodePath(wt)
	want := r.Of(key)
	if !want.Worktree {
		t.Fatal("setup")
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(repoTTL + time.Second)
	if got := r.Of(key); got != want {
		t.Fatalf("a re-check that finds nothing keeps the earlier answer: %+v", got)
	}
	if got := NewRepos(time.Now).Of(key); got.Project != key {
		t.Fatalf("a resolver that never saw it has only the folder: %+v", got)
	}
}

func TestPeekNeverReadsTheDisk(t *testing.T) {
	tr := newTree(t)
	repo := tr.repo("shop")
	wt := tr.worktree(repo, "shop-work", "a")
	key := EncodePath(wt)
	r := NewRepos(time.Now)
	if got := r.Peek(key); got.Project != key || got.Worktree {
		t.Fatalf("an unknown folder is its own project: %+v", got)
	}
	r.Of(key)
	if got := r.Peek(key); got.Project != EncodePath(repo) || !got.Worktree {
		t.Fatalf("%+v", got)
	}
}

func TestTheWorkForOneFolderAndTheCacheAreBounded(t *testing.T) {
	r := NewRepos(time.Now)
	deep := "-" + strings.Repeat("a-", 500) + "b"
	start := time.Now()
	if got := r.Of(deep); got.Project != deep {
		t.Fatalf("%+v", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a long key took too long")
	}
	for i := 0; i < maxRepoCache+50; i++ {
		r.Of("-no-such-folder-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26))
	}
	if r.size() > maxRepoCache {
		t.Fatalf("cache holds %d", r.size())
	}
}
