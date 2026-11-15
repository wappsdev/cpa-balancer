package history

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// sessionWindow is the use of [from, to), for the tests that look at one window.
func (r *Recorder) sessionWindow(from, to time.Time, limit int) SessionWindow {
	return buildWindow(r.slots(from, to), from, to, limit)
}

const uid = "11111111-aaaa-4bbb-8ccc-000000000001"

func TestCPAsSessionIdsBecomeTheKeysTheTranscriptsUse(t *testing.T) {
	for _, c := range []struct{ id, parent, wantID, wantParent string }{
		{"claude:" + uid, "", uid, ""},                                                                                     // a main session
		{"claude:" + uid + ":agent:ab12", "claude:" + uid, uid + "/ab12", uid},                                             // a subagent
		{"claude:" + uid + ":agent:cd34", "claude:" + uid + ":agent:ab12", uid + "/cd34", uid + "/ab12"},                   // nested
		{"claude:" + uid + ":agent:ab12", "", uid + "/ab12", uid},                                                          // the parent derived from the id
		{"claude:" + uid, "claude:" + "99999999-aaaa-4bbb-8ccc-000000000009", uid, "99999999-aaaa-4bbb-8ccc-000000000009"}, // parentCandidate
		{uid, "", uid, ""},                 // a bare id (the importer's) is already a key
		{"codex:abc", "", "codex:abc", ""}, // another client keeps today's handling
		{"", "", "", ""},
		{"claude:", "", "", ""},
		{"claude::agent:x", "", ":agent:x", ""}, // no session part: not a subagent form
	} {
		id, parent := CanonicalSession(c.id, c.parent)
		if id != c.wantID || parent != c.wantParent {
			t.Errorf("%q %q -> %q %q, want %q %q", c.id, c.parent, id, parent, c.wantID, c.wantParent)
		}
	}
}

func TestAHostShapedIdFitsTheBound(t *testing.T) {
	id := uid + "/" + strings.Repeat("a", 64)
	if !validID(id) || len(id) > maxIDLength {
		t.Fatalf("a uuid, a slash and a 64-character agent id must be a valid id: %d", len(id))
	}
}

func TestSubagentsDeeperThanTheLimitAreFoldedIntoTheirAncestor(t *testing.T) {
	r := NewRecorder(t.TempDir())
	const levels = 12
	for i := range levels {
		parent := ""
		if i > 0 {
			parent = fmt.Sprintf("s%02d", i-1)
		}
		r.AddTraffic(t0, src("a", "m", fmt.Sprintf("s%02d", i), parent), tok(1, 1, 1))
	}
	w := window(r, 0)
	depth, row := 0, &w.Sessions[0]
	for {
		depth++
		if len(row.Subagents) == 0 {
			break
		}
		row = &row.Subagents[0]
	}
	if len(w.Sessions) != 1 || depth != maxDepth {
		t.Fatalf("depth %d, want %d", depth, maxDepth)
	}
	// the last level holds its own request and those of every session below it
	if row.Own.Requests != levels-maxDepth+1 || w.Sessions[0].Total.Requests != levels || w.Total.Requests != levels {
		t.Fatalf("last level own %d, root total %d", row.Own.Requests, w.Sessions[0].Total.Requests)
	}
}

func TestCleanIDAndCleanModelKeepPlainNamesAndFoldTheRest(t *testing.T) {
	for in, want := range map[string]string{"": "", uid: uid, uid + "/ab12": uid + "/ab12", "has space": OtherSession, "<x>": OtherSession, strings.Repeat("a", maxIDLength+1): OtherSession} {
		if got := CleanID(in); got != want {
			t.Errorf("CleanID(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"": "", "claude-opus-5-5[1m]": "claude-opus-5-5[1m]", "<synthetic>": OtherSession, strings.Repeat("m", maxModelLength+1): OtherSession} {
		if got := CleanModel(in); got != want {
			t.Errorf("CleanModel(%q) = %q, want %q", in, got, want)
		}
	}
}
