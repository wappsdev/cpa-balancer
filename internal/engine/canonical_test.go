package engine

import (
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

func TestHostShapedSessionIdsAreStoredAsTranscriptKeys(t *testing.T) {
	const sid = "11111111-aaaa-4bbb-8ccc-000000000001"
	c := &clock{t0}
	rec := history.NewRecorder(t.TempDir())
	e := New(testConfig(t), newHost(), c.now, rec)
	main := usage(G, Opus, headers(52, 90, 30, 3, t0))
	main.SessionID = "claude:" + sid
	main.Detail.InputTokens = 100
	sub := usage(G, Opus, headers(52, 90, 30, 3, t0))
	sub.SessionID, sub.ParentSessionID = "claude:"+sid+":agent:ab12", "claude:"+sid
	sub.Detail.InputTokens = 10
	nested := usage(W, Opus, headers(10, 140, 5, 4, t0))
	nested.SessionID, nested.ParentSessionID = "claude:"+sid+":agent:cd34", "claude:"+sid+":agent:ab12"
	nested.Detail.InputTokens = 1
	e.OnUsage(main)
	e.OnUsage(sub)
	e.OnUsage(nested)

	roots := rec.Sessions(c.t).FiveHour.Sessions
	if len(roots) != 1 || roots[0].ID != sid || roots[0].Own.InputTokens != 100 || roots[0].Total.InputTokens != 111 {
		t.Fatalf("%+v", roots)
	}
	child := roots[0].Subagents
	if len(child) != 1 || child[0].ID != sid+"/ab12" || len(child[0].Subagents) != 1 || child[0].Subagents[0].ID != sid+"/cd34" {
		t.Fatalf("%+v", child)
	}
}
