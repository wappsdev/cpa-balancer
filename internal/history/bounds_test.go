package history

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAModelOutsideTheAllowedFormatIsStoredAsOther(t *testing.T) {
	r := NewRecorder(t.TempDir())
	for _, model := range []string{"claude-opus-5-5", "claude-opus-5-5[1m]", "claude-haiku-4-5@20251001", ""} {
		r.AddTraffic(t0, src("a", model, "s1", ""), tok(1, 1, 1))
	}
	bad := []string{strings.Repeat("m", 65), "has space", "a/b", "<script>", "tab\there", "naïve"}
	for _, model := range bad {
		r.AddTraffic(t0, src("a", model, "s1", ""), tok(1, 1, 1))
	}
	r.AddAdvisor(t0, "s1", "", "<advisor>", tok(1, 1, 1))
	w := window(r, 0)
	s := find(w, "s1")
	if s.Models[OtherSession].Requests != int64(len(bad)) || s.Models["claude-opus-5-5[1m]"].Requests != 1 || s.Models[""].Requests != 1 || len(s.Models) != 5 {
		t.Fatalf("session models %+v", s.Models)
	}
	if s.AdvisorModels[OtherSession].Requests != 1 {
		t.Fatalf("advisor models %+v", s.AdvisorModels)
	}
	if got := r.open.Accounts["a"].Models[OtherSession].Requests; got != int64(len(bad)) {
		t.Fatalf("account models %+v", r.open.Accounts["a"].Models)
	}
	if w.Total.Requests != int64(len(bad))+4 || s.Total != w.Total {
		t.Fatalf("every request still counts: %+v %+v", w.Total, s.Total)
	}
}

func TestModelsPerAccountAndSessionAreCappedAndTheOverflowIsFolded(t *testing.T) {
	r := NewRecorder(t.TempDir())
	const extra = 7
	for i := range maxKeysPerMap + extra {
		r.AddTraffic(t0, src("a", fmt.Sprintf("model-%02d", i), "s1", ""), tok(1, 1, 1))
		r.AddAdvisor(t0, "s1", "", fmt.Sprintf("advisor-%02d", i), tok(1, 1, 1))
	}
	r.AddTraffic(t0, src("a", "model-00", "s1", ""), tok(1, 1, 1)) // a model already there still counts under its own name
	w := window(r, 0)
	s := find(w, "s1")
	if len(s.Models) != maxKeysPerMap+1 || s.Models[OtherSession].Requests != extra || s.Models["model-00"].Requests != 2 {
		t.Fatalf("%d session models: %+v", len(s.Models), s.Models)
	}
	if len(s.AdvisorModels) != maxKeysPerMap+1 || s.AdvisorModels[OtherSession].Requests != extra {
		t.Fatalf("%d advisor models", len(s.AdvisorModels))
	}
	if got := len(r.open.Accounts["a"].Models); got != maxKeysPerMap+1 {
		t.Fatalf("%d account models", got)
	}
	var total Traffic
	for _, m := range s.Models {
		total.add(m)
	}
	if total != s.Total || w.Total.Requests != maxKeysPerMap+extra+1 || w.Accounts["a"] != w.Total {
		t.Fatalf("models %+v total %+v window %+v", total, s.Total, w.Total)
	}
}

// maxed fills a slot as far as the caps let it go, with the longest keys and the largest numbers.
func maxed() *Slot {
	big := Traffic{Requests: math.MaxInt64 / 4, Failed: math.MaxInt64 / 4, InputTokens: math.MaxInt64 / 4, OutputTokens: math.MaxInt64 / 4,
		CacheReadTokens: math.MaxInt64 / 4, CacheCreationTokens: math.MaxInt64 / 4}
	s := NewSlot(t0)
	for i := range maxSessionsPerSlot + 5 {
		id := fmt.Sprintf("%064d", i)
		for a := range maxKeysPerMap + 5 {
			for m := range maxKeysPerMap + 5 {
				s.AddTraffic(t0, Source{Account: fmt.Sprintf("account-%02d", a), Model: fmt.Sprintf("%064d", m), Session: id, Parent: fmt.Sprintf("%064d", i+1)}, big)
			}
		}
		for m := range maxKeysPerMap + 5 {
			s.AddAdvisor(t0, id, "", fmt.Sprintf("%063d%d", m, 1), big)
		}
	}
	return s
}

func TestASlotWithEveryCapMaxedStillFitsTheReadersLineLimit(t *testing.T) {
	s := maxed()
	if len(s.Sessions) != maxSessionsPerSlot+1 {
		t.Fatalf("%d sessions", len(s.Sessions))
	}
	line, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) >= maxLine/2 {
		t.Fatalf("a maxed slot is %d bytes; the reader's limit is %d and the caps must leave room", len(line), maxLine)
	}
}

func TestAnOversizedSlotIsWrittenWithoutItsSessionsAndStillCounts(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	r.AddTraffic(t0, src("a", "m", "s1", ""), tok(3, 30, 3))
	// More events than any real slot holds: only the sessions can be dropped to make it fit.
	for range maxLine / 100 {
		r.AddEvent(Event{At: t0, Kind: "switch", Bucket: "main", From: "a", To: "b", Reason: strings.Repeat("r", 60)})
	}
	if err := r.Flush(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-10-04.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxLine {
		t.Fatalf("written line is %d bytes, over the limit", len(raw))
	}
	s := NewRecorder(dir).Sessions(t0)
	none := find(s.Week, "")
	if s.SkippedLines != 0 || s.Week.Total.Requests != 3 || none == nil || none.Own.Requests != 3 || find(s.Week, "s1") != nil {
		t.Fatalf("%+v", s)
	}
}
