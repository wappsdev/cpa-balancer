// Package history records 5-minute slots of balancer data and reads them back downsampled.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	SlotLength = 5 * time.Minute
	Retention  = 90 * 24 * time.Hour
	dateLayout = "2006-01-02"
)

type Traffic struct {
	Requests        int64 `json:"requests"`
	Failed          int64 `json:"failed,omitempty"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
}

func (t *Traffic) add(o Traffic) {
	t.Requests += o.Requests
	t.Failed += o.Failed
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.CacheReadTokens += o.CacheReadTokens
}

// Levels are an account's quota values at the end of a slot; nil means unknown.
type Levels struct {
	Main        *float64 `json:"main,omitempty"`
	MainCap     *float64 `json:"main_cap,omitempty"`
	Fable       *float64 `json:"fable,omitempty"`
	FableCap    *float64 `json:"fable_cap,omitempty"`
	Session     *float64 `json:"session,omitempty"`
	SessionOpen bool     `json:"session_open,omitempty"`
}

type AccountSlot struct {
	Levels
	Models map[string]Traffic `json:"models,omitempty"`
}

type Event struct {
	At      time.Time `json:"t"`
	Kind    string    `json:"kind"` // switch | anchor | reset
	Bucket  string    `json:"bucket,omitempty"`
	Account string    `json:"account,omitempty"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Model   string    `json:"model,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

type Slot struct {
	T        time.Time               `json:"t"`
	Active   map[string]string       `json:"active,omitempty"`
	Accounts map[string]*AccountSlot `json:"accounts,omitempty"`
	Events   []Event                 `json:"events,omitempty"`
}

func newSlot(t time.Time) *Slot {
	return &Slot{T: t, Active: map[string]string{}, Accounts: map[string]*AccountSlot{}}
}

func (s *Slot) account(name string) *AccountSlot {
	a, ok := s.Accounts[name]
	if !ok {
		a = &AccountSlot{Models: map[string]Traffic{}}
		s.Accounts[name] = a
	}
	return a
}

// Recorder is shared by every engine instance, so a reconfiguration does not lose the open slot.
type Recorder struct {
	dir string

	mu         sync.Mutex
	open       *Slot
	pending    []*Slot // closed slots not yet written
	prunedDate string
}

func NewRecorder(dir string) *Recorder { return &Recorder{dir: dir} }

func slotStart(t time.Time) time.Time { return t.UTC().Truncate(SlotLength) }

// current returns the slot for now, closing the previous one; callers hold r.mu.
func (r *Recorder) current(now time.Time) *Slot {
	start := slotStart(now)
	if r.open == nil {
		r.open = newSlot(start)
	} else if start.After(r.open.T) {
		r.pending = append(r.pending, r.open)
		r.open = newSlot(start)
	}
	return r.open
}

func (r *Recorder) AddTraffic(now time.Time, account, model string, t Traffic) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.current(now).account(account)
	m := a.Models[model]
	m.add(t)
	a.Models[model] = m
}

func (r *Recorder) AddEvent(e Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.current(e.At)
	s.Events = append(s.Events, e)
}

// Snapshot stores the latest levels and active accounts; the last call in a slot wins.
func (r *Recorder) Snapshot(now time.Time, active map[string]string, levels map[string]Levels) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.current(now)
	for b, a := range active {
		s.Active[b] = a
	}
	for name, l := range levels {
		s.account(name).Levels = l
	}
}

// Flush writes closed slots and prunes old files once a day. Failed writes are retried next time.
func (r *Recorder) Flush(now time.Time) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current(now)
	if len(r.pending) > 0 {
		if err := os.MkdirAll(r.dir, 0o700); err != nil {
			return err
		}
		for len(r.pending) > 0 {
			if err := r.appendSlot(r.pending[0]); err != nil {
				return err
			}
			r.pending = r.pending[1:]
		}
	}
	today := now.UTC().Format(dateLayout)
	if r.prunedDate != today {
		r.prune(now)
		r.prunedDate = today
	}
	return nil
}

func (r *Recorder) appendSlot(s *Slot) error {
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(r.dir, s.T.Format(dateLayout)+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (r *Recorder) prune(now time.Time) {
	cutoff := now.UTC().Add(-Retention).Format(dateLayout)
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		date := strings.TrimSuffix(e.Name(), ".jsonl")
		if date != e.Name() && date < cutoff {
			os.Remove(filepath.Join(r.dir, e.Name()))
		}
	}
}

// slots returns stored and in-memory slots with T in [from, to).
func (r *Recorder) slots(from, to time.Time) []*Slot {
	r.mu.Lock()
	memory := append([]*Slot{}, r.pending...)
	if r.open != nil {
		memory = append(memory, r.open)
	}
	data, _ := json.Marshal(memory) // deep copy outside the lock
	r.mu.Unlock()
	var out []*Slot
	for day := from.UTC().Truncate(24 * time.Hour); day.Before(to); day = day.Add(24 * time.Hour) {
		f, err := os.Open(filepath.Join(r.dir, day.Format(dateLayout)+".jsonl"))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var s Slot
			if json.Unmarshal(scanner.Bytes(), &s) == nil && !s.T.Before(from) && s.T.Before(to) {
				out = append(out, &s)
			}
		}
		f.Close()
	}
	var copies []*Slot
	_ = json.Unmarshal(data, &copies)
	for _, s := range copies {
		if !s.T.Before(from) && s.T.Before(to) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}
