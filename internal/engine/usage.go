package engine

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/affinity"
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
// project: sessions whose folder is not found, and requests that came without a session id. A
// project is a repository: its figures include those of its git worktrees, which are listed apart.
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
	// Accounts is what the project used of each account, in both windows: its tokens on the meter's
	// basis and its percent of that account's own limit window (nil: not priced). It holds the
	// worktrees' use too.
	Accounts map[string]history.AccountUse `json:"accounts,omitempty"`
	// Worktrees are the project's git worktrees with use in a window, each with its own figures (the
	// project's include them), by name: the folder in Project, the worktree's directory name and its path.
	Worktrees []ProjectUsage `json:"worktrees,omitempty"`
	Name      string         `json:"name,omitempty"`
	Path      string         `json:"path,omitempty"`
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

// configuredAccounts are all the configured accounts: each has a meter of its own to price by.
func (e *Engine) configuredAccounts() map[string]bool {
	out := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		out[a.AuthFile] = true
	}
	return out
}

// ReadSessions reads the history's sessions with their use priced against every account's own
// meter; the percent of the Ultra accounts alone adds up in the rows' PlanShare figures. It reads the disk, so callers hold no lock. Without a recorder there is nothing to read.
func (e *Engine) ReadSessions(now time.Time) history.Sessions {
	if e.rec == nil {
		return history.Sessions{}
	}
	return e.rec.SessionsFor(now, e.meteredAccounts(), e.configuredAccounts())
}

// sharesByFolder sums the roots' shares per project folder.
func (e *Engine) sharesByFolder(roots []history.RootUse) map[string]history.Share {
	out := map[string]history.Share{}
	for _, root := range roots {
		folder, _ := e.projects.Folder(root.ID)
		sh := out[folder]
		sh.Add(root.Share)
		out[folder] = sh
	}
	return out
}

// byFolder sums root sessions per project folder. It looks the folders up by name, so callers hold
// no lock.
func (e *Engine) byFolder(roots []history.RootUse) map[string]Use {
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

// byProject sums root sessions per project: a worktree's sessions count with its main repository.
// Callers hold no lock.
func (e *Engine) byProject(roots []history.RootUse) map[string]Use {
	return e.foldUse(e.byFolder(roots))
}

// foldUse adds each folder's use to its project's.
func (e *Engine) foldUse(folders map[string]Use) map[string]Use {
	out := map[string]Use{}
	for folder, u := range folders {
		project := e.repos.Of(folder).Project
		sum := out[project]
		sum.Requests += u.Requests
		sum.Tokens += u.Tokens
		sum.Models = history.MergeLines(sum.Models, u.Models)
		if u.Last.After(sum.Last) {
			sum.Last = u.Last
		}
		out[project] = sum
	}
	return out
}

// foldShares adds each folder's share to its project's. A share is added as points and flags, never
// as a percent, so that one part that cannot be priced leaves the whole unknown.
func (e *Engine) foldShares(folders map[string]history.Share) map[string]history.Share {
	out := map[string]history.Share{}
	for folder, s := range folders {
		project := e.repos.Of(folder).Project
		sum := out[project]
		sum.Add(s)
		out[project] = sum
	}
	return out
}

// usageRows is the use of each key (a project, or a folder) in both windows, with its Ultra shares
// when the accounts are calibrated: a key with no use in a window used none of it.
func usageRows(five, week map[string]Use, fiveShares, weekShares map[string]history.Share, priced bool) map[string]*ProjectUsage {
	rows := map[string]*ProjectUsage{}
	get := func(name string) *ProjectUsage {
		if rows[name] == nil {
			rows[name] = &ProjectUsage{Project: name}
			if priced {
				rows[name].PlanShare5h, rows[name].PlanShare7d = new(float64), new(float64)
			}
		}
		return rows[name]
	}
	for name, u := range five {
		get(name).FiveHour = u
	}
	for name, u := range week {
		get(name).Week = u
	}
	for name, sh := range fiveShares {
		p := get(name)
		p.PlanShare5h, p.Elsewhere5h = sh.Percent(), sh.Elsewhere
	}
	for name, sh := range weekShares {
		p := get(name)
		p.PlanShare7d, p.Elsewhere7d = sh.Percent(), sh.Elsewhere
	}
	return rows
}

// setAccounts adds to each row what it used of each account, from the shares of both windows.
func setAccounts(rows map[string]*ProjectUsage, five, week map[string]history.Share) {
	for name, row := range rows {
		if uses := history.AccountUses(five[name], week[name]); len(uses) > 0 {
			row.Accounts = uses
		}
	}
}

// maxAccountProjects is how many projects an account's card lists.
const maxAccountProjects = 5

// AccountProject is what one project used of an account in the 7 days: tokens on the meter's basis
// and the percent of that account's own limit window (nil: not priced). The project "" is what has
// no project.
type AccountProject struct {
	Project  string   `json:"project"`
	Tokens7d int64    `json:"tokens7d"`
	Share7d  *float64 `json:"share7d"`
}

// byProject lists, for each account, the projects that used it in the 7 days, the largest first, cut
// at maxAccountProjects; more is how many were cut. It is summed from every project, so the cut
// list of sessions does not make it short.
func byProject(projects map[string]*ProjectUsage) (top map[string][]AccountProject, more map[string]int) {
	all := map[string][]AccountProject{}
	for name, p := range projects {
		for account, use := range p.Accounts {
			if use.Tokens7d > 0 {
				all[account] = append(all[account], AccountProject{Project: name, Tokens7d: use.Tokens7d, Share7d: use.Share7d})
			}
		}
	}
	top, more = map[string][]AccountProject{}, map[string]int{}
	for account, list := range all {
		sort.Slice(list, func(i, j int) bool {
			return list[i].Tokens7d > list[j].Tokens7d || (list[i].Tokens7d == list[j].Tokens7d && list[i].Project < list[j].Project)
		})
		if len(list) > maxAccountProjects {
			more[account] = len(list) - maxAccountProjects
			list = list[:maxAccountProjects]
		}
		top[account] = list
	}
	return top, more
}

// AddSessions puts the history's sessions into the status, with what the card groups them by: each
// project's use in both windows, summed from every root session (the week's list is cut, the sums
// are not), and the folder of each listed root session. A project is a repository: its use is that
// of its own folder and of its git worktrees, which are listed under it with their own figures. The
// sessions are read from the history files, so this runs outside the engine's lock. Sessions read
// with ReadSessions also carry what they used of the Ultra accounts' limit windows: here that is
// added to each Ultra account (its meter) and to each project; the rows have it already.
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
	fiveUse, weekUse := e.byFolder(s.FiveHour.AllRoots), e.byFolder(s.Week.AllRoots)
	fiveShares, weekShares := e.sharesByFolder(s.FiveHour.AllRoots), e.sharesByFolder(s.Week.AllRoots)
	foldedFive, foldedWeek := e.foldShares(fiveShares), e.foldShares(weekShares)
	// the Ultra percent is priced only when an Ultra account has a calibration; the split by account always is
	priced := len(s.Calibrations) > 0
	ultraFive, ultraWeek, ultraFoldedFive, ultraFoldedWeek := fiveShares, weekShares, foldedFive, foldedWeek
	if !priced {
		ultraFive, ultraWeek, ultraFoldedFive, ultraFoldedWeek = nil, nil, nil, nil
	}
	own := usageRows(fiveUse, weekUse, ultraFive, ultraWeek, priced)
	setAccounts(own, fiveShares, weekShares)
	projects := usageRows(e.foldUse(fiveUse), e.foldUse(weekUse), ultraFoldedFive, ultraFoldedWeek, priced)
	setAccounts(projects, foldedFive, foldedWeek)
	for folder, row := range own {
		if place := e.repos.Of(folder); place.Worktree && projects[place.Project] != nil {
			wt := *row
			wt.Name, wt.Path = affinity.WorktreeName(folder), place.Path // a directory that is gone: the name is cut from the key
			if place.Path != "" {
				wt.Name = filepath.Base(place.Path)
			}
			projects[place.Project].Worktrees = append(projects[place.Project].Worktrees, wt)
		}
	}
	top, more := byProject(projects)
	for i := range v.Accounts {
		v.Accounts[i].ByProject, v.Accounts[i].MoreProjects = top[v.Accounts[i].AuthFile], more[v.Accounts[i].AuthFile]
	}
	v.Affinity.Projects = make([]ProjectUsage, 0, len(projects))
	for _, p := range projects {
		sort.Slice(p.Worktrees, func(i, j int) bool {
			a, b := p.Worktrees[i], p.Worktrees[j]
			return a.Name < b.Name || (a.Name == b.Name && a.Project < b.Project)
		})
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
	v.Affinity.ProjectOf, v.Affinity.ProjectInferred = map[string]string{}, map[string]bool{}
	for _, folder := range v.Affinity.folders() {
		if folder != "" {
			place := e.repos.Of(folder)
			v.Affinity.ProjectOf[folder] = place.Project
			if place.Inferred {
				v.Affinity.ProjectInferred[folder] = true
			}
		}
	}
	v.Affinity.nameFolders(userHome())
}
