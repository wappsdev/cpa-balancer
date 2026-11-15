package backfill

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/transcripts"
)

// ErrNoMessages is Run's failure when the projects directory held no usable message. The store
// is not touched then: a wrong or emptied source must not wipe the history it keeps.
var ErrNoMessages = errors.New("no transcript messages found in the projects directory")

type Options struct {
	Projects string    // the directory holding one directory per project
	Store    string    // the transcript store's directory
	DryRun   bool      // count only: write nothing
	Now      time.Time // zero means the clock
}

// Day is what the store holds for one UTC day.
type Day struct {
	Date     string
	Sessions int
	Requests int64
	Tokens   int64
	Advisor  int64 // advisor requests
}

type Report struct {
	DryRun        bool
	MainFiles     int // main transcripts read
	SubagentFiles int // subagent transcripts read
	NestedFiles   int // of those, the ones below a directory of the subagents directory
	SkippedLines  int // transcript lines that were broken or past MaxLine
	Messages      int // distinct messages in range
	Titled        int // sessions in the store that have a name
	Sessions      int // distinct sessions, a subagent counted with its parent
	Rows          int // rows the store holds (written, or a dry run's)
	Days          []Day
}

// Run reads the transcripts and brings the store up to date with what they say (see
// transcripts.Write): the days from the earliest message to today are replaced, older days stay,
// so the history outlives Claude Code's deletion of old transcripts. Running it again changes
// nothing. It fails with ErrNoMessages, and writes nothing, when it finds no message.
func Run(o Options) (Report, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	rep := Report{DryRun: o.DryRun}
	earliest := o.Now.Add(-history.Retention)

	// One message can span several lines, one per content block, each with the usage so far: the
	// one with the largest output is the finished one. A message id is counted once, whichever
	// transcripts hold it (a resumed or forked session copies earlier messages); the transcript of
	// the copy with the largest output, then the smaller session key, owns it. A line with no id is
	// its own message.
	messages := map[string]message{}
	// A session's names: the last line of each kind wins. Only a main transcript names its own
	// session; a line that says it belongs to another session was copied in, and a subagent's
	// file holds the agent's names, not the session's.
	names := map[string]*[3]string{}
	err := walk(o.Projects, func(f file) error {
		switch {
		case f.Subagent:
			rep.SubagentFiles++
			if f.Nested {
				rep.NestedFiles++
			}
		default:
			rep.MainFiles++
		}
		return readFile(f.Path, f.Owner, func(rec record, lineNo int) {
			if t := rec.Title; t != nil {
				if !f.Subagent && (t.Session == "" || t.Session == f.Owner) {
					n := names[f.Owner]
					if n == nil {
						n = &[3]string{}
						names[f.Owner] = n
					}
					n[map[string]int{transcripts.KindCustom: 0, transcripts.KindAgent: 1, transcripts.KindAI: 2}[t.Kind]] = transcripts.CleanTitle(t.Text)
				}
				return
			}
			m := *rec.Message
			if m.At.Before(earliest) || !m.At.Before(o.Now) {
				return
			}
			key := m.ID
			if key == "" {
				key = fmt.Sprintf("%s\x00%s:%d", m.Owner, f.Path, lineNo)
			}
			if old, ok := messages[key]; !ok || m.Usage.OutputTokens > old.Usage.OutputTokens ||
				(m.Usage.OutputTokens == old.Usage.OutputTokens && m.Owner < old.Owner) {
				messages[key] = m
			}
		}, &rep.SkippedLines)
	})
	if err != nil {
		return rep, err
	}
	rep.Messages = len(messages)
	if rep.Messages == 0 {
		return rep, ErrNoMessages
	}

	rows := map[transcripts.Key]history.Traffic{}
	add := func(m message, model string, advisor bool, u usageFields) {
		k := transcripts.Key{Hour: m.At.Truncate(time.Hour), Session: history.CleanID(m.Owner), Model: history.CleanModel(model), Advisor: advisor}
		t := rows[k]
		t.Add(u.traffic())
		rows[k] = t
	}
	for _, m := range messages {
		if !m.Usage.zero() {
			add(m, m.Model, false, m.Usage)
		}
		for _, a := range m.Advisor {
			add(m, a.Model, true, a.Usage)
		}
	}
	rep.Rows = len(rows)
	rep.Sessions, rep.Days = summarize(rows)
	titles := titlesOf(names, rows)
	rep.Titled = len(titles)
	if o.DryRun {
		return rep, nil
	}
	return rep, transcripts.Write(o.Store, rows, titles, o.Now)
}

// titlesOf picks each session's name, the most deliberate kind that has one, for the sessions the
// rows hold. The names are already clean. A session whose id is not a plain one is stored as
// (other) with every such session, so no name can belong to it.
func titlesOf(names map[string]*[3]string, rows map[transcripts.Key]history.Traffic) map[string]transcripts.Title {
	present := map[string]bool{}
	for k := range rows {
		present[rootOf(k.Session)] = true
	}
	kinds := [3]string{transcripts.KindCustom, transcripts.KindAgent, transcripts.KindAI}
	out := map[string]transcripts.Title{}
	for session, n := range names {
		id := history.CleanID(session)
		if id == history.OtherSession {
			continue
		}
		for i, text := range n {
			if text = transcripts.CleanTitle(text); text != "" && present[id] {
				out[id] = transcripts.Title{Text: text, Kind: kinds[i]}
				break
			}
		}
	}
	return out
}

// summarize counts the rows' sessions (a subagent with its parent) and totals them per UTC day.
func summarize(rows map[transcripts.Key]history.Traffic) (int, []Day) {
	byDate := map[string]*Day{}
	seen := map[string]map[string]bool{}
	all := map[string]bool{}
	for k, t := range rows {
		date := k.Hour.Format("2006-01-02")
		d, ok := byDate[date]
		if !ok {
			d = &Day{Date: date}
			byDate[date] = d
			seen[date] = map[string]bool{}
		}
		session := rootOf(k.Session)
		seen[date][session], all[session] = true, true
		if k.Advisor {
			d.Advisor += t.Requests
		} else {
			d.Requests += t.Requests
			d.Tokens += t.Tokens()
		}
	}
	out := make([]Day, 0, len(byDate))
	for date, d := range byDate {
		d.Sessions = len(seen[date])
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return len(all), out
}

// Write prints the report. It holds counts and dates only: no session id, path, model or text.
func (r Report) Write(w io.Writer) {
	mode := "written"
	if r.DryRun {
		mode = "dry run, nothing written"
	}
	fmt.Fprintf(w, "Transcript import (%s)\n", mode)
	fmt.Fprintf(w, "  files read:       %d main, %d subagent (%d of them in a directory below the subagents directory)\n", r.MainFiles, r.SubagentFiles, r.NestedFiles)
	fmt.Fprintf(w, "  lines skipped:    %d (broken, or longer than %d MB)\n", r.SkippedLines, MaxLine>>20)
	fmt.Fprintf(w, "  messages:         %d in %d sessions\n", r.Messages, r.Sessions)
	fmt.Fprintf(w, "  store rows:       %d (a session, hour and model each)\n", r.Rows)
	fmt.Fprintf(w, "  named sessions:   %d\n", r.Titled)
	fmt.Fprintf(w, "\n  %-10s  %8s  %9s  %12s  %8s\n", "day (UTC)", "sessions", "requests", "tokens", "advisor")
	for _, d := range r.Days {
		fmt.Fprintf(w, "  %-10s  %8d  %9d  %12d  %8d\n", d.Date, d.Sessions, d.Requests, d.Tokens, d.Advisor)
	}
	fmt.Fprintln(w, "\n  tokens = input + output + cache writes; advisor = requests an advisor model answered")
	fmt.Fprintln(w, "  exact numbers from the transcripts: no account, no estimate")
}
