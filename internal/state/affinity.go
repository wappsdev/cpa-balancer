package state

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/affinity"
	"github.com/wappsdev/cpa-balancer/internal/config"
)

// MaxHomes bounds the projects the state remembers. The least recently used one is dropped to
// make room; its project is placed again by headroom the next time it is used.
const MaxHomes = 200

const (
	PinFolder  = "folder"
	PinSession = "session"
)

// Home is the account a project stays on until that account is full.
type Home struct {
	Account string    `json:"account"`
	Since   time.Time `json:"since"`
	Last    time.Time `json:"last"`           // when a request last used it
	Fill    *Fill     `json:"fill,omitempty"` // set when fill chose the account
}

// Fill is what fill saw of an account when it put a project there: when its week resets and the
// room it had under its cap, in percent.
type Fill struct {
	ResetsAt time.Time `json:"resets_at"`
	Room     float64   `json:"room"`
}

// Affinity is what the balancer remembers about projects: their homes, and the pins set on the page.
type Affinity struct {
	Homes       map[string]*Home  `json:"homes,omitempty"`
	FolderPins  map[string]string `json:"folder_pins,omitempty"`
	SessionPins map[string]string `json:"session_pins,omitempty"`
	// Links are the worktree folders found so far and their main repositories, the least recently used
	// first, so that a worktree removed from the disk still joins its repository (see affinity.Repos).
	Links []affinity.Link `json:"links,omitempty"`
}

// cleanLinks drops the links with an invalid name and the repeats (the later one stays), and keeps
// the most recent affinity.MaxLinks.
func cleanLinks(links []affinity.Link) []affinity.Link {
	seen := map[string]bool{}
	var out []affinity.Link
	for i := len(links) - 1; i >= 0; i-- {
		l := links[i]
		if seen[l.Folder] || !affinity.ValidProject(l.Folder) || !affinity.ValidProject(l.Project) || l.Folder == l.Project {
			continue
		}
		seen[l.Folder] = true
		out = append(out, l)
	}
	if len(out) > affinity.MaxLinks {
		out = out[:affinity.MaxLinks]
	}
	slices.Reverse(out)
	return out
}

func (a *Affinity) pins(kind string) (pins *map[string]string, limit int, valid func(string) bool, ok bool) {
	switch kind {
	case PinFolder:
		return &a.FolderPins, config.MaxFolderPins, affinity.ValidProject, true
	case PinSession:
		return &a.SessionPins, config.MaxSessionPins, affinity.ValidSID, true
	}
	return nil, 0, nil, false
}

// SetHome makes account the project's home. The project's Since only moves with its account.
func (s *State) SetHome(project, account string, now time.Time) {
	a := &s.Affinity
	if a.Homes == nil {
		a.Homes = map[string]*Home{}
	}
	if h, ok := a.Homes[project]; ok && h.Account == account {
		h.Last = now
		return
	}
	a.Homes[project] = &Home{Account: account, Since: now, Last: now}
	if len(a.Homes) > MaxHomes {
		keys := make([]string, 0, len(a.Homes))
		for k := range a.Homes {
			keys = append(keys, k)
		}
		oldest := slices.MinFunc(keys, func(x, y string) int {
			if c := a.Homes[x].Last.Compare(a.Homes[y].Last); c != 0 {
				return c
			}
			return compareStrings(x, y)
		})
		delete(a.Homes, oldest)
	}
}

// PlaceHome places a project on account by fill: the home starts over, on the same account too, and
// keeps what fill saw.
func (s *State) PlaceHome(project, account string, fill *Fill, now time.Time) {
	delete(s.Affinity.Homes, project)
	s.SetHome(project, account, now)
	s.Affinity.Homes[project].Fill = fill
}

// TouchHome notes that a request used the project's home.
func (s *State) TouchHome(project string, now time.Time) {
	if h, ok := s.Affinity.Homes[project]; ok && now.After(h.Last) {
		h.Last = now
	}
}

// SetPin pins a folder or a main session to an account. A new pin past the limit is refused, never
// made by dropping another one: a pin is something the owner set.
func (s *State) SetPin(kind, key, account string) error {
	pins, limit, valid, ok := s.Affinity.pins(kind)
	switch {
	case !ok:
		return fmt.Errorf("a pin is for a folder or a session")
	case !valid(key):
		return fmt.Errorf("the %s name is not valid", kind)
	case account == "":
		return fmt.Errorf("a pin needs an account")
	}
	if _, exists := (*pins)[key]; !exists && len(*pins) >= limit {
		return fmt.Errorf("at most %d %s pins; remove one first", limit, kind)
	}
	if *pins == nil {
		*pins = map[string]string{}
	}
	(*pins)[key] = account
	return nil
}

// ClearPin removes a pin; one that is not there is not an error.
func (s *State) ClearPin(kind, key string) {
	if pins, _, _, ok := s.Affinity.pins(kind); ok {
		delete(*pins, key)
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// normalize drops what the bounds forbid, for a state file that was edited or written by another
// version: invalid names, empty accounts, and anything past the limits (the most recently used
// homes and the first pins by name stay).
func (a *Affinity) normalize() {
	a.Links = cleanLinks(a.Links)
	for project, h := range a.Homes {
		if h == nil || h.Account == "" || !affinity.ValidProject(project) {
			delete(a.Homes, project)
		}
	}
	if len(a.Homes) > MaxHomes {
		keys := make([]string, 0, len(a.Homes))
		for k := range a.Homes {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if c := a.Homes[keys[i]].Last.Compare(a.Homes[keys[j]].Last); c != 0 {
				return c > 0
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys[MaxHomes:] {
			delete(a.Homes, k)
		}
	}
	for _, kind := range []string{PinFolder, PinSession} {
		pins, limit, valid, _ := a.pins(kind)
		keys := make([]string, 0, len(*pins))
		for k, account := range *pins {
			if account == "" || !valid(k) {
				delete(*pins, k)
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > limit {
			for _, k := range keys[limit:] {
				delete(*pins, k)
			}
		}
	}
}
