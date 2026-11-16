package history

import "testing"

func TestVariantsOfAModelFoldIntoOne(t *testing.T) {
	for name, want := range map[string]string{
		"claude-opus-5-5":            "claude-opus-5-5",
		"claude-opus-5-5[1m]":        "claude-opus-5-5",  // a context size is not another model
		"claude-haiku-4-5-20251001":  "claude-haiku-4-5", // nor is a dated id
		"claude-haiku-4-5@20251001":  "claude-haiku-4-5",
		"claude-sonnet-5-5-2025[1m]": "claude-sonnet-5-5-2025", // only a whole date folds
		"claude-1-20251001":          "claude-1",
		OtherSession:                 OtherSession,
		"":                           "",
	} {
		if got := FoldModel(name); got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

func use(r *Recorder, n int, model, session, parent string, in int64) {
	r.AddTraffic(at(n), Source{Account: "u", Model: model, Session: session, Parent: parent}, input(in))
}

func lineOf(lines []ModelLine, model string) ModelLine {
	for _, l := range lines {
		if l.Model == model {
			return l
		}
	}
	return ModelLine{}
}

func TestASessionListsEachModelItUsedByFirstSeenWithItsOwnUseAndItsSubagents(t *testing.T) {
	r := NewRecorder(t.TempDir())
	use(r, 1, "opus", "s", "", 100)
	use(r, 3, "sonnet", "s/x", "s", 40)    // a subagent on another model: not a switch of the session
	use(r, 5, "opus[1m]", "s", "", 10)     // the same model in another context size
	use(r, 6, "haiku", "s", "", 7)         // the session itself moves to another model
	use(r, 7, OtherSession, "s/x", "s", 3) // a name that is not plain stays in the sums
	w := window(r, 0)
	got := find(w, "s")
	var sum Traffic
	for _, l := range got.ModelLines {
		sum.Add(l.Own)
		sum.Add(l.Subagents)
	}
	if sum != got.Total {
		t.Fatalf("the lines add up to the row: %+v against %+v", sum, got.Total)
	}
	order := ""
	for _, l := range got.ModelLines {
		order += l.Model + " "
	}
	if order != "opus sonnet haiku (other) " {
		t.Fatalf("by first seen: %q", order)
	}
	opus := lineOf(got.ModelLines, "opus")
	if opus.Own.InputTokens != 110 || opus.Subagents.InputTokens != 0 || !opus.First.Before(opus.Last) {
		t.Fatalf("both context sizes are one line, first to last seen: %+v", opus)
	}
	if sonnet := lineOf(got.ModelLines, "sonnet"); sonnet.Own.InputTokens != 0 || sonnet.Subagents.InputTokens != 40 {
		t.Fatalf("a subagent's use is apart from the session's own: %+v", sonnet)
	}
	if got.Models["opus"].InputTokens != 100 || got.Models["opus[1m]"].InputTokens != 10 {
		t.Fatalf("the row's Models keep the names as stored; only the lines fold them: %+v", got.Models)
	}
	if sub := got.Subagents[0]; len(sub.ModelLines) != 2 || lineOf(sub.ModelLines, "sonnet").Own.InputTokens != 40 {
		t.Fatalf("a subagent has lines of its own: %+v", sub.ModelLines)
	}
}

func TestTheLinesOfSeveralRootsMergeByModel(t *testing.T) {
	a := []ModelLine{{Model: "m1", First: at(2), Last: at(3), Own: tok(1, 10, 0)}, {Model: "m2", First: at(1), Last: at(1), Subagents: tok(1, 5, 0)}}
	b := []ModelLine{{Model: "m1", First: at(1), Last: at(5), Subagents: tok(1, 7, 0)}}
	got := MergeLines(a, b)
	if len(got) != 2 || got[0].Model != "m1" || got[0].Own.InputTokens != 10 || got[0].Subagents.InputTokens != 7 || !got[0].First.Equal(at(1)) || !got[0].Last.Equal(at(5)) || got[1].Model != "m2" {
		t.Fatalf("%+v", got)
	}
}
