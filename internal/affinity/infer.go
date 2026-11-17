package affinity

import "strings"

// WorktreeMarkers split a worktree's folder key into the container and the worktree's name. "-work-"
// is a sibling folder that holds a repository's worktrees ("<repo>-work/<name>").
var WorktreeMarkers = []string{"--claude-worktrees-", "-worktrees-", "-work-"}

// containerOf is the key up to and including its last worktree marker: the folder that holds the
// worktrees. The last marker is the one that is nearest to the name, so a repository whose own
// name holds a marker still has the right container. ok is false when the key has no marker or
// nothing after it.
func containerOf(key string) (container string, ok bool) {
	end := -1
	for _, m := range WorktreeMarkers {
		if i := strings.LastIndex(key, m); i >= 0 && i+len(m) > end {
			end = i + len(m)
		}
	}
	if end < 0 || end >= len(key) {
		return "", false
	}
	return key[:end], true
}

// WorktreeName is the worktree's own name in a folder key: what follows the container, or "" when
// it has no container. It is for a worktree whose directory is gone.
func WorktreeName(key string) string {
	if container, ok := containerOf(key); ok {
		return key[len(container):]
	}
	return ""
}

// fromMemory is the place of a folder that was never found on the disk: its stored link, else the
// repository its container's worktrees belong to, else the folder itself. It reads no disk; the
// caller holds the lock.
func (r *Repos) fromMemory(folder string) Place {
	if project, ok := r.links.lookup(folder); ok {
		return Place{Project: project, Worktree: true}
	}
	if project, ok := r.infer(folder); ok {
		return Place{Project: project, Worktree: true, Inferred: true}
	}
	return Place{Project: folder}
}

// infer maps a folder that is gone, and was never resolved, to the repository of the worktrees that
// share its container. It answers only when every known worktree in that container belongs to one
// repository; with two, or with none, the folder stays its own project. The caller holds the lock.
func (r *Repos) infer(folder string) (project string, ok bool) {
	container, ok := containerOf(folder)
	if !ok {
		return "", false
	}
	for other, e := range r.links.byFolder {
		if c, has := containerOf(other); !has || c != container {
			continue
		}
		if project != "" && project != e.project {
			return "", false
		}
		project = e.project
	}
	return project, project != ""
}
