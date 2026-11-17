package affinity

import "sort"

// MaxLinks bounds the worktree folders remembered. The least recently used one is dropped to make
// room.
const MaxLinks = 1000

// Link is a worktree folder and the main repository's folder it belongs to. The links are kept in
// the state file, so that a worktree removed from the disk still joins its repository after a
// restart.
type Link struct {
	Folder  string `json:"folder"`
	Project string `json:"project"`
}

type linkEntry struct {
	project string
	use     uint64 // order of use; the smallest is the least recent
}

// links is the remembered worktree folders; the Repos lock guards it.
type links struct {
	byFolder map[string]*linkEntry
	clock    uint64
	changed  bool
}

func (l *links) touch(e *linkEntry) {
	l.clock++
	e.use = l.clock
}

// remember records that folder is a worktree of project; callers hold the Repos lock.
func (l *links) remember(folder, project string) {
	if l.byFolder == nil {
		l.byFolder = map[string]*linkEntry{}
	}
	if e, ok := l.byFolder[folder]; ok {
		l.touch(e)
		if e.project != project {
			e.project, l.changed = project, true
		}
		return
	}
	e := &linkEntry{project: project}
	l.touch(e)
	l.byFolder[folder] = e
	l.changed = true
	if len(l.byFolder) > MaxLinks {
		oldest, first := "", true
		for k, v := range l.byFolder {
			if first || v.use < l.byFolder[oldest].use || (v.use == l.byFolder[oldest].use && k < oldest) {
				oldest, first = k, false
			}
		}
		delete(l.byFolder, oldest)
	}
}

// lookup is the project remembered for folder, and counts as a use.
func (l *links) lookup(folder string) (string, bool) {
	e, ok := l.byFolder[folder]
	if !ok {
		return "", false
	}
	l.touch(e)
	return e.project, true
}

// Restore loads the links read from the state file. A link the bounds forbid is skipped, and what
// is loaded does not count as a change.
func (r *Repos) Restore(saved []Link) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(saved) > MaxLinks {
		saved = saved[len(saved)-MaxLinks:]
	}
	for _, l := range saved {
		if ValidProject(l.Folder) && ValidProject(l.Project) && l.Folder != l.Project {
			r.links.remember(l.Folder, l.Project)
		}
	}
	r.links.changed = false
}

// Take is the remembered links, the least recently used first, and whether they changed since the
// last Take.
func (r *Repos) Take() (all []Link, changed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed, r.links.changed = r.links.changed, false
	all = make([]Link, 0, len(r.links.byFolder))
	for folder, e := range r.links.byFolder {
		all = append(all, Link{Folder: folder, Project: e.project})
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := r.links.byFolder[all[i].Folder], r.links.byFolder[all[j].Folder]
		return a.use < b.use || (a.use == b.use && all[i].Folder < all[j].Folder)
	})
	return all, changed
}
