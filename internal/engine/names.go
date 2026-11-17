package engine

import (
	"os"
	"strings"

	"github.com/wappsdev/cpa-balancer/internal/affinity"
)

// containers are folder names that only hold projects; a display name leaves them out.
var containers = []string{"Documents", "Projects", "Developer", "Desktop", "src", "code", "dev", "development", "work", "repos", "git"}

// userHome is the home directory of the user CPA runs as; "" when unknown.
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func isContainer(seg string) bool {
	for _, c := range containers {
		if strings.EqualFold(seg, c) {
			return true
		}
	}
	return false
}

// folderName is one folder cut into the segments of its repository, the segment where its short name
// starts, and the worktree's name when it is a worktree.
type folderName struct {
	key      string
	segs     []string
	start    int
	worktree string
	fixed    bool // the key itself is the name
}

func (f folderName) display() string {
	if f.fixed {
		return f.key
	}
	name := strings.Join(f.segs[f.start:], "-")
	if name == "" {
		return f.key
	}
	if f.worktree != "" {
		return name + " / " + f.worktree
	}
	return name
}

func parseFolder(key, home string) folderName {
	f := folderName{key: key}
	repo := key
	eh := affinity.EncodePath(home)
	for _, m := range affinity.WorktreeMarkers {
		i := strings.Index(key, m)
		if i <= 0 || i+len(m) >= len(key) {
			continue
		}
		// The part before the marker must name a repository: not the home itself, and not only
		// container folders (a "work" folder that holds projects is a container, not a worktree's).
		before := key[:i]
		if before == eh || onlyContainers(strings.TrimPrefix(strings.TrimPrefix(before, eh), "-")) {
			continue
		}
		repo, f.worktree = before, key[i+len(m):]
		break
	}
	f.segs = strings.Split(strings.TrimPrefix(repo, "-"), "-")
	switch {
	case len(eh) > 1 && repo == eh:
		f.fixed = true // the home itself
		return f
	case len(eh) > 1 && strings.HasPrefix(repo, eh+"-"):
		f.start = strings.Count(eh, "-")
	case len(f.segs) >= 3 && f.segs[0] == "Volumes":
		f.start = 2 // the disk's name follows
	}
	for f.start < len(f.segs)-1 && isContainer(f.segs[f.start]) {
		f.start++
	}
	return f
}

// FolderNames gives each project folder a short name for the page: the user's home and the
// containers above the project are cut off, a worktree reads "repo / name", and two folders with one
// name get back just enough of the cut part to differ. The folder stays the key everywhere else.
func FolderNames(folders []string, home string) map[string]string {
	all := map[string]*folderName{}
	for _, key := range folders {
		if key == "" || all[key] != nil {
			continue
		}
		f := parseFolder(key, home)
		all[key] = &f
	}
	for {
		byName := map[string][]*folderName{}
		for _, f := range all {
			byName[f.display()] = append(byName[f.display()], f)
		}
		moved := false
		for _, group := range byName {
			if len(group) < 2 {
				continue
			}
			for _, f := range group {
				if !f.fixed && f.start > 0 {
					f.start--
					moved = true
				}
			}
		}
		if !moved {
			break
		}
	}
	out := make(map[string]string, len(all))
	for key, f := range all {
		out[key] = f.display()
	}
	return out
}

// folders is every folder the view shows, in no order and with repeats.
func (v *AffinityView) folders() []string {
	var folders []string
	for _, h := range v.Homes {
		folders = append(folders, h.Project)
	}
	for _, p := range v.FolderPins {
		folders = append(folders, p.Key)
	}
	for _, p := range v.Projects {
		folders = append(folders, p.Project)
		for _, w := range p.Worktrees {
			folders = append(folders, w.Project)
		}
	}
	for _, folder := range v.SessionProjects {
		folders = append(folders, folder)
	}
	for _, pick := range v.Sessions {
		folders = append(folders, pick.Project)
	}
	return folders
}

// nameFolders sets the display name of every folder the view shows.
func (v *AffinityView) nameFolders(home string) {
	v.Names = FolderNames(v.folders(), home)
}

// onlyContainers reports whether every segment of an encoded path is a container folder (or the
// path is empty), so it names no repository.
func onlyContainers(encoded string) bool {
	if encoded == "" {
		return true
	}
	for _, seg := range strings.Split(encoded, "-") {
		if seg != "" && !isContainer(seg) {
			return false
		}
	}
	return true
}
