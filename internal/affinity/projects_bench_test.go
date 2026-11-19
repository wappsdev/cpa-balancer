package affinity

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	weekFolders  = 400
	weekSessions = 1000
)

// weekDir builds a projects directory the size of a busy week: weekFolders project folders and
// weekSessions transcripts spread over them. Every tenth session was also resumed from a second
// folder, so it has two files.
func weekDir(tb testing.TB) (dir string, sids []string) {
	tb.Helper()
	dir = tb.TempDir()
	for f := 0; f < weekFolders; f++ {
		if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("-work-project-%03d", f)), 0o700); err != nil {
			tb.Fatal(err)
		}
	}
	write := func(folder int, session string) {
		path := filepath.Join(dir, fmt.Sprintf("-work-project-%03d", folder), session+".jsonl")
		if err := os.WriteFile(path, []byte("not read\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 0; i < weekSessions; i++ {
		session := fmt.Sprintf("%08d-2222-3333-4444-555555555555", i)
		sids = append(sids, session)
		write(i%weekFolders, session)
		if i%10 == 0 {
			write((i+7)%weekFolders, session)
		}
	}
	return dir, sids
}

// BenchmarkFolderForAWeekOfSessions is the first status page after a start: one lookup for each
// root session of the week, on a cold Projects.
func BenchmarkFolderForAWeekOfSessions(b *testing.B) {
	dir, sids := weekDir(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := NewProjects(dir, time.Now)
		for _, s := range sids {
			if _, ok := p.Folder(s); !ok {
				b.Fatal("session not found")
			}
		}
	}
}
