package engine

import (
	"os"
	"strings"
)

// containers are folder names that only hold projects; a display name leaves them out.
var containers = []string{"Documents", "Projects", "Developer", "Desktop", "src", "code", "dev", "development", "work", "repos", "git"}

// worktreeMarkers split a worktree's folder into the repository and the worktree's name.
var worktreeMarkers = []string{"--claude-worktrees-", "-worktrees-"}

// userHome is the home directory of the user CPA runs as; "" when unknown.
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// encodeHome writes a path the way Claude Code names a project folder: "/" and "." become "-".
func encodeHome(path string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(path)
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
	for _, m := range worktreeMarkers {
		if i := strings.Index(key, m); i > 0 && i+len(m) < len(key) {
			repo, f.worktree = key[:i], key[i+len(m):]
			break
		}
	}
	f.segs = strings.Split(strings.TrimPrefix(repo, "-"), "-")
	eh := encodeHome(home)
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

// nameFolders sets the display name of every folder the view shows.
func (v *AffinityView) nameFolders(home string) {
	var folders []string
	for _, h := range v.Homes {
		folders = append(folders, h.Project)
	}
	for _, p := range v.FolderPins {
		folders = append(folders, p.Key)
	}
	for _, p := range v.Projects {
		folders = append(folders, p.Project)
	}
	for _, folder := range v.SessionProjects {
		folders = append(folders, folder)
	}
	for _, pick := range v.Sessions {
		folders = append(folders, pick.Project)
	}
	v.Names = FolderNames(folders, home)
}
