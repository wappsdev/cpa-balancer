package engine

import (
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/probe"
)

// Use is what a project's sessions spent in a window.
type Use struct {
	Requests int64     `json:"requests"`
	Tokens   int64     `json:"tokens"` // input, output and cache writes
	Last     time.Time `json:"last,omitzero"`
	// Models is each model the project's sessions used, by first seen; their tokens add up to Tokens.
	Models []history.ModelLine `json:"models"`
}

// ProjectUsage is one project's use in the status's two windows. The project "" holds what has no
// project: sessions whose folder is not found, and requests that came without a session id.
type ProjectUsage struct {
	Project  string `json:"project"`
	FiveHour Use    `json:"five_hour"`
	Week     Use    `json:"week"`
	// What the project used of an Ultra account's limit window, in percent of one account's window,
	// summed over its sessions; nil when some of it cannot be priced. Elsewhere is its tokens on
	// accounts with no meter, which the percent leaves out.
	PlanShare5h *float64 `json:"planShare5h"`
	PlanShare7d *float64 `json:"planShare7d"`
	Elsewhere5h int64    `json:"elsewhere5h"`
	Elsewhere7d int64    `json:"elsewhere7d"`
}

// meteredPlan is the plan whose accounts have a meter to learn from: an Ultra account reports one
// limit window of its own, with no 5-hour limit before it.
const meteredPlan = "max_20x"

// meteredAccounts are the configured accounts on the metered plan.
func (e *Engine) meteredAccounts() map[string]bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		if acc := e.st.Accounts[a.AuthFile]; acc != nil && acc.Plan.Name == meteredPlan {
			out[a.AuthFile] = true
		}
	}
	return out
}

// ReadSessions reads the history's sessions with their use priced against the Ultra accounts'
// meters. It reads the disk, so callers hold no lock. Without a recorder there is nothing to read.
func (e *Engine) ReadSessions(now time.Time) history.Sessions {
	if e.rec == nil {
		return history.Sessions{}
	}
	return e.rec.SessionsFor(now, e.meteredAccounts())
}

// sharesByProject sums the roots' shares per project folder.
func (e *Engine) sharesByProject(roots []history.RootUse) map[string]history.Share {
	out := map[string]history.Share{}
	for _, root := range roots {
		folder, _ := e.projects.Folder(root.ID)
		sh := out[folder]
		sh.Points += root.Share.Points
		sh.Unknown = sh.Unknown || root.Share.Unknown
		sh.Elsewhere += root.Share.Elsewhere
		out[folder] = sh
	}
	return out
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
		u.Models = history.MergeLines(u.Models, root.Lines)
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
// files, so this runs outside the engine's lock. Sessions read with ReadSessions also carry what
// they used of the Ultra accounts' limit windows: here that is added to each Ultra account (its
// meter) and to each project; the rows have it already.
func (e *Engine) AddSessions(v *View, s history.Sessions) {
	v.Sessions = &s
	v.Meter = &MeterView{Label: probe.DisplayName(meteredPlan), Basis: history.BasisText, FableModels: e.cfg.Buckets.Fable}
	for i := range v.Accounts {
		if v.Accounts[i].Plan != meteredPlan {
			continue
		}
		c := s.Calibrations[v.Accounts[i].AuthFile] // an account with no history yet has no figure
		v.Accounts[i].Meter = &AccountMeter{Known: c.Known, PerMillion: c.PerMillion, FromPrevious: c.FromPrevious, WindowHours: c.Length.Hours(), Outside: c.Outside}
	}
	if v.Affinity == nil {
		return
	}
	five, week := e.byProject(s.FiveHour.AllRoots), e.byProject(s.Week.AllRoots)
	projects := map[string]*ProjectUsage{}
	get := func(name string) *ProjectUsage {
		if projects[name] == nil {
			projects[name] = &ProjectUsage{Project: name}
			if len(s.Calibrations) > 0 { // a project with no use in a window used none of it
				projects[name].PlanShare5h, projects[name].PlanShare7d = new(float64), new(float64)
			}
		}
		return projects[name]
	}
	for name, u := range five {
		get(name).FiveHour = u
	}
	for name, u := range week {
		get(name).Week = u
	}
	if len(s.Calibrations) > 0 {
		for name, sh := range e.sharesByProject(s.FiveHour.AllRoots) {
			p := get(name)
			p.PlanShare5h, p.Elsewhere5h = sh.Percent(), sh.Elsewhere
		}
		for name, sh := range e.sharesByProject(s.Week.AllRoots) {
			p := get(name)
			p.PlanShare7d, p.Elsewhere7d = sh.Percent(), sh.Elsewhere
		}
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
