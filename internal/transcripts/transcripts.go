// Package transcripts is the store of what Claude Code's own transcripts say a session used: exact
// token counts per session, hour and model. It is kept apart from the balancer's history on
// purpose. A transcript names no account and sees no proxy quota, so nothing here may be added to
// an account's figures or to the live sessions.
//
// The store is a directory of one file per UTC day, `<YYYY-MM-DD>.jsonl`, one JSON row per line,
// and `titles.jsonl`, one line per session that has a name. The importer writes it whole on every
// run (see Write), the plugin reads it (see Query).
package transcripts

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/lines"
)

const (
	// Dir is the store's directory name under the balancer's data directory.
	Dir = "transcripts"
	// TopSessions is how many root sessions a Summary lists; the rest is summed in Other.
	TopSessions = 30
	// TopSubagents is how many subagents of one session a Summary lists; the rest is summed in
	// Session.OtherSubagents. A workflow can run hundreds.
	TopSubagents = 10
	dateLayout   = "2006-01-02"
	// TitlesFile holds the session names, one line per session.
	TitlesFile = "titles.jsonl"
	// MaxTitle is the longest session name kept, in characters.
	MaxTitle = 120
)

// The kinds of a session's name, from the most to the least deliberate: one the user set with
// /rename, an agent's name, and the one Claude Code makes up on its own.
const (
	KindCustom = "custom"
	KindAgent  = "agent"
	KindAI     = "ai"
)

// Title is a session's name as its transcript gives it.
type Title struct {
	Text string `json:"title"`
	Kind string `json:"kind"`
}

type titleLine struct {
	Session string `json:"session"`
	Title
}

// CleanTitle is a name as it is stored and shown: without control and format characters (a
// bidirectional override can turn a name into another), trimmed, at most MaxTitle characters. It is
// user text, so the page escapes it as it does every value.
func CleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxTitle {
		s = strings.TrimSpace(string(r[:MaxTitle]))
	}
	return s
}

// maxRows is how many store rows one Query reads. A row is one session, hour and model, so a
// month of heavy use stays far below it; a store past it is cut, newest days first, and says so.
var maxRows = 200_000

// Key says what a row counts. Session is the id the live history uses (history.CanonicalSession):
// a bare session id for the session's own transcript, `<session>/<agent>` for a subagent's. A row
// counts each message once, in the one transcript that owns it, so the rows add up without a
// double count and a parent's figure is the sum of its own row and its children's.
type Key struct {
	Hour    time.Time // the start of the UTC hour
	Session string
	Model   string
	Advisor bool // the model answered as an advisor inside another model's message
}

type row struct {
	Hour    time.Time `json:"hour"`
	Session string    `json:"session"`
	Model   string    `json:"model"`
	Advisor bool      `json:"advisor,omitempty"`
	history.Traffic
}

// Write makes the directory hold exactly these rows, one file per UTC day. Every file is written
// to a temporary name and renamed, so a reader never sees a half file, and the rows are sorted, so
// the same input leaves the same bytes. A day file the input has no rows for is removed: the store
// is a pure function of the transcripts, which is what makes a rerun safe. Files that are not day
// files are left alone.
//
// titles are the sessions' names, by session id. Only a session the rows hold (itself or through a
// subagent) keeps one, and a name that is empty once cleaned is no name.
func Write(dir string, rows map[Key]history.Traffic, titles map[string]Title) error {
	byDay := map[string][]row{}
	for k, t := range rows {
		day := k.Hour.UTC().Format(dateLayout)
		byDay[day] = append(byDay[day], row{Hour: k.Hour.UTC(), Session: k.Session, Model: k.Model, Advisor: k.Advisor, Traffic: t})
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for day, rs := range byDay {
		slices.SortFunc(rs, func(a, b row) int {
			return cmp.Or(a.Hour.Compare(b.Hour), cmp.Compare(a.Session, b.Session), cmp.Compare(a.Model, b.Model), compareBool(a.Advisor, b.Advisor))
		})
		var out []byte
		for _, r := range rs {
			line, err := json.Marshal(r)
			if err != nil {
				return err
			}
			out = append(append(out, line...), '\n')
		}
		if err := replace(filepath.Join(dir, day+".jsonl"), out); err != nil {
			return err
		}
	}
	if err := writeTitles(dir, rows, titles); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if _, perr := time.Parse(dateLayout, name); ok && perr == nil && byDay[name] == nil && e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeTitles(dir string, rows map[Key]history.Traffic, titles map[string]Title) error {
	present := map[string]bool{}
	for k := range rows {
		root, _, _ := strings.Cut(k.Session, "/")
		present[root] = true
	}
	var out []byte
	for _, id := range slices.Sorted(maps.Keys(titles)) {
		t := titles[id]
		t.Text = CleanTitle(t.Text)
		if !present[id] || t.Text == "" {
			continue
		}
		line, err := json.Marshal(titleLine{Session: id, Title: t})
		if err != nil {
			return err
		}
		out = append(append(out, line...), '\n')
	}
	path := filepath.Join(dir, TitlesFile)
	if out == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return replace(path, out)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}

// replace writes data to path through a temporary file in the same directory.
func replace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// Session is one session in a Summary. Total is its use with its subagents', without advisors;
// Models splits the same use by model. Advisor and AdvisorModels are advisor models' answers, on
// top of Total, as the transcript reports them apart from the message's own usage.
type Session struct {
	ID            string                     `json:"id"`
	Title         string                     `json:"title,omitempty"` // the session's name, a root session only
	TitleKind     string                     `json:"title_kind,omitempty"`
	First         time.Time                  `json:"first"`
	Last          time.Time                  `json:"last"`
	Total         history.Traffic            `json:"total"`
	Models        map[string]history.Traffic `json:"models,omitempty"`
	Advisor       history.Traffic            `json:"advisor"`
	AdvisorModels map[string]history.Traffic `json:"advisor_models,omitempty"`
	Subagents     []Session                  `json:"subagents,omitempty"`
	// OtherSubagents sums the subagents that are not listed; Total already holds their use.
	OtherSubagents *Remainder `json:"other_subagents,omitempty"`
}

func (s Session) weight() int64 { return s.Total.Tokens() + s.Advisor.Tokens() }

// Remainder is what the root sessions that are not listed used.
type Remainder struct {
	Sessions int             `json:"sessions"`
	Total    history.Traffic `json:"total"`
	Advisor  history.Traffic `json:"advisor"`
}

// Summary is the transcripts' use over the whole UTC hours of [From, To). It is not an account's
// quota and carries no account.
type Summary struct {
	From         time.Time       `json:"from"`
	To           time.Time       `json:"to"`
	Total        history.Traffic `json:"total"`
	Advisor      history.Traffic `json:"advisor"`
	Sessions     []Session       `json:"sessions"` // root sessions, the largest first
	Other        Remainder       `json:"other"`
	SkippedLines int             `json:"skipped_lines,omitempty"` // store lines that were broken or past the size limit
	Truncated    bool            `json:"truncated,omitempty"`     // the store held more rows than one query reads
}

type node struct {
	id          string
	first, last time.Time
	own         history.Traffic
	models      map[string]history.Traffic
	advisor     map[string]history.Traffic
	children    []*node
}

// Query sums the store over the last `days` days up to now, in whole UTC hours. A missing store is
// an empty answer, not an error: the importer may never have run. The work is bounded: only the
// day files of the range are opened, every line has the history's size limit, and no more than
// maxRows rows are read.
func Query(dir string, now time.Time, days int) Summary {
	now = now.UTC()
	from := now.Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Hour)
	s := Summary{From: from, To: now, Sessions: []Session{}}
	nodes := map[string]*node{}
	get := func(id string) *node {
		n, ok := nodes[id]
		if !ok {
			n = &node{id: id}
			nodes[id] = n
		}
		return n
	}
	read := 0
	// Newest day first, so a cut drops the oldest rows.
	for day := now.Truncate(24 * time.Hour); !day.Before(from.Truncate(24*time.Hour)) && !s.Truncated; day = day.Add(-24 * time.Hour) {
		f, err := os.Open(filepath.Join(dir, day.Format(dateLayout)+".jsonl"))
		if err != nil {
			continue
		}
		bad, err := lines.Each(f, history.MaxLine, func(raw []byte) {
			if read >= maxRows {
				s.Truncated = true
				return
			}
			var r row
			if json.Unmarshal(raw, &r) != nil || r.Hour.IsZero() {
				s.SkippedLines++
				return
			}
			read++
			if r.Hour.Before(from) || !r.Hour.Before(now) {
				return
			}
			n := get(history.CleanID(r.Session))
			if n.first.IsZero() || r.Hour.Before(n.first) {
				n.first = r.Hour
			}
			if r.Hour.After(n.last) {
				n.last = r.Hour
			}
			model := history.CleanModel(r.Model)
			if r.Advisor {
				merge(&n.advisor, map[string]history.Traffic{model: r.Traffic})
			} else {
				n.own.Add(r.Traffic)
				merge(&n.models, map[string]history.Traffic{model: r.Traffic})
			}
		})
		if err != nil {
			s.SkippedLines++
		}
		s.SkippedLines += bad
		f.Close()
	}

	// A subagent's id is `<session>/<agent>`: it joins the family of its session, which exists
	// even when the session's own transcript has no row.
	for _, id := range slices.Sorted(maps.Keys(nodes)) {
		if parent, _, ok := strings.Cut(id, "/"); ok {
			p := get(parent)
			p.children = append(p.children, nodes[id])
		}
	}
	var roots []Session
	for _, id := range slices.Sorted(maps.Keys(nodes)) {
		if !strings.Contains(id, "/") {
			roots = append(roots, nodes[id].session())
		}
	}
	slices.SortFunc(roots, func(a, b Session) int {
		return cmp.Or(cmp.Compare(b.weight(), a.weight()), b.Last.Compare(a.Last), cmp.Compare(a.ID, b.ID))
	})
	for i, r := range roots {
		s.Total.Add(r.Total)
		s.Advisor.Add(r.Advisor)
		if i < TopSessions {
			s.Sessions = append(s.Sessions, r)
		} else {
			s.Other.Sessions++
			s.Other.Total.Add(r.Total)
			s.Other.Advisor.Add(r.Advisor)
		}
	}
	if len(s.Sessions) > 0 {
		titles := readTitles(dir)
		for i := range s.Sessions {
			if t, ok := titles[s.Sessions[i].ID]; ok {
				s.Sessions[i].Title, s.Sessions[i].TitleKind = t.Text, t.Kind
			}
		}
	}
	return s
}

// readTitles reads the names file under the same bounds as the rows: a line past the history's
// size limit is skipped, no more than maxRows lines are read, and a name is cleaned again. The
// first line of a session wins.
func readTitles(dir string) map[string]Title {
	f, err := os.Open(filepath.Join(dir, TitlesFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]Title{}
	n := 0
	_, _ = lines.Each(f, history.MaxLine, func(raw []byte) {
		var l titleLine
		if n++; n > maxRows || json.Unmarshal(raw, &l) != nil {
			return
		}
		l.Text = CleanTitle(l.Text)
		if _, seen := out[l.Session]; !seen && l.Text != "" && (l.Kind == KindCustom || l.Kind == KindAgent || l.Kind == KindAI) {
			out[l.Session] = l.Title
		}
	})
	return out
}

// tally is a node's use with its children's, before the model maps are cut to their top keys.
type tally struct {
	first, last         time.Time
	total, advisor      history.Traffic
	models, advisorKeys map[string]history.Traffic
}

func merge(dst *map[string]history.Traffic, src map[string]history.Traffic) {
	if *dst == nil {
		*dst = map[string]history.Traffic{}
	}
	for k, t := range src {
		x := (*dst)[k]
		x.Add(t)
		(*dst)[k] = x
	}
}

func (t *tally) add(o tally) {
	if !o.first.IsZero() && (t.first.IsZero() || o.first.Before(t.first)) {
		t.first = o.first
	}
	if o.last.After(t.last) {
		t.last = o.last
	}
	t.total.Add(o.total)
	t.advisor.Add(o.advisor)
	merge(&t.models, o.models)
	merge(&t.advisorKeys, o.advisorKeys)
}

// rolledUp is a node's own use as a tally, with maps of its own for the tally to add into.
func (n *node) rolledUp() tally {
	t := tally{first: n.first, last: n.last, total: n.own}
	merge(&t.models, n.models)
	merge(&t.advisorKeys, n.advisor)
	for _, a := range n.advisor {
		t.advisor.Add(a)
	}
	return t
}

// session turns a node and its children into a row. A subagent has no children of its own: ids
// have one level, and a deeper one is not a form the importer writes.
func (n *node) session() Session {
	all := n.rolledUp()
	var subs []Session
	for _, c := range n.children {
		subs = append(subs, c.session())
		all.add(c.rolledUp())
	}
	slices.SortFunc(subs, func(a, b Session) int {
		return cmp.Or(cmp.Compare(b.weight(), a.weight()), cmp.Compare(a.ID, b.ID))
	})
	out := Session{ID: n.id, First: all.first, Last: all.last, Total: all.total, Models: topModels(all.models), Advisor: all.advisor,
		AdvisorModels: topModels(all.advisorKeys), Subagents: subs}
	if len(subs) > TopSubagents {
		out.Subagents, out.OtherSubagents = subs[:TopSubagents], &Remainder{}
		for _, c := range subs[TopSubagents:] {
			out.OtherSubagents.Sessions++
			out.OtherSubagents.Total.Add(c.Total)
			out.OtherSubagents.Advisor.Add(c.Advisor)
		}
	}
	return out
}

// maxModels is how many models of one session are listed by name; the rest are summed under
// history.OtherSession, the smallest first to go.
const maxModels = 8

// topModels keeps the largest models by name and sums the others, so a session's models add up to
// its total whatever the transcripts held.
func topModels(m map[string]history.Traffic) map[string]history.Traffic {
	if len(m) == 0 {
		return nil
	}
	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(m[b].Tokens(), m[a].Tokens()), cmp.Compare(a, b))
	})
	out := map[string]history.Traffic{}
	for i, k := range keys {
		if i >= maxModels || k == history.OtherSession {
			k2 := history.OtherSession
			x := out[k2]
			x.Add(m[k])
			out[k2] = x
			continue
		}
		out[k] = m[k]
	}
	return out
}
