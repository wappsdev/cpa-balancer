package history

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	maxIDLength        = 128 // a session id comes from a client header, so only a plain id of this size is stored as given; room for a uuid, a slash and an agent id
	maxModelLength     = 64
	maxDepth           = 8       // levels of subagents listed; a deeper session is folded into its ancestor at the last level
	maxSessionsPerSlot = 200     // distinct sessions kept in one slot; later ones are folded into OtherSession
	maxKeysPerMap      = 8       // distinct models (or accounts) in one map of a slot; later ones are folded into OtherSession
	MaxLine            = 4 << 20 // the longest history line that is read or written
	// OtherSession stands for requests whose session id or model name is not a plain one, or that
	// came after a slot already held its maximum of sessions or models. Its name is not valid, so
	// no client can send it. Together the caps keep a slot far below MaxLine.
	OtherSession    = "(other)"
	FiveHourWindow  = 5 * time.Hour
	WeekWindow      = 7 * 24 * time.Hour
	weekTopSessions = 30 // root sessions listed for the week; the rest is summed in Other
)

// SessionSlot is one session's traffic in one slot. Accounts and Models split the same requests
// two ways.
type SessionSlot struct {
	Parent   string             `json:"parent,omitempty"`
	First    time.Time          `json:"first"`
	Last     time.Time          `json:"last"`
	Accounts map[string]Traffic `json:"accounts,omitempty"`
	Models   map[string]Traffic `json:"models,omitempty"`
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
	x.Add(t)
	(*m)[key] = x
}

// validModel is validID's rule with the brackets and @ that model names carry, such as
// "name[1m]" or "name@20251001". An empty name is allowed: it means the request named none.
func validModel(model string) bool {
	if model == "" {
		return true
	}
	return len(model) <= maxModelLength && strings.IndexFunc(model, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-[]@", c))
	}) < 0
}

// CleanModel is the model name as it is stored: a plain one as given, any other as OtherSession.
func CleanModel(model string) string {
	if validModel(model) {
		return model
	}
	return OtherSession
}

// CleanID is a client-supplied id as it is stored: a plain one as given, any other as OtherSession.
// An empty id stays empty.
func CleanID(id string) string {
	if id != "" && !validID(id) {
		return OtherSession
	}
	return id
}

// PlainID reports whether id is a key the history would store as given: a plain id, neither empty
// nor the name OtherSession stands under.
func PlainID(id string) bool { return id != OtherSession && validID(id) }

// validID reports whether a client-supplied id is a plain one: letters, digits and . _ : - / only.
func validID(id string) bool {
	if len(id) == 0 || len(id) > maxIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-' || c == '/') {
			return false
		}
	}
	return true
}

// CanonicalSession turns the ids CPA reports for Claude Code into the keys the history uses, so
// live traffic and transcripts land on the same row. CPA names a main session `claude:<sid>` and a
// subagent `claude:<sid>:agent:<agentId>`, with the parent in the same forms (or `claude:<other>`).
// The main session becomes `<sid>`, the transcript's own file name; a subagent becomes
// `<sid>/<agentId>` and, when no parent came with it, takes `<sid>` as its parent. An id in any
// other form is returned as it came.
func CanonicalSession(id, parent string) (string, string) {
	one := func(raw string) (key, owner string) {
		raw = strings.TrimSpace(raw)
		rest, ok := strings.CutPrefix(raw, "claude:")
		if !ok {
			return raw, ""
		}
		if sid, agent, ok := strings.Cut(rest, ":agent:"); ok && sid != "" && agent != "" {
			return sid + "/" + agent, sid
		}
		return rest, ""
	}
	id, owner := one(id)
	parent, _ = one(parent)
	if parent == "" {
		parent = owner
	}
	return id, parent
}

// AddTraffic counts requests in the slot (see Recorder.AddTraffic); the caller owns the slot.
func (s *Slot) AddTraffic(now time.Time, src Source, t Traffic) {
	model := CleanModel(src.Model)
	addCapped(&s.account(src.Account).Models, model, t)
	ss := s.session(now, src.Session, src.Parent)
	addCapped(&ss.Accounts, src.Account, t)
	addCapped(&ss.Models, model, t)
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
// (and theirs in turn). Accounts and Models describe the whole family, as Total does.
type SessionUsage struct {
	ID        string             `json:"id"` // empty: requests that came without a session
	Parent    string             `json:"parent,omitempty"`
	First     time.Time          `json:"first,omitzero"`
	Last      time.Time          `json:"last,omitzero"`
	Own       Traffic            `json:"own"`
	Total     Traffic            `json:"total"`
	Accounts  map[string]Traffic `json:"accounts"`
	Models    map[string]Traffic `json:"models"`
	Subagents []SessionUsage     `json:"subagents,omitempty"`
	// The family's use of the metered accounts' limit windows, in percent of one account's window,
	// for the last 5 hours and the last 7 days; nil when it cannot be priced. Elsewhere is its
	// tokens on accounts with no meter, which the percent leaves out. Set by SessionsFor.
	PlanShare5h *float64 `json:"planShare5h"`
	PlanShare7d *float64 `json:"planShare7d"`
	Elsewhere5h int64    `json:"elsewhere5h"`
	Elsewhere7d int64    `json:"elsewhere7d"`
	share       Share    // in this window alone
	// ModelLines is each model the family used, by first seen (see ModelLine). Their tokens add up
	// to Total.
	ModelLines []ModelLine `json:"model_lines"`
}

func (u SessionUsage) weight() int64 { return u.Total.Tokens() }

// Remainder is what the window's other root sessions used, when only the top ones are listed.
type Remainder struct {
	Sessions int     `json:"sessions"`
	Total    Traffic `json:"total"`
}

// SessionWindow is the use of one window [From, To). Total and Accounts come from the accounts'
// own counts. The listed roots' Total plus Other.Total always equal Total.
type SessionWindow struct {
	From     time.Time          `json:"from"`
	To       time.Time          `json:"to"`
	Total    Traffic            `json:"total"`
	Accounts map[string]Traffic `json:"accounts"`
	Sessions []SessionUsage     `json:"sessions"` // root sessions, the largest first
	Other    Remainder          `json:"other"`
	// AllRoots is every root session of the window, listed or not, with its family's total. It
	// stays on the server: the project totals and the balancer's load are summed from it, so a cut
	// list does not make them short.
	AllRoots []RootUse `json:"-"`
	// Shares is the share of every session of the window, listed or not, subagents too: a session
	// listed in the 5 hours but cut from the week's list still has a share for the week.
	Shares map[string]Share `json:"-"`
}

// RootUse is a root session's family in a window, without its detail.
type RootUse struct {
	ID    string
	Last  time.Time
	Total Traffic
	Share Share
	Lines []ModelLine
}

// Sessions is what the status page shows: the last five hours and the last seven days.
type Sessions struct {
	FiveHour SessionWindow `json:"five_hour"`
	Week     SessionWindow `json:"week"`
	// SkippedLines counts history lines that could not be read: broken or past the size limit.
	SkippedLines int `json:"skipped_lines,omitempty"`
	// Calibrations is what each metered account's meter says now, by account. It stays on the
	// server: the status page shows it with the account.
	Calibrations map[string]Calibration `json:"-"`
}

// Sessions reads the history once for both windows, which end with the slot now is in. The
// 5-hour window is not an account's quota window: those differ per account, so this is a rolling
// span. It prices nothing: see SessionsFor.
func (r *Recorder) Sessions(now time.Time) Sessions { return r.sessionsOf(now, nil, weekTopSessions) }

// SessionsFor is Sessions with each session's use priced against the meters of the accounts in
// metered (see meter.go). It reads back as far as the window before the current one.
func (r *Recorder) SessionsFor(now time.Time, metered map[string]bool) Sessions {
	return r.sessionsOf(now, metered, weekTopSessions)
}

func (r *Recorder) sessionsOf(now time.Time, metered map[string]bool, weekLimit int) Sessions {
	to := slotStart(now).Add(SlotLength)
	from := to.Add(-WeekWindow)
	if len(metered) > 0 {
		from = to.Add(-MeterLookback)
	}
	all, skipped := r.readSlots(from, to)
	var ms *Meters
	if len(metered) > 0 {
		ms = buildMeters(all, metered)
	}
	var slots, recent []*Slot
	for _, s := range all {
		if !s.T.Before(to.Add(-WeekWindow)) {
			slots = append(slots, s)
		}
		if !s.T.Before(to.Add(-FiveHourWindow)) {
			recent = append(recent, s)
		}
	}
	out := Sessions{
		FiveHour:     buildWindow(recent, to.Add(-FiveHourWindow), to, 0, ms), // never cut: the current session must be found in it
		Week:         buildWindow(slots, to.Add(-WeekWindow), to, weekLimit, ms),
		SkippedLines: skipped,
	}
	if ms != nil {
		out.Calibrations = ms.calibrations()
		fill(out.FiveHour.Sessions, &out)
		fill(out.Week.Sessions, &out)
	}
	return out
}

// fill sets the shares of the rows, both windows' figures on every row. A session that is in only
// one window has no figure (nil) for the other.
func fill(rows []SessionUsage, s *Sessions) {
	for i := range rows {
		if five, ok := s.FiveHour.Shares[rows[i].ID]; ok {
			rows[i].PlanShare5h, rows[i].Elsewhere5h = five.Percent(), five.Elsewhere
		}
		if week, ok := s.Week.Shares[rows[i].ID]; ok {
			rows[i].PlanShare7d, rows[i].Elsewhere7d = week.Percent(), week.Elsewhere
		}
		fill(rows[i].Subagents, s)
	}
}

type node struct {
	id, parent  string
	first, last time.Time
	own         Traffic
	accounts    map[string]Traffic
	models      map[string]Traffic // as the history stored the names
	folded      map[string]Traffic // by folded model name, see FoldModel
	seen        map[string][2]time.Time
	children    []*node
	share       Share
}

// addModels counts a session's models in the node, as stored and under their folded names, and
// when each folded one was seen.
func (n *node) addModels(models map[string]Traffic, first, last time.Time) {
	mergeInto(&n.models, models)
	for model, t := range models {
		key := FoldModel(model)
		if n.folded == nil {
			n.folded = map[string]Traffic{}
		}
		x := n.folded[key]
		x.Add(t)
		n.folded[key] = x
		n.see(key, first, last)
	}
}

func (n *node) see(model string, first, last time.Time) {
	if n.seen == nil {
		n.seen = map[string][2]time.Time{}
	}
	s := n.seen[model]
	if !first.IsZero() && (s[0].IsZero() || first.Before(s[0])) {
		s[0] = first
	}
	if last.After(s[1]) {
		s[1] = last
	}
	n.seen[model] = s
}

func mergeInto(dst *map[string]Traffic, src map[string]Traffic) {
	for k, t := range src {
		if *dst == nil {
			*dst = map[string]Traffic{}
		}
		x := (*dst)[k]
		x.Add(t)
		(*dst)[k] = x
	}
}

func sum(m map[string]Traffic) (t Traffic) {
	for _, x := range m {
		t.Add(x)
	}
	return t
}

func buildWindow(slots []*Slot, from, to time.Time, limit int, ms *Meters) SessionWindow {
	w := SessionWindow{From: from, To: to, Accounts: map[string]Traffic{}, Sessions: []SessionUsage{}, Shares: map[string]Share{}}
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
			w.Total.Add(t)
			if t != (Traffic{}) {
				x := w.Accounts[name]
				x.Add(t)
				w.Accounts[name] = x
			}
		}
		for id, ss := range sessions {
			n := get(id)
			if ss.Parent != "" && ss.Parent != id {
				n.parent = ss.Parent
			}
			if !ss.First.IsZero() && (n.first.IsZero() || ss.First.Before(n.first)) {
				n.first = ss.First
			}
			if ss.Last.After(n.last) {
				n.last = ss.Last
			}
			n.own.Add(sum(ss.Accounts))
			n.share.add(ms.shareOf(s.T, ss.Accounts))
			mergeInto(&n.accounts, ss.Accounts)
			n.addModels(ss.Models, ss.First, ss.Last)
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
		rows[i] = n.usage(1)
	}
	sortRows(rows)
	var index func([]SessionUsage)
	index = func(rows []SessionUsage) {
		for _, row := range rows {
			w.Shares[row.ID] = row.share
			index(row.Subagents)
		}
	}
	index(rows)
	listed := 0
	for _, row := range rows {
		w.AllRoots = append(w.AllRoots, RootUse{ID: row.ID, Last: row.Last, Total: row.Total, Share: row.share, Lines: row.ModelLines})
		if row.ID != "" && limit > 0 && listed >= limit {
			w.Other.Sessions++
			w.Other.Total.Add(row.Total)
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

// absorb adds another node's own use to this one, as if the requests had come from it.
func (n *node) absorb(c *node) {
	if !c.first.IsZero() && (n.first.IsZero() || c.first.Before(n.first)) {
		n.first = c.first
	}
	if c.last.After(n.last) {
		n.last = c.last
	}
	n.own.Add(c.own)
	n.share.add(c.share)
	mergeInto(&n.accounts, c.accounts)
	mergeInto(&n.models, c.models)
	mergeInto(&n.folded, c.folded)
	for model, s := range c.seen {
		n.see(model, s[0], s[1])
	}
}

// foldDescendants absorbs every session below n into it and leaves it without children. Cycles
// are already cut, so the walk ends.
func (n *node) foldDescendants() {
	stack := n.children
	n.children = nil
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = append(stack[:len(stack)-1], c.children...)
		n.absorb(c)
	}
}

// usage turns a node and its descendants into a row; depth is the node's level, 1 for a root.
// At maxDepth the node takes in everything below it, so the tree and the recursion stay bounded.
func (n *node) usage(depth int) SessionUsage {
	if depth >= maxDepth {
		n.foldDescendants()
	}
	u := SessionUsage{ID: n.id, Parent: n.parent, First: n.first, Last: n.last, Own: n.own, Total: n.own,
		Accounts: map[string]Traffic{}, Models: map[string]Traffic{}, share: n.share}
	mergeInto(&u.Accounts, n.accounts)
	mergeInto(&u.Models, n.models)
	own := make([]ModelLine, 0, len(n.folded))
	for model, t := range n.folded {
		own = append(own, ModelLine{Model: model, First: n.seen[model][0], Last: n.seen[model][1], Own: t})
	}
	lines := [][]ModelLine{own}
	for _, c := range n.children {
		cu := c.usage(depth + 1)
		u.Subagents = append(u.Subagents, cu)
		u.Total.Add(cu.Total)
		u.share.add(cu.share)
		mergeInto(&u.Accounts, cu.Accounts)
		mergeInto(&u.Models, cu.Models)
		sub := make([]ModelLine, len(cu.ModelLines)) // its own use and its subagents' alike are subagent use here
		for i, l := range cu.ModelLines {
			l.Subagents.Add(l.Own)
			l.Own = Traffic{}
			sub[i] = l
		}
		lines = append(lines, sub)
		if !cu.First.IsZero() && (u.First.IsZero() || cu.First.Before(u.First)) {
			u.First = cu.First
		}
		if cu.Last.After(u.Last) {
			u.Last = cu.Last
		}
	}
	sortRows(u.Subagents)
	u.ModelLines = MergeLines(lines...)
	return u
}
