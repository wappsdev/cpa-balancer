package history

import (
	"cmp"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	maxIDLength        = 64  // a session id comes from a client header, so only a plain id of this size is stored as given
	maxSessionsPerSlot = 200 // distinct sessions kept in one slot; later ones are folded into OtherSession
	maxKeysPerMap      = 8   // distinct models (or accounts) in one map of a slot; later ones are folded into OtherSession
	maxLine            = 4 << 20
	// OtherSession stands for requests whose session id or model name is not a plain one, or that
	// came after a slot already held its maximum of sessions or models. Its name is not valid, so
	// no client can send it. Together the caps keep a slot far below maxLine.
	OtherSession    = "(other)"
	FiveHourWindow  = 5 * time.Hour
	WeekWindow      = 7 * 24 * time.Hour
	weekTopSessions = 30 // root sessions listed for the week; the rest is summed in Other
)

// SessionSlot is one session's traffic in one slot. Accounts and Models split the same requests
// two ways. Advisor is separate: advisor requests are not part of what an account reports.
type SessionSlot struct {
	Parent   string             `json:"parent,omitempty"`
	First    time.Time          `json:"first"`
	Last     time.Time          `json:"last"`
	Accounts map[string]Traffic `json:"accounts,omitempty"`
	Models   map[string]Traffic `json:"models,omitempty"`
	Advisor  map[string]Traffic `json:"advisor,omitempty"` // by advisor model
}

// addCapped adds t under key; a key the map does not hold yet goes under OtherSession once the map
// has maxKeysPerMap keys. The sum of the map does not change.
func addCapped(m *map[string]Traffic, key string, t Traffic) {
	if *m == nil {
		*m = map[string]Traffic{}
	}
	if _, ok := (*m)[key]; !ok && key != OtherSession && len(*m) >= maxKeysPerMap {
		key = OtherSession
	}
	x := (*m)[key]
	x.add(t)
	(*m)[key] = x
}

// validModel is validID's rule with the brackets and @ that model names carry, such as
// "name[1m]" or "name@20251001". An empty name is allowed: it means the request named none.
func validModel(model string) bool {
	if model == "" {
		return true
	}
	return len(model) <= maxIDLength && strings.IndexFunc(model, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-[]@", c))
	}) < 0
}

func cleanModel(model string) string {
	if validModel(model) {
		return model
	}
	return OtherSession
}

// validID reports whether a client-supplied id is a plain one: letters, digits and . _ : - only.
func validID(id string) bool {
	if len(id) == 0 || len(id) > maxIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// NewSlot is an empty slot that starts at the slot boundary t is in.
func NewSlot(t time.Time) *Slot { return newSlot(slotStart(t)) }

// AddTraffic counts requests in the slot (see Recorder.AddTraffic); the caller owns the slot.
func (s *Slot) AddTraffic(now time.Time, src Source, t Traffic) {
	model := cleanModel(src.Model)
	addCapped(&s.account(src.Account).Models, model, t)
	ss := s.session(now, src.Session, src.Parent)
	addCapped(&ss.Accounts, src.Account, t)
	addCapped(&ss.Models, model, t)
}

// AddAdvisor counts advisor requests in the slot (see Recorder.AddAdvisor).
func (s *Slot) AddAdvisor(now time.Time, session, parent, model string, t Traffic) {
	ss := s.session(now, session, parent)
	addCapped(&ss.Advisor, cleanModel(model), t)
}

// session returns the slot's record of a session. An id that is not a plain one, or that would be
// one session too many for the slot, becomes OtherSession.
func (s *Slot) session(now time.Time, id, parent string) *SessionSlot {
	id, parent = strings.TrimSpace(id), strings.TrimSpace(parent)
	if id != "" && !validID(id) {
		id, parent = OtherSession, ""
	}
	if parent != "" && !validID(parent) || parent == id {
		parent = ""
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*SessionSlot{}
	}
	ss, ok := s.Sessions[id]
	if !ok && id != OtherSession && len(s.Sessions) >= maxSessionsPerSlot {
		id, parent = OtherSession, ""
		ss, ok = s.Sessions[id]
	}
	if !ok {
		ss = &SessionSlot{First: now}
		s.Sessions[id] = ss
	}
	if now.Before(ss.First) {
		ss.First = now
	}
	if now.After(ss.Last) {
		ss.Last = now
	}
	if parent != "" {
		ss.Parent = parent
	}
	return ss
}

// Tokens is what a request spends of a plan: input, output and cache writes. Cache reads are left
// out, as they cost a small part of a token each.
func (t Traffic) Tokens() int64 { return t.InputTokens + t.OutputTokens + t.CacheCreationTokens }

// SessionUsage is a session in a window. Own is its requests alone; Total adds its subagents'
// (and theirs in turn). Accounts, Models, Advisor and AdvisorModels describe the whole family, as
// Total does. An advisor's requests are not in Own or Total.
type SessionUsage struct {
	ID            string             `json:"id"` // empty: requests that came without a session
	Parent        string             `json:"parent,omitempty"`
	First         time.Time          `json:"first,omitzero"`
	Last          time.Time          `json:"last,omitzero"`
	Estimated     bool               `json:"estimated,omitempty"` // part of it was rebuilt from transcripts, with its account guessed
	Own           Traffic            `json:"own"`
	Total         Traffic            `json:"total"`
	Accounts      map[string]Traffic `json:"accounts"`
	Models        map[string]Traffic `json:"models"`
	Advisor       Traffic            `json:"advisor"`
	AdvisorModels map[string]Traffic `json:"advisor_models,omitempty"`
	Subagents     []SessionUsage     `json:"subagents,omitempty"`
}

func (u SessionUsage) weight() int64 { return u.Total.Tokens() + u.Advisor.Tokens() }

// Remainder is what the window's other root sessions used, when only the top ones are listed.
type Remainder struct {
	Sessions int     `json:"sessions"`
	Total    Traffic `json:"total"`
	Advisor  Traffic `json:"advisor"`
}

// SessionWindow is the use of one window [From, To). Total and Accounts come from the accounts'
// own counts. The listed roots' Total plus Other.Total always equal Total.
type SessionWindow struct {
	From     time.Time          `json:"from"`
	To       time.Time          `json:"to"`
	Total    Traffic            `json:"total"`
	Advisor  Traffic            `json:"advisor"`
	Accounts map[string]Traffic `json:"accounts"`
	Sessions []SessionUsage     `json:"sessions"` // root sessions, the largest first
	Other    Remainder          `json:"other"`
}

// Sessions is what the status page shows: the last five hours and the last seven days.
type Sessions struct {
	FiveHour SessionWindow `json:"five_hour"`
	Week     SessionWindow `json:"week"`
	// SkippedLines counts history lines that could not be read: broken or past the size limit.
	SkippedLines int `json:"skipped_lines,omitempty"`
}

// Sessions reads the history once for both windows, which end with the slot now is in. The
// 5-hour window is not an account's quota window: those differ per account, so this is a rolling
// span.
func (r *Recorder) Sessions(now time.Time) Sessions {
	to := slotStart(now).Add(SlotLength)
	slots, skipped := r.readSlots(to.Add(-WeekWindow), to)
	var recent []*Slot
	for _, s := range slots {
		if !s.T.Before(to.Add(-FiveHourWindow)) {
			recent = append(recent, s)
		}
	}
	return Sessions{
		FiveHour:     buildWindow(recent, to.Add(-FiveHourWindow), to, 0), // never cut: the current session must be found in it
		Week:         buildWindow(slots, to.Add(-WeekWindow), to, weekTopSessions),
		SkippedLines: skipped,
	}
}

// SessionWindow is the use of [from, to); limit > 0 lists only that many root sessions, besides
// the one without an id, and sums the rest in Other.
func (r *Recorder) SessionWindow(from, to time.Time, limit int) SessionWindow {
	return buildWindow(r.slots(from, to), from, to, limit)
}

type node struct {
	id, parent  string
	first, last time.Time
	estimated   bool
	own         Traffic
	accounts    map[string]Traffic
	models      map[string]Traffic
	advisor     map[string]Traffic
	children    []*node
}

func mergeInto(dst *map[string]Traffic, src map[string]Traffic) {
	for k, t := range src {
		if *dst == nil {
			*dst = map[string]Traffic{}
		}
		x := (*dst)[k]
		x.add(t)
		(*dst)[k] = x
	}
}

func sum(m map[string]Traffic) (t Traffic) {
	for _, x := range m {
		t.add(x)
	}
	return t
}

func buildWindow(slots []*Slot, from, to time.Time, limit int) SessionWindow {
	w := SessionWindow{From: from, To: to, Accounts: map[string]Traffic{}, Sessions: []SessionUsage{}}
	nodes := map[string]*node{}
	get := func(id string) *node {
		n, ok := nodes[id]
		if !ok {
			n = &node{id: id}
			nodes[id] = n
		}
		return n
	}
	for _, s := range slots {
		sessions := s.Sessions
		if len(sessions) == 0 {
			// Written before sessions were recorded: all of it is traffic without a session.
			legacy := &SessionSlot{First: s.T, Last: s.T, Accounts: map[string]Traffic{}, Models: map[string]Traffic{}}
			for name, a := range s.Accounts {
				if t := sum(a.Models); t != (Traffic{}) {
					legacy.Accounts[name] = t
					mergeInto(&legacy.Models, a.Models)
				}
			}
			if len(legacy.Accounts) > 0 {
				sessions = map[string]*SessionSlot{"": legacy}
			}
		}
		for name, a := range s.Accounts {
			t := sum(a.Models)
			w.Total.add(t)
			if t != (Traffic{}) {
				x := w.Accounts[name]
				x.add(t)
				w.Accounts[name] = x
			}
		}
		for id, ss := range sessions {
			n := get(id)
			if ss.Parent != "" && ss.Parent != id {
				n.parent = ss.Parent
			}
			n.estimated = n.estimated || s.Backfill
			if !ss.First.IsZero() && (n.first.IsZero() || ss.First.Before(n.first)) {
				n.first = ss.First
			}
			if ss.Last.After(n.last) {
				n.last = ss.Last
			}
			n.own.add(sum(ss.Accounts))
			mergeInto(&n.accounts, ss.Accounts)
			mergeInto(&n.models, ss.Models)
			mergeInto(&n.advisor, ss.Advisor)
		}
	}
	for _, n := range slices.Collect(maps.Values(nodes)) {
		if n.parent != "" {
			get(n.parent) // a parent with no traffic of its own in the window still heads its family
		}
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids { // break a cycle at the first of its members in id order
		n := nodes[id]
		p := n.parent
		for steps := 0; p != "" && p != id && steps <= len(nodes); steps++ {
			p = nodes[p].parent
		}
		if p == id {
			n.parent = ""
		}
	}
	var roots []*node
	for _, id := range ids {
		if n := nodes[id]; n.parent == "" {
			roots = append(roots, n)
		} else {
			nodes[n.parent].children = append(nodes[n.parent].children, n)
		}
	}
	rows := make([]SessionUsage, len(roots))
	for i, n := range roots {
		rows[i] = n.usage()
		w.Advisor.add(rows[i].Advisor)
	}
	sortRows(rows)
	listed := 0
	for _, row := range rows {
		if row.ID != "" && limit > 0 && listed >= limit {
			w.Other.Sessions++
			w.Other.Total.add(row.Total)
			w.Other.Advisor.add(row.Advisor)
			continue
		}
		if row.ID != "" {
			listed++
		}
		w.Sessions = append(w.Sessions, row)
	}
	return w
}

func sortRows(rows []SessionUsage) {
	slices.SortFunc(rows, func(a, b SessionUsage) int {
		return cmp.Or(cmp.Compare(b.weight(), a.weight()), b.Last.Compare(a.Last), cmp.Compare(a.ID, b.ID))
	})
}

// usage turns a node and its descendants into a row.
func (n *node) usage() SessionUsage {
	u := SessionUsage{ID: n.id, Parent: n.parent, First: n.first, Last: n.last, Estimated: n.estimated, Own: n.own, Total: n.own,
		Accounts: map[string]Traffic{}, Models: map[string]Traffic{}, Advisor: sum(n.advisor)}
	mergeInto(&u.Accounts, n.accounts)
	mergeInto(&u.Models, n.models)
	mergeInto(&u.AdvisorModels, n.advisor)
	for _, c := range n.children {
		cu := c.usage()
		u.Subagents = append(u.Subagents, cu)
		u.Total.add(cu.Total)
		u.Advisor.add(cu.Advisor)
		u.Estimated = u.Estimated || cu.Estimated
		mergeInto(&u.Accounts, cu.Accounts)
		mergeInto(&u.Models, cu.Models)
		mergeInto(&u.AdvisorModels, cu.AdvisorModels)
		if !cu.First.IsZero() && (u.First.IsZero() || cu.First.Before(u.First)) {
			u.First = cu.First
		}
		if cu.Last.After(u.Last) {
			u.Last = cu.Last
		}
	}
	sortRows(u.Subagents)
	return u
}

func hasTraffic(s *Slot) bool {
	if len(s.Sessions) > 0 {
		return true
	}
	for _, a := range s.Accounts {
		if sum(a.Models) != (Traffic{}) {
			return true
		}
	}
	return false
}

// WriteBackfill appends slots rebuilt from transcripts. A slot time that already holds recorded
// traffic or sessions, live or from an earlier backfill, is left alone, so running it again adds
// nothing. It returns how many slots it wrote. A written slot sits beside any levels-only slot of
// the same time: readers add the traffic of slots with one time and take the highest level.
func (r *Recorder) WriteBackfill(slots []*Slot) (int, error) {
	if r == nil || len(slots) == 0 {
		return 0, nil
	}
	from, to := slots[0].T, slots[0].T
	for _, s := range slots {
		if s.T.Before(from) {
			from = s.T
		}
		if s.T.After(to) {
			to = s.T
		}
	}
	taken := map[time.Time]bool{}
	for _, s := range r.slots(from, to.Add(SlotLength)) {
		if hasTraffic(s) {
			taken[s.T] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return 0, err
	}
	written := 0
	for _, s := range slots {
		if taken[s.T] || !hasTraffic(s) {
			continue
		}
		s.Backfill = true
		if err := r.appendSlot(s); err != nil {
			return written, err
		}
		taken[s.T] = true
		written++
	}
	return written, nil
}
