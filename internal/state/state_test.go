package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/policy"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func TestMissingFileGivesEmptyState(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(s.Accounts) != 0 || s.Active == nil {
		t.Fatal(s, err)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	reset := now.Add(5 * time.Hour)
	s := New()
	s.Active[quota.Main] = policy.Active{Account: "claude-g.json", Since: now}
	a := s.Account("claude-w.json")
	a.Probe = quota.Readings{quota.Session: {ObservedAt: now, Source: "probe"}}
	a.LastAnchor, a.AnchorMainModel = now, true
	a.AnchorReading = &quota.Reading{ResetsAt: &reset, ObservedAt: now, Source: "anchor"}
	s.AddChange(Change{At: now, Bucket: quota.Main, From: "", To: "claude-g.json", Reason: "earliest reset"})
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", got, s)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions %v", info.Mode().Perm())
	}
}

func TestChangesAreCapped(t *testing.T) {
	s := New()
	for i := range MaxChanges + 5 {
		s.AddChange(Change{Reason: string(rune('a' + i%26))})
	}
	if len(s.Changes) != MaxChanges {
		t.Fatal(len(s.Changes))
	}
}
