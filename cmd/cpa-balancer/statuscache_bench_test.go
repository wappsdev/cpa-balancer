package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

// writeSyntheticHistory fills dir with a fortnight of slots shaped like a busy machine's: three
// accounts with levels, two models, and sessions in about a third of the slots.
func writeSyntheticHistory(tb testing.TB, dir string, now time.Time) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		tb.Fatal(err)
	}
	accounts := []string{"a.json", "b.json", "c.json"}
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	end := now.UTC().Truncate(history.SlotLength)
	for t := end.Add(-14 * 24 * time.Hour); t.Before(end); t = t.Add(history.SlotLength) {
		day := t.Format("2006-01-02")
		f := files[day]
		if f == nil {
			var err error
			if f, err = os.Create(filepath.Join(dir, day+".jsonl")); err != nil {
				tb.Fatal(err)
			}
			files[day] = f
		}
		slot := history.Slot{T: t, Active: map[string]string{"main": "a.json"}, Accounts: map[string]*history.AccountSlot{}}
		level := float64(t.Unix()%100000) / 1000
		for i, a := range accounts {
			l := level + float64(i)
			slot.Accounts[a] = &history.AccountSlot{Levels: history.Levels{Main: &l, Session: &l},
				Models: map[string]history.Traffic{"m": {Requests: 3, InputTokens: 900, OutputTokens: 300, CacheReadTokens: 40000}, "n": {Requests: 1, InputTokens: 50}}}
		}
		if n := int(t.Unix()/300) % 3; n == 0 {
			slot.Sessions = map[string]*history.SessionSlot{}
			for s := 0; s < 1+int(t.Unix()/300)%7; s++ {
				id := fmt.Sprintf("session-%d-%d", t.Unix()/86400, s)
				slot.Sessions[id] = &history.SessionSlot{First: t, Last: t, Accounts: map[string]history.Traffic{accounts[s%3]: {Requests: 2, InputTokens: 500}},
					Models: map[string]history.Traffic{"m": {Requests: 2, InputTokens: 500}}}
			}
		}
		line, err := json.Marshal(slot)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			tb.Fatal(err)
		}
	}
}

func benchSetup(tb testing.TB) {
	tb.Helper()
	dir := tb.TempDir()
	writeSyntheticHistory(tb, filepath.Join(dir, "history"), time.Now())
	previous := recorder
	recorder = history.NewRecorder(filepath.Join(dir, "history"))
	tb.Cleanup(func() { recorder = previous })
	req, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(fmt.Sprintf(
		"state_path: %s/state.json\naccounts: [{auth_file: a.json}, {auth_file: b.json}, {auth_file: c.json}]\nbuckets: {main: [m]}\n", dir))})
	configure(req)
	tb.Cleanup(shutdown)
}

// BenchmarkStatusBuild is what one status costs without the cache: the build every caller paid.
func BenchmarkStatusBuild(b *testing.B) {
	benchSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := buildStatus(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStatusCached is a status the cache answers.
func BenchmarkStatusCached(b *testing.B) {
	benchSetup(b)
	pluginAPI{}.Status()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pluginAPI{}.Status()
	}
}

// BenchmarkHUDBurst is many sessions' HUDs asking at the same moment: every one built its own
// status before the cache; now one build answers all of them.
func BenchmarkHUDBurst(b *testing.B) {
	const sessions = 40
	for name, status := range map[string]func(){
		"uncached": func() { _, _ = buildStatus() },
		"cached":   func() { pluginAPI{}.Status() },
	} {
		b.Run(name, func(b *testing.B) {
			benchSetup(b)
			for i := 0; i < b.N; i++ {
				invalidateViews() // a cold cache: the worst case, the first burst after a change
				var wg sync.WaitGroup
				for s := 0; s < sessions; s++ {
					wg.Add(1)
					go func() { defer wg.Done(); status() }()
				}
				wg.Wait()
			}
		})
	}
}
