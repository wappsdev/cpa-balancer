package engine

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// Why a session is on its account. The page and /status show these.
const (
	ReasonProject            = "project_home"         // the project's home account
	ReasonFolderPin          = "folder_pin"           // the folder is pinned to this account
	ReasonSessionPin         = "session_pin"          // the session is pinned to this account
	ReasonFallback           = "fallback"             // the home could not take this request (full, held back or not offered)
	ReasonFolderPinFallback  = "folder_pin_fallback"  // the folder's pinned account is full or held back
	ReasonSessionPinFallback = "session_pin_fallback" // the session's pinned account is full or held back
	ReasonNoProject          = "no_project"           // no project found: CPA's own order
)

// Where a pin was set.
const (
	SourceUI     = "ui"
	SourceConfig = "config"
)

// MaxRememberedSessions bounds the sessions whose last pick is kept; the oldest is dropped first.
const MaxRememberedSessions = 500

// SessionPick is where a session's last request went and why. It is kept in memory only.
type SessionPick struct {
	Account string    `json:"account"` // "" when CPA's own order left the choice open
	Reason  string    `json:"reason"`
	Project string    `json:"project,omitempty"` // the folder; user data, escaped on the page
	At      time.Time `json:"at"`
}

type HomeView struct {
	Project string    `json:"project"`
	Account string    `json:"account"`
	Since   time.Time `json:"since"`
	Last    time.Time `json:"last"`
	Full    bool      `json:"full"` // the account is at its weekly cap: the project moves on its next request
}

type PinView struct {
	Key     string `json:"key"` // a folder name or a main session id
	Account string `json:"account"`
	Source  string `json:"source"` // ui or config; a config pin is changed in the config
	Full    bool   `json:"full"`   // the account is at its weekly cap, so the pin does not hold now
}

// AffinityView is the project homes, the pins and where each recent session went.
type AffinityView struct {
	Homes       []HomeView             `json:"homes"`
	FolderPins  []PinView              `json:"folder_pins"`
	SessionPins []PinView              `json:"session_pins"`
	Sessions    map[string]SessionPick `json:"sessions"` // by the session key the history uses
	// Projects and SessionProjects are added with the history's sessions (see AddSessions): what each
	// project used, and the folder of each listed root session, found by name.
	Projects        []ProjectUsage    `json:"projects"`
	SessionProjects map[string]string `json:"session_projects"`
	// Names is the short name the page shows for each folder (see FolderNames). The folder stays the
	// key of pins and homes; a folder with no entry is shown as it is.
	Names map[string]string `json:"names"`
	// ProjectOf is the project each listed folder belongs to: the folder itself, or, for a git
	// worktree, the main repository's folder. Homes and folder pins are keyed by the project (a pin on
	// a worktree's own folder holds for it alone); sessions are listed with their own folder.
	ProjectOf map[string]string `json:"project_of"`
	// ProjectInferred marks the folders in ProjectOf that are gone and were never found: they were put
	// with a repository only because their worktree container holds that repository's worktrees only.
	ProjectInferred map[string]bool `json:"project_inferred"`
}

// standing is an account's standing for a request in a bucket. An account that is not configured
// can never take work again, so it counts as full.
func (e *Engine) standing(account string, b quota.Bucket, now time.Time) policy.Standing {
	for _, a := range e.cfg.Accounts {
		if a.AuthFile == account {
			s, listed := e.snaps[account]
			return policy.Stand(a, s, listed, b, now, e.cfg.Thresholds)
		}
	}
	return policy.Full
}

func (e *Engine) headroom(account string, b quota.Bucket, now time.Time) float64 {
	for _, a := range e.cfg.Accounts {
		if a.AuthFile == account {
			h, _ := policy.Headroom(a, e.snaps[account], b, now, e.cfg.Thresholds)
			return h
		}
	}
	return 0
}

// orderOf is the existing decision's order for a bucket; it breaks ties between equal headroom.
func (e *Engine) orderOf(b quota.Bucket) []string {
	if b == quota.Fable {
		return e.decision.FableOrder
	}
	return e.decision.MainOrder
}

// ranker is an account's place in the existing order of a bucket; an account outside it comes last.
func (e *Engine) ranker(b quota.Bucket) func(string) int {
	order := e.orderOf(b)
	return func(a string) int {
		if i := slices.Index(order, a); i >= 0 {
			return i
		}
		return len(order)
	}
}

// best is the account among names with the most headroom in a bucket; a tie goes to the existing
// order, then to the config's.
func (e *Engine) best(names []string, b quota.Bucket, now time.Time) string {
	rank := e.ranker(b)
	best := ""
	for _, a := range names {
		switch {
		case best == "":
			best = a
		case e.headroom(a, b, now) > e.headroom(best, b, now):
			best = a
		case e.headroom(a, b, now) == e.headroom(best, b, now) && rank(a) < rank(best):
			best = a
		}
	}
	return best
}

// existing is the answer without affinity: the existing decision's first candidate.
func (e *Engine) existing(b quota.Bucket, candidates map[string]bool) pluginapi.SchedulerPickResponse {
	for _, name := range e.orderOf(b) {
		if candidates[name] {
			return pluginapi.SchedulerPickResponse{AuthID: name, Handled: true}
		}
	}
	return delegate
}

// pinFor is the account a session is pinned to, if any: a session's pin outranks its folder's, and
// a pin in the config outranks one set on the page for the same key (the page refuses to set one).
// A worktree's folder is pinned on its own, or else with its project (the main repository's folder).
func (e *Engine) pinFor(root, folder, project string) (kind, account string) {
	a := &e.st.Affinity
	if acct := pinned(e.cfg.Affinity.SessionPins, a.SessionPins, root); acct != "" {
		return state.PinSession, acct
	}
	for _, key := range []string{folder, project} {
		if key == "" {
			continue
		}
		if acct := pinned(e.cfg.Affinity.FolderPins, a.FolderPins, key); acct != "" {
			return state.PinFolder, acct
		}
	}
	return "", ""
}

// pinned is the account a key is pinned to: the config's pin, else the one set on the page.
func pinned(config, ui map[string]string, key string) string {
	if a := config[key]; a != "" {
		return a
	}
	return ui[key]
}

// homeOf is the project's account, placing the project first when it has none, or moving it when its
// account is full for the week. Callers hold e.mu. It moves a project only on a weekly cap: a
// 5-hour cap, a cooldown or missing data is a detour for the request, not a reason to move. With
// no account to move to, the home is left as it is and "" is returned when there is none.
func (e *Engine) homeOf(project string, now time.Time) string {
	h := e.st.Affinity.Homes[project]
	if h != nil && e.standing(h.Account, quota.Main, now) != policy.Full {
		e.st.TouchHome(project, now)
		return h.Account
	}
	var usable []string
	for _, a := range e.cfg.Accounts {
		if e.standing(a.AuthFile, quota.Main, now) == policy.Usable {
			usable = append(usable, a.AuthFile)
		}
	}
	best := e.balanced(project, usable, now)
	switch {
	case best != "":
		e.st.SetHome(project, best, now)
		e.dirty = true
		if h != nil {
			e.host.Log("info", fmt.Sprintf("cpa-balancer: a project moved from %q to %q: its account is full", h.Account, best))
		}
		return best
	case h != nil:
		return h.Account
	}
	return ""
}

// place answers a request of a known session: the pin, else the project's home, else a detour,
// else CPA's own order. folder is the session's own project folder; project is the one its home
// and pins belong to (the folder, or a worktree's main repository). Callers hold e.mu.
func (e *Engine) place(b quota.Bucket, id, root, folder, project string, candidates map[string]bool, now time.Time) pluginapi.SchedulerPickResponse {
	usable := func(account string) bool {
		return candidates[account] && e.standing(account, b, now) == policy.Usable
	}
	use := func(account, reason string) pluginapi.SchedulerPickResponse {
		e.remember(id, account, reason, folder, now)
		return pluginapi.SchedulerPickResponse{AuthID: account, Handled: true}
	}
	pinKind, pinned := e.pinFor(root, folder, project)
	if pinned != "" && usable(pinned) {
		return use(pinned, pinKind+"_pin")
	}
	fallback := ReasonFallback
	if pinned != "" {
		fallback = pinKind + "_pin_fallback"
	}
	if folder == "" {
		resp := e.existing(b, candidates)
		if pinned == "" {
			fallback = ReasonNoProject
		}
		e.remember(id, resp.AuthID, fallback, "", now)
		return resp
	}
	if home := e.homeOf(project, now); home != "" && usable(home) {
		if pinned != "" {
			return use(home, fallback)
		}
		return use(home, ReasonProject)
	}
	var others []string
	for _, a := range e.cfg.Accounts {
		if usable(a.AuthFile) {
			others = append(others, a.AuthFile)
		}
	}
	if account := e.best(others, b, now); account != "" {
		return use(account, fallback)
	}
	resp := e.existing(b, candidates)
	e.remember(id, resp.AuthID, fallback, folder, now)
	return resp
}

// remember keeps a session's last pick; at the limit the oldest goes. Callers hold e.mu.
func (e *Engine) remember(id, account, reason, project string, now time.Time) {
	if e.picks == nil {
		e.picks = map[string]SessionPick{}
	}
	if _, ok := e.picks[id]; !ok && len(e.picks) >= MaxRememberedSessions {
		oldest, first := "", true
		for k, p := range e.picks {
			if first || p.At.Before(e.picks[oldest].At) {
				oldest, first = k, false
			}
		}
		delete(e.picks, oldest)
	}
	e.picks[id] = SessionPick{Account: account, Reason: reason, Project: project, At: now}
}

// SetPin pins a folder or a main session to an account from the page, and saves at once: a rebuild
// of the engine must not lose it. A key pinned in the config is changed there.
func (e *Engine) SetPin(kind, key, account string) error {
	if !e.cfg.Managed(account) {
		return fmt.Errorf("the account is not one of the configured accounts")
	}
	return e.editPin(kind, key, func(s *state.State) error { return s.SetPin(kind, key, account) })
}

// ClearPin removes a pin set on the page.
func (e *Engine) ClearPin(kind, key string) error {
	return e.editPin(kind, key, func(s *state.State) error { s.ClearPin(kind, key); return nil })
}

func (e *Engine) editPin(kind, key string, edit func(*state.State) error) error {
	inConfig := e.cfg.Affinity.FolderPins
	if kind == state.PinSession {
		inConfig = e.cfg.Affinity.SessionPins
	}
	if _, ok := inConfig[key]; ok {
		return fmt.Errorf("this pin is set in the config; change it there")
	}
	e.mu.Lock()
	err := edit(e.st)
	if err == nil {
		e.dirty = true
	}
	e.mu.Unlock()
	if err == nil {
		e.save()
	}
	return err
}

// affinityView is the status's part about projects; callers hold e.mu.
func (e *Engine) affinityView(now time.Time) *AffinityView {
	v := &AffinityView{Homes: []HomeView{}, FolderPins: []PinView{}, SessionPins: []PinView{}, Sessions: make(map[string]SessionPick, len(e.picks))}
	for project, h := range e.st.Affinity.Homes {
		if e.isWorktree(project) {
			continue // a home stored before worktrees joined their repository: no longer a project
		}
		v.Homes = append(v.Homes, HomeView{Project: project, Account: h.Account, Since: h.Since, Last: h.Last,
			Full: e.standing(h.Account, quota.Main, now) == policy.Full})
	}
	sort.Slice(v.Homes, func(i, j int) bool {
		if c := v.Homes[i].Last.Compare(v.Homes[j].Last); c != 0 {
			return c > 0
		}
		return v.Homes[i].Project < v.Homes[j].Project
	})
	pins := func(config, ui map[string]string) []PinView {
		out := []PinView{}
		for key, account := range config {
			out = append(out, PinView{Key: key, Account: account, Source: SourceConfig, Full: e.standing(account, quota.Main, now) == policy.Full})
		}
		for key, account := range ui {
			if _, ok := config[key]; !ok {
				out = append(out, PinView{Key: key, Account: account, Source: SourceUI, Full: e.standing(account, quota.Main, now) == policy.Full})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out
	}
	v.FolderPins = pins(e.cfg.Affinity.FolderPins, e.st.Affinity.FolderPins)
	v.SessionPins = pins(e.cfg.Affinity.SessionPins, e.st.Affinity.SessionPins)
	for id, p := range e.picks {
		v.Sessions[id] = p
	}
	v.nameFolders(userHome())
	return v
}
