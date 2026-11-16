package history

import (
	"fmt"
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
	return r.sessionWindow(t0.Add(-time.Hour), t0.Add(time.Hour), limit)
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
	walk(w.Sessions, func(s SessionUsage) { own.Add(s.Own) })
	for _, s := range w.Sessions {
		roots.Add(s.Total)
	}
	for _, a := range w.Accounts {
		accounts.Add(a)
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
		sum.Add(s.Total)
	}
	sum.Add(w.Other.Total)
	if sum != w.Total {
		t.Fatalf("roots %+v total %+v", sum, w.Total)
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
		sum.Add(s.Total)
	}
	if sum != w.Total || w.Total.Requests != 6 {
		t.Fatalf("sum %+v total %+v", sum, w.Total)
	}
}

func TestSessionsSurviveTheDisk(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	r.AddTraffic(t0, src("a", "m", "s1", ""), tok(1, 1, 1))
	r.AddTraffic(t0, src("a", "m", "s2", "s1"), tok(1, 1, 1))
	if err := r.Flush(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	w := NewRecorder(dir).sessionWindow(t0.Add(-time.Hour), t0.Add(time.Hour), 0)
	if s1 := find(w, "s1"); s1 == nil || s1.Total.Requests != 2 || len(s1.Subagents) != 1 || w.Total.Requests != 2 {
		t.Fatalf("%+v", w)
	}
}

func TestAnIdOutsideTheAllowedFormatIsStoredAsOther(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	valid := strings.Repeat("a", 128)
	bad := []string{strings.Repeat("x", 129), "has space", "naïve", "tab\there", "<img>", "line\nbreak"}
	r.AddTraffic(t0, src("a", "m", valid, ""), tok(1, 1, 1))
	r.AddTraffic(t0, src("a", "m", "ok.Id_1:2-3", ""), tok(1, 1, 1))
	for _, id := range bad {
		r.AddTraffic(t0, src("a", "m", id, "p"), tok(1, 1, 1))
	}
	r.AddTraffic(t0, src("a", "m", "child", "bad parent"), tok(1, 1, 1))
	if err := r.Flush(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-10-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append(bad, "bad parent") {
		if strings.Contains(string(raw), id) {
			t.Fatalf("%q reached the disk", id)
		}
	}
	w := window(r, 0)
	other := find(w, OtherSession)
	if find(w, valid) == nil || find(w, "ok.Id_1:2-3") == nil || other == nil || other.Own.Requests != int64(len(bad)) || other.Parent != "" {
		t.Fatalf("%+v", w.Sessions)
	}
	if c := find(w, "child"); c == nil || c.Parent != "" {
		t.Fatalf("a parent in a bad format means no parent: %+v", c)
	}
	if w.Total.Requests != int64(len(bad)+3) {
		t.Fatalf("every request still counts: %+v", w.Total)
	}
}

func TestSessionsPerSlotAreCappedAndTheOverflowIsFolded(t *testing.T) {
	r := NewRecorder(t.TempDir())
	const extra = 50
	for i := range maxSessionsPerSlot + extra {
		r.AddTraffic(t0, src("a", "m", fmt.Sprintf("s%04d", i), "s0000"), tok(1, int64(i), 1))
	}
	// a session already in the slot keeps counting after the cap is reached
	r.AddTraffic(t0, src("a", "m", "s0001", ""), tok(1, 5, 5))
	if got := len(r.open.Sessions); got != maxSessionsPerSlot+1 {
		t.Fatalf("%d sessions kept, want the cap and one for the overflow", got)
	}
	w := window(r, 0)
	var own Traffic
	walk(w.Sessions, func(s SessionUsage) { own.Add(s.Own) })
	if w.Total.Requests != maxSessionsPerSlot+extra+1 || own != w.Total {
		t.Fatalf("total %+v own %+v", w.Total, own)
	}
	other := find(w, OtherSession)
	if other == nil || other.Own.Requests != extra || other.Parent != "" {
		t.Fatalf("overflow row: %+v", other)
	}
	if !hasNested(w, "s0001") || find(w, "s0000").Own.Requests != 1 {
		t.Fatal("sessions counted before the cap keep their own rows")
	}
}

func hasNested(w SessionWindow, id string) bool {
	found := false
	walk(w.Sessions, func(s SessionUsage) { found = found || s.ID == id })
	return found
}

func TestAnOversizedOrBrokenHistoryLineIsSkippedAndCounted(t *testing.T) {
	dir := t.TempDir()
	good := `{"t":"2026-10-04T11:55:00Z","accounts":{"a":{"models":{"m":{"requests":5}}}}}` + "\n"
	huge := `{"t":"2026-10-04T11:50:00Z","x":"` + strings.Repeat("y", MaxLine+10) + `"}` + "\n"
	body := good + huge + "not json\n" + strings.Replace(good, "11:55", "11:56", 1)
	if err := os.WriteFile(filepath.Join(dir, "2026-10-04.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewRecorder(dir).Sessions(t0)
	if s.Week.Total.Requests != 10 || s.SkippedLines != 2 {
		t.Fatalf("total %+v skipped %d: the lines after a bad one must still be read", s.Week.Total, s.SkippedLines)
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

func TestAllRootsListsEveryRootEvenWhenTheWindowIsCut(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0, src("a", "opus", "s1", ""), tok(1, 300, 0))
	r.AddTraffic(t0, src("a", "opus", "s1/x", "s1"), tok(1, 50, 0))
	r.AddTraffic(t0.Add(time.Minute), src("a", "opus", "s2", ""), tok(2, 200, 0))
	r.AddTraffic(t0.Add(2*time.Minute), src("a", "opus", "s3", ""), tok(3, 100, 0))

	w := window(r, 1) // only the largest root is listed
	if len(w.Sessions) != 1 || w.Other.Sessions != 2 {
		t.Fatalf("setup: %+v", w)
	}
	got := map[string]int64{}
	for _, root := range w.AllRoots {
		got[root.ID] = root.Total.Tokens()
	}
	if len(w.AllRoots) != 3 || got["s1"] != 350 || got["s2"] != 200 || got["s3"] != 100 {
		t.Fatalf("every root with its family's total: %+v", w.AllRoots)
	}
	for _, root := range w.AllRoots {
		if root.Last.IsZero() {
			t.Fatal("a root carries when it was last seen")
		}
	}
}
