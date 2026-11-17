package affinity

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	repoTTL       = 10 * time.Minute // an answer is checked again after this long
	maxRepoCache  = 1000             // folders remembered
	maxDirReads   = 64               // directory listings to resolve one folder
	maxDirEntries = 200000           // entries read from one directory
	maxWalkDepth  = 40               // path segments of one folder
	maxGitFile    = 4096             // bytes read from a git file
)

// EncodePath writes a path the way Claude Code names a project folder: every character that is
// not an ASCII letter or digit becomes "-" ("/", ".", "_" and a space too).
func EncodePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xffff:
			b.WriteString("--") // Claude Code counts UTF-16 units
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Place is where a project folder lives and which project it belongs to.
type Place struct {
	// Project is the folder of the repository the folder belongs to: the folder itself, or, for a git
	// worktree, the main repository's folder key.
	Project string
	// Path is the folder's directory, found from its name; "" when it was not found.
	Path string
	// Worktree is true when the folder is a linked git worktree of another folder's repository.
	Worktree bool
	// Inferred is true when the folder is gone and was never found: it is put with the repository of the
	// worktrees in the same container (see Repos.infer), which may be wrong, so the page says so.
	Inferred bool
}

// Repos finds, from git's own small files, which repository a project folder belongs to. A folder
// key is a working directory with every "/" and "." (and other character) written as "-", so it is
// ambiguous: the directory is found by walking the file system from "/" and matching the encoded
// names of the entries. Then `.git` there says what the folder is: a directory means a main
// repository, a file means a linked worktree, whose main repository is the directory that holds the
// common git directory. Only those files are read, never git and never a transcript.
type Repos struct {
	now func() time.Time

	mu    sync.Mutex
	cache map[string]repoHit
	links links // the worktrees found, kept in the state file; see Take and Restore
}

type repoHit struct {
	place Place
	at    time.Time
}

func NewRepos(now func() time.Time) *Repos {
	return &Repos{now: now, cache: map[string]repoHit{}}
}

// Of is the place of a folder. An answer is kept for repoTTL, then checked again; a check that does
// not find the directory (a worktree removed since) keeps the earlier answer, as the sessions of
// a removed worktree stay in the history for a week. A worktree found is also remembered in the
// links, which the state file keeps: a folder never seen in this run, and gone, gets its stored answer. A folder that is not found, or is not a path,
// is its own project.
func (r *Repos) Of(folder string) Place {
	if folder == "" || !ValidProject(folder) {
		return Place{Project: folder}
	}
	now := r.now()
	r.mu.Lock()
	prev, hadHit := r.cache[folder]
	r.mu.Unlock()
	if hadHit && now.Sub(prev.at) < repoTTL {
		return prev.place
	}

	place, found := locate(folder) // reads the disk, outside the lock
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case found && place.Worktree:
		r.links.remember(folder, place.Project)
	case !found && hadHit:
		place = prev.place
	case !found:
		if project, ok := r.links.lookup(folder); ok {
			place = Place{Project: project, Worktree: true}
		} else if project, ok := r.infer(folder); ok {
			place = Place{Project: project, Worktree: true, Inferred: true}
		}
	}
	if len(r.cache) >= maxRepoCache {
		if _, ok := r.cache[folder]; !ok {
			clear(r.cache)
		}
	}
	r.cache[folder] = repoHit{place, now}
	return place
}

// Peek is the place of a folder from what is already known, without reading the disk; a folder not
// seen yet is its own project. It is for callers that hold the engine's lock.
func (r *Repos) Peek(folder string) Place {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hit, ok := r.cache[folder]; ok {
		return hit.place
	}
	return Place{Project: folder}
}

// size is the number of folders remembered, for the tests.
func (r *Repos) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cache)
}

// locate finds the folder's directory and its repository. found is false when no directory has the
// folder's name.
func locate(folder string) (place Place, found bool) {
	place = Place{Project: folder}
	if !strings.HasPrefix(folder, "-") {
		return place, false
	}
	budget := maxDirReads
	dir, ok := walk("/", folder[1:], 0, &budget)
	if !ok {
		return place, false
	}
	place.Path = dir
	if main, linked := mainRepository(dir); linked {
		if key := EncodePath(main); ValidProject(key) {
			place.Project, place.Worktree = key, true
		}
	}
	return place, true
}

// walk finds the directory below dir whose path, encoded, is rest. It tries the entries whose
// encoded name starts rest, the longest first, and backs off when one leads nowhere. The listings
// it may read, and the depth, are bounded: past them the folder is not found.
func walk(dir, rest string, depth int, budget *int) (string, bool) {
	if rest == "" {
		info, err := os.Stat(dir)
		return dir, err == nil && info.IsDir()
	}
	if depth >= maxWalkDepth || *budget <= 0 {
		return "", false
	}
	*budget--
	type step struct{ name, rest string }
	var steps []step
	for _, name := range listDir(dir) {
		enc := EncodePath(name)
		switch {
		case rest == enc:
			steps = append(steps, step{name, ""})
		case strings.HasPrefix(rest, enc+"-"):
			steps = append(steps, step{name, rest[len(enc)+1:]})
		}
	}
	// the longest name first: "my-app" before "my"
	sort.Slice(steps, func(i, j int) bool {
		if len(steps[i].rest) != len(steps[j].rest) {
			return len(steps[i].rest) < len(steps[j].rest)
		}
		return steps[i].name < steps[j].name
	})
	for _, s := range steps {
		if found, ok := walk(filepath.Join(dir, s.name), s.rest, depth+1, budget); ok {
			return found, true
		}
	}
	return "", false
}

// listDir is the names in a directory that may be a directory (a regular file cannot be).
func listDir(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(maxDirEntries)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() && utf8.ValidString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

// mainRepository reads dir's `.git`. A directory there means dir is a main repository (linked is
// false). A file is a linked worktree when it names a git directory `<common>/worktrees/<name>`
// that exists; the main repository is then the directory that holds the common git directory
// (`<gitdir>/commondir` says where it is, and `../..` of the git directory is the default). Any
// other `.git` file, such as a submodule's, leaves dir its own repository.
func mainRepository(dir string) (main string, linked bool) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	line, _, _ := strings.Cut(readSmall(dotGit), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir := absFrom(dir, strings.TrimSpace(rest))
	if gitdir == "" || filepath.Base(filepath.Dir(gitdir)) != "worktrees" {
		return "", false
	}
	if info, err := os.Stat(gitdir); err != nil || !info.IsDir() {
		return "", false
	}
	common := filepath.Dir(filepath.Dir(gitdir))
	if c := strings.TrimSpace(readSmall(filepath.Join(gitdir, "commondir"))); c != "" {
		common = absFrom(gitdir, c)
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), true
	}
	return common, true // a bare repository is its own main folder
}

// absFrom is path as an absolute, clean path; a relative one is relative to base. "" for no path.
func absFrom(base, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// readSmall is the first maxGitFile bytes of a file; "" when it cannot be read.
func readSmall(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, maxGitFile))
	return string(data)
}
