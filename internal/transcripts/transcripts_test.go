package transcripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

var now = time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)

const (
	sid   = "11111111-aaaa-4bbb-8ccc-000000000001"
	sid2  = "22222222-aaaa-4bbb-8ccc-000000000002"
	opus  = "claude-opus-5-5"
	haiku = "claude-haiku-5-5"
	fable = "claude-fable-5-2"
)

func tok(requests, in, out int64) history.Traffic {
	return history.Traffic{Requests: requests, InputTokens: in, OutputTokens: out}
}

func hour(daysAgo int, h int) time.Time {
	return time.Date(2026, 10, 10-daysAgo, h, 0, 0, 0, time.UTC)
}

func store(t *testing.T, rows map[Key]history.Traffic) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "transcripts")
	if err := Write(dir, rows, nil, now); err != nil {
		t.Fatal(err)
	}
	return dir
}

// snapshot is every file of a directory with its bytes.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func find(s Summary, id string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return &s.Sessions[i]
		}
	}
	return nil
}

func TestWriteIsAPureFunctionOfItsInput(t *testing.T) {
	rows := map[Key]history.Traffic{
		{Hour: hour(1, 9), Session: sid, Model: opus}:   tok(2, 10, 5),
		{Hour: hour(1, 10), Session: sid, Model: opus}:  tok(1, 1, 1),
		{Hour: hour(3, 23), Session: sid2, Model: opus}: tok(1, 7, 7),
	}
	dir := store(t, rows)
	first := snapshot(t, dir)
	if len(first) != 2 || first["2026-10-09.jsonl"] == "" || first["2026-10-07.jsonl"] == "" {
		t.Fatalf("one file per UTC day: %v", first)
	}
	if err := Write(dir, rows, nil, now); err != nil {
		t.Fatal(err)
	}
	if again := snapshot(t, dir); len(again) != len(first) || again["2026-10-09.jsonl"] != first["2026-10-09.jsonl"] || again["2026-10-07.jsonl"] != first["2026-10-07.jsonl"] {
		t.Fatalf("a rerun must leave the same bytes:\n%v\n%v", first, again)
	}
}

func TestWriteReplacesTheDaysTheRowsCoverAndKeepsEveryOlderDay(t *testing.T) {
	dir := store(t, map[Key]history.Traffic{
		{Hour: hour(7, 9), Session: "oldest", Model: opus}: tok(1, 1, 1), // Oct 3
		{Hour: hour(5, 9), Session: "older", Model: opus}:  tok(1, 2, 2), // Oct 5
		{Hour: hour(2, 9), Session: "stale", Model: opus}:  tok(1, 3, 3), // Oct 8
		{Hour: hour(1, 9), Session: "gone", Model: opus}:   tok(1, 4, 4), // Oct 9
	})
	before := snapshot(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Claude Code has deleted the transcripts before Oct 5 (and the one of Oct 8 and Oct 9 is gone too).
	if err := Write(dir, map[Key]history.Traffic{{Hour: hour(0, 8), Session: "fresh", Model: opus}: tok(1, 5, 5), {Hour: hour(5, 11), Session: "older", Model: opus}: tok(2, 20, 20)}, nil, now); err != nil {
		t.Fatal(err)
	}
	got := snapshot(t, dir)
	if got["2026-10-03.jsonl"] != before["2026-10-03.jsonl"] || got["2026-10-03.jsonl"] == "" {
		t.Fatalf("a day before the earliest message is left as it was: %v", got)
	}
	if _, ok := got["2026-10-08.jsonl"]; ok {
		t.Fatalf("a day in the covered range with no rows is removed: %v", got)
	}
	if _, ok := got["2026-10-09.jsonl"]; ok || got["2026-10-10.jsonl"] == "" || got["2026-10-05.jsonl"] == before["2026-10-05.jsonl"] || got["notes.txt"] == "" {
		t.Fatalf("%v", got)
	}
}

func TestWriteDropsDaysPastTheRetention(t *testing.T) {
	old := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).Add(-history.Retention - 48*time.Hour)
	dir := filepath.Join(t.TempDir(), "transcripts")
	if err := Write(dir, map[Key]history.Traffic{{Hour: old, Session: sid, Model: opus}: tok(1, 1, 1)}, nil, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(snapshot(t, dir)) != 1 {
		t.Fatal("the fixture day is not there")
	}
	if err := Write(dir, map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid, Model: opus}: tok(1, 1, 1)}, nil, now); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, dir); len(got) != 1 || got["2026-10-09.jsonl"] == "" {
		t.Fatalf("%v", got)
	}
}

func TestWriteWithNoRowsWritesNothingAndRemovesNothing(t *testing.T) {
	dir := store(t, map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid, Model: opus}: tok(1, 1, 1)})
	before := snapshot(t, dir)
	for _, rows := range []map[Key]history.Traffic{nil, {}} {
		if err := Write(dir, rows, map[string]Title{sid: {Text: "x", Kind: KindAI}}, now); err == nil {
			t.Fatal("expected an error")
		}
	}
	after := snapshot(t, dir)
	if len(after) != len(before) || after["2026-10-09.jsonl"] != before["2026-10-09.jsonl"] {
		t.Fatalf("%v", after)
	}
}

func TestTitlesAreMergedANewOneReplacesTheOldAndASessionNotSeenKeepsItsName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	rows := map[Key]history.Traffic{{Hour: hour(5, 9), Session: sid, Model: opus}: tok(1, 1, 1), {Hour: hour(5, 9), Session: sid2, Model: opus}: tok(1, 1, 1)}
	if err := Write(dir, rows, map[string]Title{sid: {Text: "Old name", Kind: KindAI}, sid2: {Text: "Other old name", Kind: KindCustom}}, now); err != nil {
		t.Fatal(err)
	}
	// the next run sees only the newer messages of sid, and a new name for it
	if err := Write(dir, map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid, Model: opus}: tok(1, 1, 1)}, map[string]Title{sid: {Text: "New name", Kind: KindCustom}}, now); err != nil {
		t.Fatal(err)
	}
	s := Query(dir, now, 30)
	a, b := find(s, sid), find(s, sid2)
	if a == nil || a.Title != "New name" || a.TitleKind != KindCustom || b == nil || b.Title != "Other old name" || b.TitleKind != KindCustom {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestATitleGoesWhenItsSessionLeavesTheStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	if err := Write(dir, map[Key]history.Traffic{{Hour: hour(2, 9), Session: sid, Model: opus}: tok(1, 1, 1)}, map[string]Title{sid: {Text: "Name", Kind: KindAI}}, now); err != nil {
		t.Fatal(err)
	}
	// the day of sid is covered by a later run that no longer holds it
	if err := Write(dir, map[Key]history.Traffic{{Hour: hour(3, 9), Session: sid2, Model: opus}: tok(1, 1, 1)}, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, TitlesFile)); err == nil {
		t.Fatal("no session with a name is left, so no titles file")
	}
}

func TestQueryRollsSubagentsIntoTheirParentAndKeepsThemApart(t *testing.T) {
	dir := store(t, map[Key]history.Traffic{
		{Hour: hour(1, 9), Session: sid, Model: opus}:                           tok(2, 100, 10),
		{Hour: hour(1, 9), Session: sid + "/ab12", Model: haiku}:                tok(3, 30, 3),
		{Hour: hour(1, 10), Session: sid + "/cd34", Model: haiku}:               tok(1, 5, 1),
		{Hour: hour(1, 9), Session: sid + "/ab12", Model: fable, Advisor: true}: tok(1, 900, 90),
		{Hour: hour(2, 9), Session: sid2, Model: opus}:                          tok(1, 1, 1),
	})
	s := Query(dir, now, 7)
	root := find(s, sid)
	if root == nil || len(s.Sessions) != 2 {
		t.Fatalf("%+v", s.Sessions)
	}
	if root.Total != tok(6, 135, 14) || root.Models[opus] != tok(2, 100, 10) || root.Models[haiku] != tok(4, 35, 4) {
		t.Fatalf("a parent counts its subagents: %+v", root)
	}
	if len(root.Subagents) != 2 || root.Subagents[0].ID != sid+"/ab12" || root.Subagents[0].Total != tok(3, 30, 3) || root.Subagents[0].Advisor != tok(1, 900, 90) {
		t.Fatalf("each child is its own row: %+v", root.Subagents)
	}
	if root.Advisor != tok(1, 900, 90) || root.AdvisorModels[fable] != tok(1, 900, 90) || len(root.AdvisorModels) != 1 {
		t.Fatalf("advisor use rolls up, by its own model: %+v", root)
	}
	// advisor use is not in any total; the window total is the sum of the roots
	if s.Total != tok(7, 136, 15) || s.Advisor != tok(1, 900, 90) {
		t.Fatalf("total %+v advisor %+v", s.Total, s.Advisor)
	}
	if !root.First.Equal(hour(1, 9)) || !root.Last.Equal(hour(1, 10)) {
		t.Fatalf("first %v last %v", root.First, root.Last)
	}
}

func TestAChildWithoutItsParentsOwnRowStillHeadsAFamily(t *testing.T) {
	dir := store(t, map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid + "/ab12", Model: haiku}: tok(1, 1, 1)})
	s := Query(dir, now, 7)
	if len(s.Sessions) != 1 || s.Sessions[0].ID != sid || s.Sessions[0].Total != tok(1, 1, 1) || len(s.Sessions[0].Subagents) != 1 {
		t.Fatalf("%+v", s.Sessions)
	}
}

func TestQueryReadsOnlyTheWholeHoursOfTheRange(t *testing.T) {
	dir := store(t, map[Key]history.Traffic{
		{Hour: hour(7, 12), Session: sid, Model: opus}:   tok(1, 1, 1), // the hour 7 days before 12:30 begins in: inside
		{Hour: hour(7, 11), Session: sid2, Model: opus}:  tok(1, 2, 2), // the hour before: outside 7 days, inside 30
		{Hour: hour(40, 9), Session: "old", Model: opus}: tok(1, 4, 4),
	})
	week, month := Query(dir, now, 7), Query(dir, now, 30)
	if week.Total.Requests != 1 || find(week, sid) == nil || find(week, sid2) != nil {
		t.Fatalf("7 days: %+v", week)
	}
	if month.Total.Requests != 2 || find(month, sid2) == nil || find(month, "old") != nil {
		t.Fatalf("30 days: %+v", month)
	}
	if !week.To.Equal(now) || !week.From.Equal(now.Add(-7*24*time.Hour).Truncate(time.Hour)) {
		t.Fatalf("%v %v", week.From, week.To)
	}
}

func TestOnlyTheLargestSessionsAreListedAndTheRestIsSummed(t *testing.T) {
	rows := map[Key]history.Traffic{}
	for i := range TopSessions + 5 {
		rows[Key{Hour: hour(1, 9), Session: "s" + string(rune('A'+i/26)) + string(rune('a'+i%26)), Model: opus}] = tok(1, int64(i+1), 0)
	}
	s := Query(store(t, rows), now, 7)
	if len(s.Sessions) != TopSessions || s.Other.Sessions != 5 || s.Other.Total.Requests != 5 {
		t.Fatalf("%d listed, other %+v", len(s.Sessions), s.Other)
	}
	var sum history.Traffic
	for _, r := range s.Sessions {
		sum.Add(r.Total)
	}
	sum.Add(s.Other.Total)
	if sum != s.Total || s.Sessions[0].Total.InputTokens < s.Sessions[1].Total.InputTokens {
		t.Fatalf("listed + other must equal the total, largest first: %+v %+v", sum, s.Total)
	}
	if s.Other.Total.InputTokens != 1+2+3+4+5 {
		t.Fatalf("the smallest are the ones left out: %+v", s.Other)
	}
}

func TestAStoreFileFromOutsideIsReadWithTheLiveStoresBounds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := `{"hour":"2026-10-09T09:00:00Z","session":"<img src=x>","model":"<script>","requests":1,"input_tokens":3}`
	good := `{"hour":"2026-10-09T09:00:00Z","session":"` + sid + `","model":"` + opus + `","requests":1,"input_tokens":4}`
	huge := `{"session":"` + strings.Repeat("s", history.MaxLine+10) + `"}`
	body := strings.Join([]string{bad, "not json", huge, good, `{"hour":"garbage","session":"x","requests":1}`}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-10-09.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Query(dir, now, 7)
	other := find(s, history.OtherSession)
	if other == nil || other.Models[history.OtherSession].InputTokens != 3 || find(s, sid) == nil || s.Total.Requests != 2 {
		t.Fatalf("a hostile id and model are stored as (other): %+v", s.Sessions)
	}
	if s.SkippedLines != 3 {
		t.Fatalf("the broken, the oversized and the one with no hour are counted: %d", s.SkippedLines)
	}
}

func TestQueryStopsAtItsRowLimitAndSaysSo(t *testing.T) {
	old := maxRows
	maxRows = 3
	defer func() { maxRows = old }()
	rows := map[Key]history.Traffic{}
	for i := range 5 {
		rows[Key{Hour: hour(1, i), Session: sid, Model: opus}] = tok(1, 1, 1)
	}
	s := Query(store(t, rows), now, 7)
	if !s.Truncated || s.Total.Requests != 3 {
		t.Fatalf("%+v", s)
	}
}

func TestAMissingStoreIsAnEmptyAnswer(t *testing.T) {
	s := Query(filepath.Join(t.TempDir(), "none"), now, 7)
	if len(s.Sessions) != 0 || s.Total.Requests != 0 || s.Sessions == nil || s.Truncated {
		t.Fatalf("%+v", s)
	}
}

func TestTheModelsOfOneSessionAreCapped(t *testing.T) {
	rows := map[Key]history.Traffic{}
	for i := range 12 {
		rows[Key{Hour: hour(1, 9), Session: sid, Model: "model-" + string(rune('a'+i))}] = tok(1, 1, 1)
	}
	s := Query(store(t, rows), now, 7)
	row := find(s, sid)
	if len(row.Models) != 9 || row.Models[history.OtherSession].Requests != 4 || row.Total.Requests != 12 {
		t.Fatalf("%+v", row.Models)
	}
}

func TestAnEmptyTitleIsNoTitleAndTitlesAreCleanedOnTheWayIn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	rows := map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid, Model: opus}: tok(1, 1, 1)}
	long := strings.Repeat("é", 300)
	err := Write(dir, rows, map[string]Title{
		sid:  {Text: "  a\x00b\n\u202ec" + long, Kind: KindCustom},
		sid2: {Text: " \x01 ", Kind: KindAI}, // nothing left once cleaned
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, TitlesFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), sid2) || strings.ContainsAny(string(b), "\u202e") || strings.Contains(string(b), `\u0000`) {
		t.Fatalf("what the store holds is clean: %s", b)
	}
	got := find(Query(dir, now, 7), sid)
	if got == nil || got.TitleKind != KindCustom || len([]rune(got.Title)) != MaxTitle || !strings.HasPrefix(got.Title, "abc") {
		t.Fatalf("%+v", got)
	}
}

func TestTheTitleOfASessionWithNoRowInTheStoreIsNotKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	rows := map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid + "/ab12", Model: opus}: tok(1, 1, 1)}
	if err := Write(dir, rows, map[string]Title{sid: {Text: "kept", Kind: KindAgent}, sid2: {Text: "dropped", Kind: KindAI}}, now); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, TitlesFile))
	if !strings.Contains(string(b), "kept") || strings.Contains(string(b), "dropped") {
		t.Fatalf("a family's title is kept, an absent session's is not: %s", b)
	}
	if err := Write(dir, rows, nil, now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, TitlesFile)); !strings.Contains(string(b), "kept") {
		t.Fatalf("a run that brings no titles keeps the ones a session has: %s", b)
	}
}

func TestATitleFileFromOutsideIsReadWithTheSameBounds(t *testing.T) {
	dir := store(t, map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid, Model: opus}: tok(1, 1, 1)})
	body := `{"session":"` + sid + `","title":"<img src=x onerror=1>` + strings.Repeat("t", 500) + `","kind":"custom"}` + "\n" +
		`{"session":"` + sid + `","title":"second line is ignored","kind":"ai"}` + "\nnot json\n" +
		`{"session":"` + sid2 + `","title":"` + strings.Repeat("x", history.MaxLine) + `","kind":"ai"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, TitlesFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := find(Query(dir, now, 7), sid)
	if got == nil || len([]rune(got.Title)) != MaxTitle || !strings.HasPrefix(got.Title, "<img") {
		t.Fatalf("a long title is cut (the page escapes it): %+v", got)
	}
}

func TestOnlyTheLargestSubagentsOfASessionAreListedAndTheRestIsSummed(t *testing.T) {
	rows := map[Key]history.Traffic{{Hour: hour(1, 9), Session: sid, Model: opus}: tok(1, 1000, 0)}
	const extra = 7
	for i := range TopSubagents + extra {
		rows[Key{Hour: hour(1, 9), Session: sid + "/a" + string(rune('a'+i)), Model: haiku}] = tok(1, int64(i+1), 0)
	}
	rows[Key{Hour: hour(1, 9), Session: sid + "/az", Model: fable, Advisor: true}] = tok(1, 5000, 0) // the smallest child, but an advisor makes it count
	s := Query(store(t, rows), now, 7)
	root := find(s, sid)
	if len(root.Subagents) != TopSubagents || root.OtherSubagents == nil || root.OtherSubagents.Sessions != extra+1 {
		t.Fatalf("%d listed, other %+v", len(root.Subagents), root.OtherSubagents)
	}
	var sum history.Traffic
	for _, c := range root.Subagents {
		sum.Add(c.Total)
	}
	sum.Add(root.OtherSubagents.Total)
	own := tok(1, 1000, 0)
	own.Add(sum)
	if root.Total != own || root.Advisor != tok(1, 5000, 0) {
		t.Fatalf("the family total already holds every child: %+v vs %+v", root.Total, own)
	}
	if root.Subagents[0].ID != sid+"/az" {
		t.Fatalf("a child is ranked with its advisor use: %q", root.Subagents[0].ID)
	}
}
