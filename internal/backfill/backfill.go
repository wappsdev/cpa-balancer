package backfill

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

// liveMargin keeps the import out of the slots the plugin is still filling.
const liveMargin = 10 * time.Minute

type Options struct {
	Projects  string    // the directory holding one directory per project
	History   string    // the balancer's history directory
	FableMark string    // a model whose name holds this is in the Fable bucket, the others in main
	DryRun    bool      // count only: write nothing
	Now       time.Time // zero means the clock
}

// Day is what an import adds to one day.
type Day struct {
	Date     string
	Sessions int
	Requests int64
	Tokens   int64
	Advisor  int64 // advisor requests
}

type Report struct {
	DryRun       bool
	Files        int
	SkippedLines int // transcript lines that were broken or past MaxLine
	Messages     int // distinct messages in range
	Sessions     int
	SlotsFound   int // 5-minute slots the messages fall into
	SlotsLive    int // of those, slots the history already covers (the plugin wrote one at that time)
	SlotsWould   int // slots a dry run would write
	SlotsAdded   int // slots written
	Days         []Day
}

// Run reads the transcripts and writes the sessions they hold into the history, in the slots no
// slot of the history covers yet: the plugin writes one at every tick while it runs, so this is
// the time before it ran and the gaps when it was off. Running it again adds nothing.
func Run(o Options) (Report, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.FableMark == "" {
		o.FableMark = "fable"
	}
	rep := Report{DryRun: o.DryRun}
	earliest, latest := o.Now.Add(-history.Retention), o.Now.Add(-liveMargin)

	// One message can span several lines, one per content block, each with the usage so far: the
	// one with the largest output is the finished one. A message id is counted once, whichever
	// transcripts hold it (a resumed or forked session copies earlier messages); the session of
	// the copy with the largest output, then the smaller session id, keeps it. A line with no id
	// is its own message.
	messages := map[string]message{}
	err := walk(o.Projects, func(path, session string) error {
		rep.Files++
		return readFile(path, session, func(m message, lineNo int) {
			if m.At.Before(earliest) || !m.At.Before(latest) {
				return
			}
			key := m.ID
			if key == "" {
				m.ID = fmt.Sprintf("%s:%d", path, lineNo)
				key = m.Session + "\x00" + m.ID
			}
			if old, ok := messages[key]; !ok || m.Usage.OutputTokens > old.Usage.OutputTokens ||
				(m.Usage.OutputTokens == old.Usage.OutputTokens && m.Session < old.Session) {
				messages[key] = m
			}
		}, &rep.SkippedLines)
	})
	if err != nil {
		return rep, err
	}
	rep.Messages = len(messages)

	store := history.NewRecorder(o.History)
	timeline := newTimeline(store.Slots(earliest.Add(-history.SlotLength), o.Now))
	ordered := make([]message, 0, len(messages))
	sessions := map[string]bool{}
	for _, m := range messages {
		ordered = append(ordered, m)
		sessions[m.Session] = true
	}
	rep.Sessions = len(sessions)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.Session+a.ID < b.Session+b.ID
	})
	slots := map[time.Time]*history.Slot{}
	for _, m := range ordered {
		start := m.At.Truncate(history.SlotLength)
		slot, ok := slots[start]
		if !ok {
			slot = history.NewSlot(start)
			slots[start] = slot
		}
		bucket := "main"
		if strings.Contains(m.Model, o.FableMark) {
			bucket = "fable"
		}
		slot.AddTraffic(m.At, history.Source{Account: timeline.at(bucket, m.At), Model: m.Model, Session: m.Session}, m.Usage.traffic())
		for _, a := range m.Advisor {
			slot.AddAdvisor(m.At, m.Session, "", a.Model, a.Usage.traffic())
		}
	}
	all := make([]*history.Slot, 0, len(slots))
	for _, s := range slots {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].T.Before(all[j].T) })
	todo := store.Untaken(all)
	rep.SlotsFound, rep.SlotsLive = len(all), len(all)-len(todo)
	rep.Days = days(todo)
	if o.DryRun {
		rep.SlotsWould = len(todo)
		return rep, nil
	}
	rep.SlotsAdded, err = store.AppendBackfill(todo)
	return rep, err
}

// days totals what the slots add, per UTC day.
func days(slots []*history.Slot) []Day {
	byDate := map[string]*Day{}
	seen := map[string]map[string]bool{}
	for _, s := range slots {
		date := s.T.Format("2006-01-02")
		d, ok := byDate[date]
		if !ok {
			d = &Day{Date: date}
			byDate[date] = d
			seen[date] = map[string]bool{}
		}
		for _, a := range s.Accounts {
			for _, t := range a.Models {
				d.Requests += t.Requests
				d.Tokens += t.Tokens()
			}
		}
		for id, ss := range s.Sessions {
			seen[date][id] = true
			for _, t := range ss.Advisor {
				d.Advisor += t.Requests
			}
		}
	}
	out := make([]Day, 0, len(byDate))
	for date, d := range byDate {
		d.Sessions = len(seen[date])
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// Write prints the report. It holds counts and dates only: no session id, path, model or text.
func (r Report) Write(w io.Writer) {
	mode := "written"
	if r.DryRun {
		mode = "dry run, nothing written"
	}
	fmt.Fprintf(w, "Transcript import (%s)\n", mode)
	fmt.Fprintf(w, "  files read:       %d\n", r.Files)
	fmt.Fprintf(w, "  lines skipped:    %d (broken, or longer than %d MB)\n", r.SkippedLines, MaxLine>>20)
	fmt.Fprintf(w, "  messages:         %d in %d sessions\n", r.Messages, r.Sessions)
	adds := r.SlotsAdded
	if r.DryRun {
		adds = r.SlotsWould
	}
	fmt.Fprintf(w, "  5-minute slots:   %d found, %d already covered by the history (left as they are), %d to add\n", r.SlotsFound, r.SlotsLive, adds)
	fmt.Fprintf(w, "\n  %-10s  %8s  %9s  %12s  %8s\n", "day (UTC)", "sessions", "requests", "tokens", "advisor")
	for _, d := range r.Days {
		fmt.Fprintf(w, "  %-10s  %8d  %9d  %12d  %8d\n", d.Date, d.Sessions, d.Requests, d.Tokens, d.Advisor)
	}
	fmt.Fprintln(w, "\n  tokens = input + output + cache writes; advisor = requests an advisor model answered")
}
