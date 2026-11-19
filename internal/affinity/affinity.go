// Package affinity finds which project a Claude Code session belongs to. It reads names only: the
// headers of a request, the names in Claude Code's projects directory, and a folder's small git
// files (see Repos), never a transcript.
package affinity

import (
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

const (
	sessionHeader = "X-Claude-Code-Session-Id"
	agentHeader   = "X-Claude-Code-Agent-Id"
	mainAgent     = "main"

	// indexTTL is how old the projects index may be. A new session's transcript file appears after
	// its first request, and its project home must apply within seconds; a session resumed from
	// another directory must move to its current folder soon. A pass over about 400 folders and 1000
	// transcripts costs about 10 to 20 ms on a warm cache (see BenchmarkFolderForAWeekOfSessions), so
	// one pass every 5 s is well under 1 % of one core, while an answer is never more than 5 s old.
	indexTTL = 5 * time.Second
	maxName  = 255 // the longest file name
)

// header is the first non-empty value of a header, whatever the case of its name.
func header(h map[string][]string, name string) string {
	name = textproto.CanonicalMIMEHeaderKey(name)
	for k, values := range h {
		if textproto.CanonicalMIMEHeaderKey(k) != name {
			continue
		}
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

// ValidSID reports whether s can be one name in a path and one part of a session key: plain
// characters, no separator, no colon, not hidden.
func ValidSID(s string) bool {
	return history.PlainID(s) && !strings.ContainsAny(s, "/:") && !strings.HasPrefix(s, ".")
}

// SessionID is the session of a request in the history's key form, and its root session: the main
// session `<sid>`, or a subagent `<sid>/<agentId>` whose root is `<sid>`. CPA hands the plugin the
// client's headers (CPA's own `claude:<sid>` ids are made later, after the scheduler picks), so the
// key is made here, through the same CanonicalSession the usage records go through. A request
// without a usable session gives "", "". Only the root session reaches a path, so an agent id that
// is not usable (or makes the key too long) leaves the request as the main session: it keeps its
// project.
func SessionID(h map[string][]string) (id, root string) {
	sid := header(h, sessionHeader)
	if sid == "" || !ValidSID(sid) {
		return "", ""
	}
	if agent := header(h, agentHeader); agent != "" && agent != mainAgent && ValidSID(agent) {
		if id, _ = history.CanonicalSession("claude:"+sid+":agent:"+agent, ""); history.PlainID(id) {
			return id, sid
		}
	}
	return sid, sid
}

// ValidProject reports whether name can be a project: one file name of at most 255 bytes, valid
// UTF-8, without control characters. Folder names are user data, so they are checked before they
// are stored or shown.
func ValidProject(name string) bool {
	if name == "" || len(name) > maxName || name == "." || name == ".." || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r == '/' || r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// Projects finds a session's project folder: the directory under the projects directory that holds
// `<sid>.jsonl`. When a session was resumed from another directory, several folders hold one, and
// the folder whose file was written last is the current one (the first by name when two have the
// same time). It only lists directory names and reads file metadata; it never opens a transcript.
//
// The answers come from one index of the whole projects directory, `session -> folder`, built in a
// single pass over every folder's entries and rebuilt when it is older than indexTTL. A lookup is a
// map read, so a page that asks for hundreds of sessions costs one pass, not one per session. The
// index holds one entry per session that has a transcript file, so its size is bounded by the
// number of transcript files on disk; a lookup for an unknown session adds nothing to it.
type Projects struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	index    map[string]indexed // nil until the first pass ends
	builtAt  time.Time          // when the pass that made index began
	building chan struct{}      // open while a pass runs; closed when it ends

	passes atomic.Int64 // passes started, for the tests
}

// indexed is the folder that holds a session's newest transcript file, and that file's time.
type indexed struct {
	folder string
	at     time.Time
}

func NewProjects(dir string, now func() time.Time) *Projects {
	return &Projects{dir: dir, now: now}
}

// Folder is the project folder of a root session, found by name. An unknown session has none. The
// answer is as old as the index (at most indexTTL, plus the length of a pass), so a session resumed
// from another folder moves to its current folder within that time, and a new session, whose
// transcript is written after its first request, has its project within that time too.
func (p *Projects) Folder(sid string) (string, bool) {
	if p.dir == "" || !ValidSID(sid) {
		return "", false
	}
	e, ok := p.current()[sid]
	return e.folder, ok
}

// current is the index, rebuilt first when it is too old. At most one pass runs at a time. While a
// pass runs, the other callers use the index they have: an answer a few seconds old is what they
// would get one moment earlier anyway, and none of them waits for the directory reads. Only the
// very first caller has no index, so the others wait for that first pass. The lock is never held
// during the directory reads.
func (p *Projects) current() map[string]indexed {
	now := p.now()
	p.mu.Lock()
	if p.index != nil && now.Sub(p.builtAt) < indexTTL {
		defer p.mu.Unlock()
		return p.index
	}
	if running := p.building; running != nil {
		stale := p.index
		p.mu.Unlock()
		if stale != nil {
			return stale
		}
		<-running
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.index
	}
	done := make(chan struct{})
	p.building = done
	p.mu.Unlock()

	fresh, ok := p.build()

	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case ok:
		p.index = fresh
	case p.index == nil:
		p.index = map[string]indexed{} // nothing is known yet; try again after indexTTL
	} // else an unreadable directory keeps the earlier index
	p.builtAt = now
	p.building = nil
	close(done)
	return p.index
}

// build reads the entries of every project folder once and maps each session to the folder whose
// `<sid>.jsonl` was modified last (the first by name when two have the same time). It reports false
// when the projects directory cannot be read.
func (p *Projects) build() (map[string]indexed, bool) {
	p.passes.Add(1)
	folders, err := os.ReadDir(p.dir) // sorted by name, so the first of equal times wins below
	if err != nil {
		return nil, false
	}
	index := map[string]indexed{}
	for _, f := range folders {
		if !f.IsDir() || !ValidProject(f.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(p.dir, f.Name()))
		if err != nil {
			continue
		}
		for _, e := range files {
			session, ok := strings.CutSuffix(e.Name(), ".jsonl")
			if !ok || !ValidSID(session) || !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if prev, seen := index[session]; !seen || info.ModTime().After(prev.at) {
				index[session] = indexed{f.Name(), info.ModTime()}
			}
		}
	}
	return index, true
}

// size is the number of sessions in the index, for the tests.
func (p *Projects) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.index)
}
