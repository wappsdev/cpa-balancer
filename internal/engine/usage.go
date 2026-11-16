package engine

import (
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

// Use is what a project's sessions spent in a window.
type Use struct {
	Requests int64     `json:"requests"`
	Tokens   int64     `json:"tokens"` // input, output and cache writes
	Last     time.Time `json:"last,omitzero"`
}

// ProjectUsage is one project's use in the status's two windows. The project "" holds what has no
// project: sessions whose folder is not found, and requests that came without a session id.
type ProjectUsage struct {
	Project  string `json:"project"`
	FiveHour Use    `json:"five_hour"`
	Week     Use    `json:"week"`
}

// byProject sums root sessions per project folder. It looks the folders up by name, so callers hold
// no lock.
func (e *Engine) byProject(roots []history.RootUse) map[string]Use {
	out := map[string]Use{}
	for _, root := range roots {
		folder, _ := e.projects.Folder(root.ID) // an empty id or a miss is the group of no project
		u := out[folder]
		u.Requests += root.Total.Requests
		u.Tokens += root.Total.Tokens()
		if root.Last.After(u.Last) {
			u.Last = root.Last
		}
		out[folder] = u
	}
	return out
}

// AddSessions puts the history's sessions into the status, with what the card groups them by: each
// project's use in both windows, summed from every root session (the week's list is cut, the sums
// are not), and the folder of each listed root session. The sessions are read from the history
// files, so this runs outside the engine's lock.
func (e *Engine) AddSessions(v *View, s history.Sessions) {
	v.Sessions = &s
	if v.Affinity == nil {
		return
	}
	five, week := e.byProject(s.FiveHour.AllRoots), e.byProject(s.Week.AllRoots)
	projects := map[string]*ProjectUsage{}
	get := func(name string) *ProjectUsage {
		if projects[name] == nil {
			projects[name] = &ProjectUsage{Project: name}
		}
		return projects[name]
	}
	for name, u := range five {
		get(name).FiveHour = u
	}
	for name, u := range week {
		get(name).Week = u
	}
	v.Affinity.Projects = make([]ProjectUsage, 0, len(projects))
	for _, p := range projects {
		v.Affinity.Projects = append(v.Affinity.Projects, *p)
	}
	sort.Slice(v.Affinity.Projects, func(i, j int) bool { return v.Affinity.Projects[i].Project < v.Affinity.Projects[j].Project })
	v.Affinity.SessionProjects = map[string]string{}
	for _, window := range []history.SessionWindow{s.FiveHour, s.Week} {
		for _, root := range window.Sessions {
			if folder, ok := e.projects.Folder(root.ID); ok {
				v.Affinity.SessionProjects[root.ID] = folder
			}
		}
	}
}
