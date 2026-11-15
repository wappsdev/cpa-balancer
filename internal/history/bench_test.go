package history

import (
	"fmt"
	"testing"
	"time"
)

// A busy week: every 5-minute slot holds 12 sessions (some are subagents) over 3 accounts and 3 models.
func busyWeek(tb testing.TB) (*Recorder, time.Time) {
	tb.Helper()
	r := NewRecorder(tb.TempDir())
	end := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for at := end.Add(-WeekWindow); at.Before(end); at = at.Add(SlotLength) {
		day := at.YearDay()*288 + int(at.Sub(at.Truncate(24*time.Hour))/SlotLength)
		for i := range 12 {
			session, parent := fmt.Sprintf("%08d-aaaa-4bbb-8ccc-%012d", day/12, i%4), ""
			if i%3 == 0 {
				session, parent = fmt.Sprintf("%08d-bbbb-4bbb-8ccc-%012d", day/12, i), fmt.Sprintf("%08d-aaaa-4bbb-8ccc-%012d", day/12, i%4)
			}
			r.AddTraffic(at, Source{Account: fmt.Sprintf("account-%d", i%3), Model: fmt.Sprintf("claude-model-%d", i%3), Session: session, Parent: parent},
				Traffic{Requests: 3, InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 50000, CacheCreationTokens: 800})
		}
		if err := r.Flush(at.Add(SlotLength)); err != nil {
			tb.Fatal(err)
		}
	}
	return r, end
}

// BenchmarkSessionsOfABusyWeek times what every status poll costs: reading and aggregating seven days of slots.
func BenchmarkSessionsOfABusyWeek(b *testing.B) {
	r, end := busyWeek(b)
	b.ResetTimer()
	for range b.N {
		s := r.Sessions(end)
		if s.Week.Total.Requests == 0 {
			b.Fatal("no traffic")
		}
	}
}
