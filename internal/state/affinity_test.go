package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/affinity"
	"github.com/wappsdev/cpa-balancer/internal/config"
)

func TestHomesAndPinsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := New()
	s.SetHome("-work-alpha", "a.json", now)
	if err := s.SetPin(PinFolder, "-work-beta", "b.json"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(PinSession, "11111111-2222-3333-4444-555555555555", "a.json"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if h := got.Affinity.Homes["-work-alpha"]; h == nil || h.Account != "a.json" || !h.Since.Equal(now) || !h.Last.Equal(now) {
		t.Fatalf("home: %+v", h)
	}
	if got.Affinity.FolderPins["-work-beta"] != "b.json" || got.Affinity.SessionPins["11111111-2222-3333-4444-555555555555"] != "a.json" {
		t.Fatalf("pins: %+v", got.Affinity)
	}
}

func TestWorktreeLinksSurviveARestartAndNormalizeDropsWhatTheBoundsForbid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := New()
	s.Affinity.Links = []affinity.Link{{Folder: "-x-w-1", Project: "-x-repo"}, {Folder: "a/b", Project: "-x-repo"}, {Folder: "-x-w-2", Project: ""}}
	for i := 0; i < affinity.MaxLinks; i++ {
		s.Affinity.Links = append(s.Affinity.Links, affinity.Link{Folder: fmt.Sprintf("-x-n-%d", i), Project: "-x-repo"})
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	links := got.Affinity.Links
	if len(links) != affinity.MaxLinks || links[len(links)-1].Folder != fmt.Sprintf("-x-n-%d", affinity.MaxLinks-1) {
		t.Fatalf("invalid links dropped, the oldest ones past the bound too: %d", len(links))
	}
	if links[0].Folder == "-x-w-1" || links[0].Folder == "a/b" {
		t.Fatalf("%+v", links[0])
	}
}

func TestMovingAHomeKeepsItsOriginalSinceOnlyWhenTheAccountIsTheSame(t *testing.T) {
	s := New()
	s.SetHome("-work-alpha", "a.json", now)
	s.SetHome("-work-alpha", "a.json", now.Add(time.Hour))
	if h := s.Affinity.Homes["-work-alpha"]; !h.Since.Equal(now) || !h.Last.Equal(now.Add(time.Hour)) {
		t.Fatalf("same account: %+v", h)
	}
	s.SetHome("-work-alpha", "b.json", now.Add(2*time.Hour))
	if h := s.Affinity.Homes["-work-alpha"]; h.Account != "b.json" || !h.Since.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("moved: %+v", h)
	}
}

func TestTheHomesAreBoundedAndTheLeastRecentlyUsedGoFirst(t *testing.T) {
	s := New()
	for i := 0; i < MaxHomes; i++ {
		s.SetHome(fmt.Sprintf("-work-%d", i), "a.json", now.Add(time.Duration(i)*time.Minute))
	}
	s.TouchHome("-work-0", now.Add(time.Hour*10)) // in use again
	s.SetHome("-work-new", "b.json", now.Add(time.Hour*11))
	if len(s.Affinity.Homes) != MaxHomes {
		t.Fatalf("homes: %d", len(s.Affinity.Homes))
	}
	if _, ok := s.Affinity.Homes["-work-1"]; ok {
		t.Fatal("the least recently used home is dropped")
	}
	for _, kept := range []string{"-work-0", "-work-new"} {
		if _, ok := s.Affinity.Homes[kept]; !ok {
			t.Fatalf("%s must be kept", kept)
		}
	}
}

func TestPinsAreBoundedAndRefuseRatherThanEvict(t *testing.T) {
	s := New()
	for i := 0; i < config.MaxFolderPins; i++ {
		if err := s.SetPin(PinFolder, fmt.Sprintf("-work-%d", i), "a.json"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPin(PinFolder, "-work-extra", "a.json"); err == nil {
		t.Fatal("a pin past the limit is refused")
	}
	if err := s.SetPin(PinFolder, "-work-3", "b.json"); err != nil || s.Affinity.FolderPins["-work-3"] != "b.json" {
		t.Fatal("changing an existing pin is not a new pin")
	}
	for i := 0; i < config.MaxSessionPins; i++ {
		if err := s.SetPin(PinSession, fmt.Sprintf("s%d", i), "a.json"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPin(PinSession, "s-extra", "a.json"); err == nil {
		t.Fatal("a session pin past the limit is refused")
	}
}

func TestPinsAreCheckedBeforeTheyAreStored(t *testing.T) {
	s := New()
	for name, f := range map[string]func() error{
		"a folder that is a path":   func() error { return s.SetPin(PinFolder, "a/b", "a.json") },
		"an empty folder":           func() error { return s.SetPin(PinFolder, "", "a.json") },
		"a session that is not one": func() error { return s.SetPin(PinSession, "a b", "a.json") },
		"a subagent id":             func() error { return s.SetPin(PinSession, "x/y", "a.json") },
		"an empty account":          func() error { return s.SetPin(PinSession, "s1", "") },
		"an unknown kind":           func() error { return s.SetPin("other", "s1", "a.json") },
	} {
		if f() == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if len(s.Affinity.FolderPins)+len(s.Affinity.SessionPins) != 0 {
		t.Fatalf("nothing is stored: %+v", s.Affinity)
	}
}

func TestClearPinRemovesIt(t *testing.T) {
	s := New()
	_ = s.SetPin(PinFolder, "-work-alpha", "a.json")
	s.ClearPin(PinFolder, "-work-alpha")
	s.ClearPin(PinFolder, "-work-absent") // no error for a pin that is not there
	if len(s.Affinity.FolderPins) != 0 {
		t.Fatalf("%+v", s.Affinity)
	}
}

func TestLoadDropsWhatTheBoundsForbid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	homes := map[string]any{"a/b": map[string]any{"account": "a.json"}, "": map[string]any{"account": "a.json"}}
	for i := 0; i < MaxHomes+20; i++ {
		homes[fmt.Sprintf("-work-%d", i)] = map[string]any{"account": "a.json", "last": now.Add(time.Duration(i) * time.Minute)}
	}
	pins := map[string]string{"a/b": "a.json"}
	for i := 0; i < config.MaxFolderPins+20; i++ {
		pins[fmt.Sprintf("-work-%d", i)] = "a.json"
	}
	raw, _ := json.Marshal(map[string]any{"affinity": map[string]any{"homes": homes, "folder_pins": pins, "session_pins": map[string]string{"bad id": "a.json"}}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Affinity.Homes) != MaxHomes || len(s.Affinity.FolderPins) != config.MaxFolderPins || len(s.Affinity.SessionPins) != 0 {
		t.Fatalf("homes %d, folder pins %d, session pins %d", len(s.Affinity.Homes), len(s.Affinity.FolderPins), len(s.Affinity.SessionPins))
	}
	if _, ok := s.Affinity.Homes["a/b"]; ok {
		t.Fatal("a folder name that is a path is dropped")
	}
	if _, ok := s.Affinity.Homes[fmt.Sprintf("-work-%d", MaxHomes+19)]; !ok {
		t.Fatal("the most recently used homes are the ones kept")
	}
}
