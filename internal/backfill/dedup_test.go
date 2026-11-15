package backfill

import (
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

func TestAMessageRepeatedInTwoTranscriptsCountsOnce(t *testing.T) {
	// a resumed session copies its earlier messages into a new transcript
	root := transcripts(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl": {assistant(sessA, "msg_shared", "claude-opus-5-5", at, usage{in: 10, out: 5}), assistant(sessA, "msg_a", "claude-opus-5-5", at.Add(time.Minute), usage{in: 1, out: 1})},
		"proj-a/" + sessB + ".jsonl": {assistant(sessB, "msg_shared", "claude-opus-5-5", at, usage{in: 10, out: 5}), assistant(sessB, "msg_b", "claude-opus-5-5", at.Add(2*time.Minute), usage{in: 2, out: 1})},
	})
	hist := t.TempDir()
	rep := run(t, root, hist, false)
	w := window(t, hist)
	if rep.Messages != 3 || w.Total.Requests != 3 || w.Total.InputTokens != 13 {
		t.Fatalf("report %+v total %+v", rep, w.Total)
	}
	// the tie goes to the smaller session id, whatever the order the files were read in
	if a := row(w, sessA); a == nil || a.Own.Requests != 2 {
		t.Fatalf("%+v", a)
	}
	if b := row(w, sessB); b == nil || b.Own.Requests != 1 {
		t.Fatalf("%+v", b)
	}
}

func TestMessagesWithoutAnIdAreNotMergedAcrossSessions(t *testing.T) {
	noID := func(session string) string {
		return assistant(session, "", "claude-opus-5-5", at, usage{in: 3, out: 1})
	}
	root := transcripts(t, map[string][]string{
		"proj-a/" + sessA + ".jsonl": {noID(sessA)},
		"proj-a/" + sessB + ".jsonl": {noID(sessB)},
	})
	hist := t.TempDir()
	run(t, root, hist, false)
	if w := window(t, hist); w.Total.Requests != 2 {
		t.Fatalf("%+v", w.Total)
	}
}

func TestImportedAndLiveRowsOfOneSessionLandOnTheSameKey(t *testing.T) {
	root := transcripts(t, map[string][]string{"proj-a/" + sessA + ".jsonl": {assistant(sessA, "m1", "claude-opus-5-5", at, usage{in: 10, out: 1})}})
	hist := t.TempDir()
	live := history.NewRecorder(hist)
	id, parent := history.CanonicalSession("claude:"+sessA, "")
	live.AddTraffic(at.Add(2*time.Hour), history.Source{Account: "g", Model: "claude-opus-5-5", Session: id, Parent: parent}, history.Traffic{Requests: 1, InputTokens: 5})
	if err := live.Flush(at.Add(3 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	run(t, root, hist, false)
	w := window(t, hist)
	if len(w.Sessions) != 1 || w.Sessions[0].ID != sessA || w.Sessions[0].Own.Requests != 2 {
		t.Fatalf("%+v", w.Sessions)
	}
}
