// Package state persists what the engine must remember across restarts.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/policy"
	"github.com/adnankurt/cpa-balancer/internal/quota"
)

const MaxChanges = 50

type Account struct {
	Headers          quota.Readings `json:"headers,omitempty"` // latest rate-limit headers from real traffic
	Probe            quota.Readings `json:"probe,omitempty"`
	LastProbe        time.Time      `json:"last_probe,omitzero"`
	RetryAfter       time.Time      `json:"retry_after,omitzero"`
	LastAnchor       time.Time      `json:"last_anchor,omitzero"`
	AnchorRetryAfter time.Time      `json:"anchor_retry_after,omitzero"`
	AnchorMainModel  bool           `json:"anchor_main_model,omitempty"`
	AnchorReading    *quota.Reading `json:"anchor_reading,omitempty"`
}

type Change struct {
	At     time.Time    `json:"at"`
	Bucket quota.Bucket `json:"bucket"`
	From   string       `json:"from"`
	To     string       `json:"to"`
	Reason string       `json:"reason"`
}

type State struct {
	Active   map[quota.Bucket]policy.Active `json:"active"`
	Accounts map[string]*Account            `json:"accounts"`
	Changes  []Change                       `json:"changes"`
}

func New() *State {
	return &State{Active: map[quota.Bucket]policy.Active{}, Accounts: map[string]*Account{}}
}

func Load(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	s := New()
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, err
	}
	if s.Active == nil {
		s.Active = map[quota.Bucket]policy.Active{}
	}
	if s.Accounts == nil {
		s.Accounts = map[string]*Account{}
	}
	return s, nil
}

func (s *State) Save(path string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *State) Account(name string) *Account {
	a, ok := s.Accounts[name]
	if !ok {
		a = &Account{}
		s.Accounts[name] = a
	}
	return a
}

func (s *State) AddChange(c Change) {
	s.Changes = append(s.Changes, c)
	if len(s.Changes) > MaxChanges {
		s.Changes = s.Changes[len(s.Changes)-MaxChanges:]
	}
}
