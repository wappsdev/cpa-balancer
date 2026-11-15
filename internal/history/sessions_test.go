package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tok(requests, in, out int64) Traffic {
	return Traffic{Requests: requests, InputTokens: in, OutputTokens: out}
}

func src(account, model, session, parent string) Source {
	return Source{Account: account, Model: model, Session: session, Parent: parent}
}

// find returns the root row with the id, or nil.
func find(w SessionWindow, id string) *SessionUsage {
	for i := range w.Sessions {
		if w.Sessions[i].ID == id {
			return &w.Sessions[i]
		}
	}
	return nil
}

// walk calls f for every row of the forest.
func walk(rows []SessionUsage, f func(SessionUsage)) {
	for _, r := range rows {
		f(r)
		walk(r.Subagents, f)
	}
}

func window(r *Recorder, limit int) SessionWindow {
	return r.SessionWindow(t0.Add(-time.Hour), t0.Add(time.Hour), limit)
}

func TestSessionsAreAggregatedPerSessionAccountAndModel(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "opus", "s1", ""), tok(1, 100, 10))
	r.AddTraffic(t0.Add(time.Minute), src("b", "opus", "s1", ""), tok(1, 50, 5))
	r.AddTraffic(t0.Add(7*time.Minute), src("a", "haiku", "s1", ""), Traffic{Requests: 1, Failed: 1, InputTokens: 7, CacheReadTokens: 900, CacheCreationTokens: 30})
	r.AddTraffic(t0.Add(2*time.Minute), src("a", "opus", "s2", ""), tok(2, 1, 1))

	w := window(r, 0)
	s1 := find(w, "s1")
	if s1 == nil || len(w.Sessions) != 2 {
		t.Fatalf("sessions: %+v", w.Sessions)
	}
	want := Traffic{Requests: 3, Failed: 1, InputTokens: 157, OutputTokens: 15, CacheReadTokens: 900, CacheCreationTokens: 30}
	if s1.Own != want || s1.Total != want {
		t.Fatalf("totals %+v", s1.Own)
	}
	if s1.Accounts["a"].Requests != 2 || s1.Accounts["b"].InputTokens != 50 || s1.Models["opus"].Requests != 2 || s1.Models["haiku"].CacheReadTokens != 900 {
		t.Fatalf("splits %+v %+v", s1.Accounts, s1.Models)
	}
	if !s1.First.Equal(t0) || !s1.Last.Equal(t0.Add(7*time.Minute)) {
		t.Fatalf("seen %v .. %v", s1.First, s1.Last)
	}
}

func TestRequestsWithoutASessionGoToTheEmptyIdRow(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "m", "", ""), tok(4, 40, 4))
	r.AddTraffic(t0, src("a", "m", "s1", ""), tok(1, 1, 1))
	w := window(r, 0)
	none := find(w, "")
	if none == nil || none.Own.Requests != 4 || none.Parent != "" {
		t.Fatalf("empty-id row: %+v", w.Sessions)
	}
}

func TestSubagentsRollUpIntoTheirParentAndKeepTheirOwnTotals(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "opus", "p", ""), tok(1, 100, 10))
	r.AddTraffic(t0, src("a", "haiku", "c1", "p"), tok(2, 20, 2))
	r.AddTraffic(t0, src("b", "haiku", "c2", "p"), tok(3, 30, 3))
	r.AddTraffic(t0, src("a", "haiku", "g", "c1"), tok(1, 5, 5)) // a grandchild rolls up through c1
	r.AddTraffic(t0, src("a", "opus", "solo", ""), tok(1, 1, 1))

	w := window(r, 0)
	if len(w.Sessions) != 2 {
		t.Fatalf("only roots are listed: %+v", w.Sessions)
	}
	p := find(w, "p")
	if p.Own.Requests != 1 || p.Total.Requests != 7 || p.Total.InputTokens != 155 {
		t.Fatalf("parent own %+v total %+v", p.Own, p.Total)
	}
	if p.Accounts["b"].Requests != 3 || p.Accounts["a"].Requests != 4 || p.Models["haiku"].Requests != 6 {
		t.Fatalf("the splits describe the rolled-up total: %+v %+v", p.Accounts, p.Models)
	}
	if len(p.Subagents) != 2 {
		t.Fatalf("subagents %+v", p.Subagents)
	}
	var c1 SessionUsage
	for _, c := range p.Subagents {
		if c.ID == "c1" {
			c1 = c
		}
	}
	if c1.Parent != "p" || c1.Own.Requests != 2 || c1.Total.Requests != 3 || len(c1.Subagents) != 1 || c1.Subagents[0].ID != "g" {
		t.Fatalf("c1 %+v", c1)
	}
}

func TestAParentWithoutTrafficInTheWindowGetsAZeroRow(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "m", "child", "gone"), tok(2, 10, 1))
	w := window(r, 0)
	g := find(w, "gone")
	if g == nil || g.Own.Requests != 0 || g.Total.Requests != 2 || len(g.Subagents) != 1 || g.Subagents[0].ID != "child" {
		t.Fatalf("%+v", w.Sessions)
	}
}

func TestAParentThatIsItselfOrACycleDoesNotLoseTraffic(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "m", "self", "self"), tok(1, 1, 1))
	r.AddTraffic(t0, src("a", "m", "x", "y"), tok(1, 2, 2))
	r.AddTraffic(t0, src("a", "m", "y", "x"), tok(1, 4, 4))
	w := window(r, 0)
	var own, roots int64
	walk(w.Sessions, func(s SessionUsage) { own += s.Own.Requests })
	for _, s := range w.Sessions {
		roots += s.Total.Requests
	}
	if own != 3 || roots != 3 || w.Total.Requests != 3 {
		t.Fatalf("own %d roots %d total %+v rows %+v", own, roots, w.Total, w.Sessions)
	}
}

func TestSessionsSumToTheAccountTotalsOfTheWindow(t *testing.T) {
	r := NewRecorder(t.TempDir())
	n := int64(0)
	for i := range 20 {
		at := t0.Add(time.Duration(i) * 3 * time.Minute)
		session, parent := []string{"s1", "s2", "", "s3"}[i%4], ""
		if i%5 == 0 {
			session, parent = "sub", "s1"
		}
		r.AddTraffic(at, src([]string{"a", "b"}[i%2], []string{"opus", "haiku"}[i%3%2], session, parent),
			Traffic{Requests: 1, InputTokens: int64(i), OutputTokens: 1, CacheReadTokens: 3, CacheCreationTokens: 2})
		n++
	}
	w := window(r, 0)
	var own, roots, accounts Traffic
	walk(w.Sessions, func(s SessionUsage) { own.add(s.Own) })
	for _, s := range w.Sessions {
		roots.add(s.Total)
	}
	for _, a := range w.Accounts {
		accounts.add(a)
	}
	if w.Total.Requests != n || own != w.Total || roots != w.Total || accounts != w.Total {
		t.Fatalf("total %+v own %+v roots %+v accounts %+v", w.Total, own, roots, accounts)
	}
}

func TestOnlyTheWindowCounts(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0.Add(-3*time.Hour), src("a", "m", "old", ""), tok(1, 1, 1))
	r.AddTraffic(t0, src("a", "m", "now", ""), tok(1, 1, 1))
	w := window(r, 0)
	if find(w, "old") != nil || find(w, "now") == nil || w.Total.Requests != 1 {
		t.Fatalf("%+v", w)
	}
}

func TestTheTopSessionsAreKeptAndTheRestIsSummedUp(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "m", "big", ""), tok(1, 1000, 0))
	r.AddTraffic(t0, src("a", "m", "mid", ""), tok(1, 100, 0))
	r.AddTraffic(t0, src("a", "m", "small", ""), tok(1, 10, 0))
	r.AddTraffic(t0, src("a", "m", "tiny", "small"), tok(1, 1, 0))
	r.AddTraffic(t0, src("a", "m", "", ""), tok(1, 1, 0))
	w := window(r, 1)
	if len(w.Sessions) != 2 || find(w, "big") == nil || find(w, "") == nil {
		t.Fatalf("the top one and the empty-id row stay: %+v", w.Sessions)
	}
	if w.Other.Sessions != 2 || w.Other.Total.InputTokens != 111 || w.Other.Total.Requests != 3 {
		t.Fatalf("other %+v", w.Other)
	}
	var sum Traffic
	for _, s := range w.Sessions {
		sum.add(s.Total)
	}
	sum.add(w.Other.Total)
	if sum != w.Total {
		t.Fatalf("roots %+v total %+v", sum, w.Total)
	}
}

func TestAdvisorUseIsItsOwnColumnAndRollsUp(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddAdvisor(t0, "p", "", "fable", tok(1, 775, 6))
	r.AddAdvisor(t0, "c", "p", "opus", tok(2, 10, 1))
	r.AddTraffic(t0, src("a", "m", "p", ""), tok(1, 1, 1))
	w := window(r, 0)
	p := find(w, "p")
	if p.Advisor.Requests != 3 || p.Advisor.InputTokens != 785 || p.AdvisorModels["fable"].OutputTokens != 6 || p.AdvisorModels["opus"].Requests != 2 {
		t.Fatalf("advisor %+v %+v", p.Advisor, p.AdvisorModels)
	}
	if p.Total.Requests != 1 || w.Total.Requests != 1 || w.Advisor.Requests != 3 {
		t.Fatalf("advisor use stays out of the account totals: %+v %+v", p.Total, w.Total)
	}
}

func TestSlotsWrittenBeforeSessionsWereRecordedCountAsTheEmptyId(t *testing.T) {
	dir := t.TempDir()
	old := `{"t":"2026-10-04T11:55:00Z","accounts":{"a":{"models":{"opus":{"requests":5,"input_tokens":50,"output_tokens":5}}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-10-04.jsonl"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(dir)
	r.AddTraffic(t0, src("a", "opus", "s1", ""), tok(1, 1, 1))
	w := window(r, 0)
	none := find(w, "")
	if none == nil || none.Own.Requests != 5 || none.Accounts["a"].InputTokens != 50 || none.Models["opus"].Requests != 5 {
		t.Fatalf("old slot: %+v", w.Sessions)
	}
	if !none.First.Equal(time.Date(2026, 10, 4, 11, 55, 0, 0, time.UTC)) {
		t.Fatalf("seen %v", none.First)
	}
	var sum Traffic
	for _, s := range w.Sessions {
		sum.add(s.Total)
	}
	if sum != w.Total || w.Total.Requests != 6 {
		t.Fatalf("sum %+v total %+v", sum, w.Total)
	}
}

func TestSessionsSurviveTheDiskAndLongIdsAreBounded(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	long := strings.Repeat("x", 5000)
	r.AddTraffic(t0, src("a", "m", long, "p"), tok(1, 1, 1))
	r.AddTraffic(t0, src("a", "m", "s1", ""), tok(1, 1, 1))
	if err := r.Flush(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-10-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 2000 {
		t.Fatalf("an id must be cut before it reaches the disk: %d bytes", len(raw))
	}
	w := NewRecorder(dir).SessionWindow(t0.Add(-time.Hour), t0.Add(time.Hour), 0)
	if find(w, "s1") == nil || w.Total.Requests != 2 {
		t.Fatalf("%+v", w)
	}
}

func TestSessionsReportsBothWindowsFromOneRead(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0.Add(-6*time.Hour), src("a", "m", "older", ""), tok(1, 1, 1))
	r.AddTraffic(t0.Add(-time.Hour), src("a", "m", "recent", ""), tok(1, 1, 1))
	s := r.Sessions(t0)
	if find(s.FiveHour, "older") != nil || find(s.FiveHour, "recent") == nil || find(s.Week, "older") == nil {
		t.Fatalf("%+v", s)
	}
	if !s.FiveHour.To.After(t0) || !s.Week.To.Equal(s.FiveHour.To) || s.FiveHour.To.Sub(s.FiveHour.From) < 5*time.Hour {
		t.Fatalf("bounds %v %v", s.FiveHour.From, s.FiveHour.To)
	}
}

func TestABackfilledSlotMarksItsSessionsEstimated(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "m", "live", ""), tok(1, 1, 1))
	slot := &Slot{T: t0.Add(10 * time.Minute), Backfill: true, Sessions: map[string]*SessionSlot{
		"old": {First: t0.Add(10 * time.Minute), Last: t0.Add(11 * time.Minute), Accounts: map[string]Traffic{"unknown": tok(1, 1, 1)}, Models: map[string]Traffic{"m": tok(1, 1, 1)}}},
		Accounts: map[string]*AccountSlot{"unknown": {Models: map[string]Traffic{"m": tok(1, 1, 1)}}}}
	if _, err := r.WriteBackfill([]*Slot{slot}); err != nil {
		t.Fatal(err)
	}
	w := window(r, 0)
	if o := find(w, "old"); o == nil || !o.Estimated || find(w, "live").Estimated || w.Total.Requests != 2 {
		t.Fatalf("%+v", w.Sessions)
	}
}
