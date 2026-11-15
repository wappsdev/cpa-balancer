package engine

import (
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

func TestUsageReachesTheSessionsOfTheStatus(t *testing.T) {
	c := &clock{t0}
	rec := history.NewRecorder(t.TempDir())
	e := New(testConfig(t), newHost(), c.now, rec)
	main := usage(G, Opus, headers(52, 90, 30, 3, t0))
	main.SessionID = "11111111-aaaa-bbbb-cccc-000000000001"
	main.Detail.InputTokens, main.Detail.OutputTokens = 100, 10
	sub := usage(W, Opus, headers(10, 140, 5, 4, t0))
	sub.SessionID, sub.ParentSessionID = "11111111-aaaa-bbbb-cccc-000000000002", main.SessionID
	sub.Detail.InputTokens = 7
	e.OnUsage(main)
	e.OnUsage(sub)
	e.OnUsage(usage(G, Opus, headers(52, 90, 30, 3, t0))) // no session id

	s := rec.Sessions(c.t)
	if len(s.FiveHour.Sessions) != 2 || s.FiveHour.Total.Requests != 3 {
		t.Fatalf("%+v", s.FiveHour)
	}
	var parent history.SessionUsage
	for _, row := range s.FiveHour.Sessions {
		if row.ID == main.SessionID {
			parent = row
		}
	}
	if parent.Own.InputTokens != 100 || parent.Total.InputTokens != 107 || len(parent.Subagents) != 1 || parent.Subagents[0].Accounts[W].Requests != 1 {
		t.Fatalf("parent %+v", parent)
	}
}
