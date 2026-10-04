package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func TestSlotsAreWrittenWhenTheyClose(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	r.AddTraffic(t0.Add(time.Minute), "w", "claude-opus-5-5", Traffic{Requests: 1, InputTokens: 100, OutputTokens: 10})
	r.AddTraffic(t0.Add(2*time.Minute), "w", "claude-opus-5-5", Traffic{Requests: 1, InputTokens: 50, OutputTokens: 5, Failed: 1})
	r.Snapshot(t0.Add(3*time.Minute), map[string]string{"main": "w"}, map[string]Levels{"w": {Main: f(40), MainCap: f(99), Session: f(10), SessionOpen: true}})
	r.Snapshot(t0.Add(4*time.Minute), map[string]string{"main": "w"}, map[string]Levels{"w": {Main: f(41), MainCap: f(99), Session: f(12), SessionOpen: true}})
	r.AddEvent(Event{At: t0.Add(4 * time.Minute), Kind: "switch", Bucket: "main", From: "g", To: "w", Reason: "forced"})
	if err := r.Flush(t0.Add(4 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-04.jsonl")); err == nil {
		t.Fatal("an open slot must not be written")
	}
	if err := r.Flush(t0.Add(6 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-10-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := string(raw)
	for _, want := range []string{`"t":"2026-10-04T12:00:00Z"`, `"requests":2`, `"failed":1`, `"input_tokens":150`, `"main":41`, `"kind":"switch"`} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("expected one line, got %q", line)
	}
}

func TestDayRolloverAndPruning(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, t0.Add(-91*24*time.Hour).Format(dateLayout)+".jsonl")
	keep := filepath.Join(dir, t0.Add(-89*24*time.Hour).Format(dateLayout)+".jsonl")
	for _, p := range []string{old, keep} {
		os.WriteFile(p, []byte("{}\n"), 0o600)
	}
	r := NewRecorder(dir)
	late := time.Date(2026, 10, 4, 23, 58, 0, 0, time.UTC)
	r.AddTraffic(late, "w", "m", Traffic{Requests: 1})
	if err := r.Flush(late.Add(5 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-04.jsonl")); err != nil {
		t.Fatal("the 23:55 slot belongs to the 4th")
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("files older than 90 days must be pruned")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("files within 90 days must be kept")
	}
}

func TestQueryDownsamples(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	// Six 5-minute slots between 11:00 and 11:30 fall into one 30-minute point of the 7d range.
	for i := range 6 {
		at := time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC).Add(time.Duration(i) * SlotLength)
		active := "g"
		if i >= 4 {
			active = "w"
		}
		r.Snapshot(at, map[string]string{"main": active}, map[string]Levels{"w": {Main: f(float64(40 + i)), SessionOpen: i < 3, Session: f(float64(10 * i))}})
		r.AddTraffic(at, "w", "claude-opus-5-5", Traffic{Requests: 2, InputTokens: 10, OutputTokens: 5})
		r.AddTraffic(at, "w", "claude-fable-5-1", Traffic{Requests: 1})
	}
	r.AddEvent(Event{At: time.Date(2026, 10, 4, 11, 20, 0, 0, time.UTC), Kind: "anchor", Account: "g"})
	if err := r.Flush(t0); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "2026-10-04.jsonl"), append(mustRead(t, filepath.Join(dir, "2026-10-04.jsonl")), []byte("not json\n")...), 0o600)

	s, err := r.Query("7d", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.T) != 336 || s.Step != 1800 {
		t.Fatalf("points %d step %d", len(s.T), s.Step)
	}
	i := -1
	for k, ts := range s.T {
		if ts == time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC).Unix() {
			i = k
		}
	}
	w := s.Accounts["w"]
	if i < 0 || w == nil || *w.Main[i] != 45 || *w.Session[i] != 20 || w.Requests[i] != 18 || w.Tokens[i] != 90 || w.Models["claude-fable-5-1"][i] != 6 {
		t.Fatalf("point %d: %+v", i, w)
	}
	if s.Active["main"][i] != "g" || len(s.Events) != 1 {
		t.Fatalf("active %q events %v", s.Active["main"][i], s.Events)
	}
	if _, err := r.Query("1y", t0); err == nil {
		t.Fatal("unknown range must fail")
	}
}

func TestQueryIncludesTheOpenSlot(t *testing.T) {
	r := NewRecorder(t.TempDir())
	r.AddTraffic(t0.Add(-time.Minute), "w", "m", Traffic{Requests: 3})
	s, _ := r.Query("24h", t0)
	if len(s.T) != 288 || s.Accounts["w"].Requests[len(s.T)-2] != 3 {
		t.Fatalf("open slot missing: %+v", s.Accounts["w"])
	}
}

func TestASlotInMemoryAndOnDiskCountsOnce(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	r.AddTraffic(t0.Add(time.Minute), "w", "m", Traffic{Requests: 2})
	r.current(t0.Add(6 * time.Minute)) // closes 12:00 into pending without writing it
	// A flush racing the query wrote the same slot to disk after the memory copy was taken.
	if err := r.appendSlot(r.pending[0]); err != nil {
		t.Fatal(err)
	}
	s, _ := r.Query("24h", t0.Add(6*time.Minute))
	if got := s.Accounts["w"].Requests[len(s.T)-2]; got != 2 {
		t.Fatalf("requests %d, want 2", got)
	}
}

func TestPendingSlotsAreBoundedWhenWritesFail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(dir, nil, 0o600); err != nil { // a file where the directory should be
		t.Fatal(err)
	}
	r := NewRecorder(dir)
	for i := range maxPending + 50 {
		at := t0.Add(time.Duration(i) * SlotLength)
		r.AddTraffic(at, "w", "m", Traffic{Requests: 1})
		if err := r.Flush(at); err == nil && i > 0 {
			t.Fatal("flush must fail")
		}
	}
	if len(r.pending) != maxPending {
		t.Fatalf("pending %d, want %d", len(r.pending), maxPending)
	}
	// Slots 0..336 are closed; the last 288 are kept.
	if want := t0.Add(49 * SlotLength); !r.pending[0].T.Equal(want) {
		t.Fatalf("oldest kept %v, want %v", r.pending[0].T, want)
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
