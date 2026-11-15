// Package backfill rebuilds per-session history from Claude Code transcripts.
//
// A transcript line is decoded into the fields below and nothing else: the message text is never
// kept, printed or stored.
package backfill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/lines"
)

// MaxLine is the longest transcript line that is read. A line past it holds something large, such
// as a pasted file or an image, and a usage line is far smaller.
const MaxLine = 16 << 20

// usageFields are the token counts of a message or of one of its iterations.
type usageFields struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
}

func (u usageFields) traffic() history.Traffic {
	return history.Traffic{Requests: 1, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens, CacheCreationTokens: u.CacheCreationTokens}
}

func (u usageFields) zero() bool { return u == usageFields{} }

type iteration struct {
	Type  string `json:"type"`
	Model string `json:"model"`
	usageFields
}

// transcriptLine holds every field of a transcript line the import reads.
type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			usageFields
			Iterations []iteration `json:"iterations"`
		} `json:"usage"`
	} `json:"message"`
}

// advisorCall is one request an advisor model answered inside a message.
type advisorCall struct {
	Model string
	Usage usageFields
}

// message is one assistant message: a request.
type message struct {
	Session string // the top-level session the transcript file belongs to
	ID      string
	At      time.Time
	Model   string
	Usage   usageFields
	Advisor []advisorCall
}

// decode reads one line; ok is false for anything that is not a usable assistant message.
func decode(raw []byte, session string) (m message, ok bool, err error) {
	var l transcriptLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return message{}, false, err
	}
	if l.Type != "assistant" {
		return message{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		return message{}, false, nil
	}
	m = message{Session: session, ID: l.Message.ID, At: at.UTC(), Model: l.Message.Model, Usage: l.Message.Usage.usageFields}
	for _, it := range l.Message.Usage.Iterations {
		// The top-level usage already holds the executor's own sub-calls ("message"); an advisor's are
		// on top of it.
		if it.Type == "advisor_message" && !it.zero() {
			m.Advisor = append(m.Advisor, advisorCall{Model: it.Model, Usage: it.usageFields})
		}
	}
	if m.Usage.zero() && len(m.Advisor) == 0 {
		return message{}, false, nil // an error placeholder or a message that used nothing
	}
	return m, true, nil
}

// readFile decodes every assistant message of one transcript file.
func readFile(path, session string, each func(message, int), skipped *int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n := 0
	bad, err := lines.Each(f, MaxLine, func(raw []byte) {
		n++
		m, ok, derr := decode(raw, session)
		if derr != nil {
			*skipped++
		} else if ok {
			each(m, n)
		}
	})
	*skipped += bad
	return err
}

// walk calls fn for every main transcript (session = its file name) and every subagent transcript
// (session = the directory of the session it belongs to). Links are not followed.
func walk(projects string, fn func(path, session string) error) error {
	dirs, err := os.ReadDir(projects)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		project := filepath.Join(projects, d.Name())
		entries, err := os.ReadDir(project)
		if err != nil {
			return err
		}
		for _, e := range entries {
			switch {
			case e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl"):
				if err := fn(filepath.Join(project, e.Name()), strings.TrimSuffix(e.Name(), ".jsonl")); err != nil {
					return err
				}
			case e.IsDir():
				subs, err := os.ReadDir(filepath.Join(project, e.Name(), "subagents"))
				if err != nil {
					continue // most sessions have no subagents
				}
				for _, s := range subs {
					if s.Type().IsRegular() && strings.HasSuffix(s.Name(), ".jsonl") {
						if err := fn(filepath.Join(project, e.Name(), "subagents", s.Name()), e.Name()); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}
