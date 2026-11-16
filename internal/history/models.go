package history

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// FoldModel names the model behind the name a request carried. A context size in brackets
// ("name[1m]") and a date ("name-20251001", "name@20251001") are the same model: they are one
// line on the page, and use of them is not a switch of model. A name that is not plain, and an
// empty one, stay as they are. The history keeps the names as they came; this folds them when
// they are read.
func FoldModel(name string) string {
	if i := strings.IndexByte(name, '['); i > 0 {
		name = name[:i]
	}
	if n := len(name); n > 9 && (name[n-9] == '-' || name[n-9] == '@') && strings.Trim(name[n-8:], "0123456789") == "" {
		name = name[:n-9]
	}
	return name
}

// ModelLine is one model's use in a session's family: when it was first and last seen (to the
// slot), and its tokens, those of the session's own requests apart from those of its subagents.
// A session whose subagents run another model has a line for each, and has not switched.
type ModelLine struct {
	Model     string    `json:"model"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
	Own       Traffic   `json:"own"`
	Subagents Traffic   `json:"subagents"`
}

// MergeLines adds sets of lines together by model; the result is ordered by first seen.
func MergeLines(sets ...[]ModelLine) []ModelLine {
	by := map[string]*ModelLine{}
	for _, set := range sets {
		for _, l := range set {
			m := by[l.Model]
			if m == nil {
				c := l
				by[l.Model] = &c
				continue
			}
			m.Own.Add(l.Own)
			m.Subagents.Add(l.Subagents)
			if !l.First.IsZero() && (m.First.IsZero() || l.First.Before(m.First)) {
				m.First = l.First
			}
			if l.Last.After(m.Last) {
				m.Last = l.Last
			}
		}
	}
	out := make([]ModelLine, 0, len(by))
	for _, m := range by {
		out = append(out, *m)
	}
	slices.SortFunc(out, func(a, b ModelLine) int {
		return cmp.Or(a.First.Compare(b.First), cmp.Compare(a.Model, b.Model))
	})
	return out
}
