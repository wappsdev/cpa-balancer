package backfill

import (
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

// Unknown is the account of a request the balancer's history cannot place.
const Unknown = "unknown"

// maxCarry is how long an account stays the answer after the balancer's last record of it. The
// plugin records its active accounts every few minutes while it runs, so a longer silence means
// it was not running and the transcript's traffic went elsewhere or nowhere we can see.
const maxCarry = time.Hour

type point struct {
	at      time.Time
	account string
}

// timeline answers which account the balancer had active for a bucket at a moment. Transcripts
// do not name the account, so this is a guess, and the rows built from it are marked estimated.
type timeline map[string][]point

// newTimeline reads the recorded switch events (exact times) and each slot's active account (the
// state at the slot's end).
func newTimeline(slots []*history.Slot) timeline {
	tl := timeline{}
	for _, s := range slots {
		for bucket, account := range s.Active {
			tl[bucket] = append(tl[bucket], point{s.T.Add(history.SlotLength), account})
		}
		for _, e := range s.Events {
			if e.Kind == "switch" && e.Bucket != "" {
				tl[e.Bucket] = append(tl[e.Bucket], point{e.At, e.To})
			}
		}
	}
	for _, points := range tl {
		sort.SliceStable(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
	}
	return tl
}

// at is the account of the latest record at or before t, or Unknown if there is none within
// maxCarry or the record names no account.
func (tl timeline) at(bucket string, t time.Time) string {
	points := tl[bucket]
	i := sort.Search(len(points), func(i int) bool { return points[i].at.After(t) })
	if i == 0 || t.Sub(points[i-1].at) > maxCarry || points[i-1].account == "" {
		return Unknown
	}
	return points[i-1].account
}
