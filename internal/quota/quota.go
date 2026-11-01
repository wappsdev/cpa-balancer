// Package quota turns Anthropic rate-limit data into per-account, per-bucket readings.
package quota

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Bucket string

const (
	Main    Bucket = "main"
	Fable   Bucket = "fable"
	Session Bucket = "session"
)

const week = 7 * 24 * time.Hour

// Reading is one quota observation. ResetsAt is nil only for Session: no 5-hour window is running.
type Reading struct {
	Util       float64    `json:"util"`
	ResetsAt   *time.Time `json:"resets_at"`
	ObservedAt time.Time  `json:"observed_at"`
	Source     string     `json:"source"` // header | probe | anchor | estimate
}

func (r Reading) Running(now time.Time) bool { return r.ResetsAt != nil && r.ResetsAt.After(now) }

type Readings map[Bucket]Reading

const headerPrefix = "Anthropic-Ratelimit-Unified-"

func headerReading(h http.Header, window string, now time.Time) (Reading, bool) {
	util, errUtil := strconv.ParseFloat(h.Get(headerPrefix+window+"-Utilization"), 64)
	reset, errReset := strconv.ParseInt(h.Get(headerPrefix+window+"-Reset"), 10, 64)
	if errUtil != nil || errReset != nil {
		return Reading{}, false
	}
	at := time.Unix(reset, 0).UTC()
	return Reading{Util: util * 100, ResetsAt: &at, ObservedAt: now, Source: "header"}, true
}

// FromHeaders reads the shared 5-hour window and, for a model of a configured bucket, the
// all-models weekly window. The unified 7d header is that window on a Fable request too (Fable
// use counts toward it); Fable's own weekly limit is only in the usage endpoint (FromProbe).
func FromHeaders(h http.Header, model string, mainModels, fableModels []string, now time.Time) Readings {
	out := Readings{}
	if r, ok := headerReading(h, "5h", now); ok {
		out[Session] = r
	}
	if r, ok := headerReading(h, "7d", now); ok && (slices.Contains(mainModels, model) || slices.Contains(fableModels, model)) {
		out[Main] = r
	}
	return out
}

type usageLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

func (l usageLimit) bucket() (Bucket, bool) {
	switch l.Kind {
	case "session":
		return Session, true
	case "weekly_all":
		return Main, true
	case "weekly_scoped":
		if l.Scope != nil && l.Scope.Model != nil && strings.EqualFold(l.Scope.Model.DisplayName, "fable") {
			return Fable, true
		}
	}
	return "", false
}

// FromProbe parses GET /api/oauth/usage. A session limit without resets_at is a closed window; no
// session limit at all (fiveHour false) is a plan without a 5-hour limit.
func FromProbe(body []byte, now time.Time) (readings Readings, fiveHour bool, err error) {
	var payload struct {
		Limits []usageLimit `json:"limits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, err
	}
	out := Readings{}
	for _, l := range payload.Limits {
		b, ok := l.bucket()
		fiveHour = fiveHour || (ok && b == Session)
		if !ok || l.Percent == nil {
			continue
		}
		r := Reading{Util: *l.Percent, ObservedAt: now, Source: "probe"}
		if l.ResetsAt != "" {
			at, err := time.Parse(time.RFC3339Nano, l.ResetsAt)
			if err != nil {
				continue
			}
			at = at.UTC()
			r.ResetsAt = &at
		} else if b != Session {
			continue
		}
		out[b] = r
	}
	return out, fiveHour, nil
}

// Merge keeps the newest reading per bucket; on equal times the later source wins.
func Merge(sources ...Readings) Readings {
	out := Readings{}
	for _, src := range sources {
		for b, r := range src {
			if cur, ok := out[b]; !ok || !r.ObservedAt.Before(cur.ObservedAt) {
				out[b] = r
			}
		}
	}
	return out
}

// RollForward: weekly windows run on a fixed schedule; a 5-hour window only runs after use.
func RollForward(in Readings, now time.Time) Readings {
	out := Readings{}
	for b, r := range in {
		switch {
		case r.ResetsAt == nil || r.ResetsAt.After(now):
			out[b] = r
		case b == Session:
			out[b] = Reading{ObservedAt: now, Source: "estimate"}
		default:
			at := *r.ResetsAt
			for !at.After(now) {
				at = at.Add(week)
			}
			out[b] = Reading{ResetsAt: &at, ObservedAt: now, Source: "estimate"}
		}
	}
	return out
}
