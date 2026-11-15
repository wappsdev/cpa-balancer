// Package affinity finds which project a Claude Code session belongs to. It reads names only:
// the headers of a request and the names in Claude Code's projects directory, never a transcript.
package affinity

import (
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wappsdev/cpa-balancer/internal/history"
)

const (
	sessionHeader = "X-Claude-Code-Session-Id"
	agentHeader   = "X-Claude-Code-Agent-Id"
	mainAgent     = "main"

	missTTL   = 30 * time.Second // a session's transcript file appears after its first request
	maxCached = 1000             // sessions remembered, hits and misses each
	maxName   = 255              // the longest file name
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
// without a usable session gives "", "".
func SessionID(h map[string][]string) (id, root string) {
	sid := header(h, sessionHeader)
	if sid == "" {
		return "", ""
	}
	raw := "claude:" + sid
	if agent := header(h, agentHeader); agent != "" && agent != mainAgent {
		raw += ":agent:" + agent
		if !ValidSID(agent) {
			return "", ""
		}
	}
	if !ValidSID(sid) {
		return "", ""
	}
	id, _ = history.CanonicalSession(raw, "")
	return id, sid
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
// `<sid>.jsonl`. It only lists directory names and checks that a file exists; it never opens one.
type Projects struct {
	dir string
	now func() time.Time

	mu     sync.Mutex
	found  map[string]string    // session -> folder
	missed map[string]time.Time // session -> when it was not found
}

func NewProjects(dir string, now func() time.Time) *Projects {
	return &Projects{dir: dir, now: now, found: map[string]string{}, missed: map[string]time.Time{}}
}

// Folder is the project folder of a root session, found by name. An unknown session has none. A
// hit is kept; a miss is kept for a short while only, as the transcript is written after the
// session's first request.
func (p *Projects) Folder(sid string) (string, bool) {
	if p.dir == "" || !ValidSID(sid) {
		return "", false
	}
	now := p.now()
	p.mu.Lock()
	if folder, ok := p.found[sid]; ok {
		p.mu.Unlock()
		return folder, true
	}
	if at, ok := p.missed[sid]; ok && now.Sub(at) < missTTL {
		p.mu.Unlock()
		return "", false
	}
	p.mu.Unlock()

	folder, ok := p.scan(sid) // directory reads happen outside the lock
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		delete(p.missed, sid)
		p.found[sid] = folder
	} else {
		p.missed[sid] = now
	}
	if len(p.found) > maxCached {
		clear(p.found)
	}
	if len(p.missed) > maxCached {
		clear(p.missed)
	}
	return folder, ok
}

func (p *Projects) scan(sid string) (string, bool) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidProject(e.Name()) {
			continue
		}
		if info, err := os.Stat(filepath.Join(p.dir, e.Name(), sid+".jsonl")); err == nil && info.Mode().IsRegular() {
			return e.Name(), true
		}
	}
	return "", false
}

// size is the number of sessions remembered, for the tests.
func (p *Projects) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.found) + len(p.missed)
}
