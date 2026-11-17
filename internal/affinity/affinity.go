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

	missTTL    = 5 * time.Second  // a new session's transcript file appears after its first request; short, so its project home applies within seconds
	recheckTTL = 60 * time.Second // a hit is looked up again after this long, so a session resumed from another directory moves to its current folder
	maxCached  = 1000             // sessions remembered, hits and misses each
	maxName    = 255              // the longest file name
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
// the folder whose file was written last is the current one. It only lists directory names and
// stats files; it never opens one.
type Projects struct {
	dir string
	now func() time.Time

	mu     sync.Mutex
	found  map[string]hit       // session -> its folder
	missed map[string]time.Time // session -> when it was not found
}

// hit is the folder a session was found in, and when that was looked up.
type hit struct {
	folder string
	at     time.Time
}

func NewProjects(dir string, now func() time.Time) *Projects {
	return &Projects{dir: dir, now: now, found: map[string]hit{}, missed: map[string]time.Time{}}
}

// Folder is the project folder of a root session, found by name. An unknown session has none. A
// hit is kept for recheckTTL, then looked up again (the session may have been resumed from another
// folder); a miss is kept for a short while only, as the transcript is written after the
// session's first request. A lookup that finds nothing keeps the earlier answer.
func (p *Projects) Folder(sid string) (string, bool) {
	if p.dir == "" || !ValidSID(sid) {
		return "", false
	}
	now := p.now()
	p.mu.Lock()
	prev, hadHit := p.found[sid]
	if hadHit && now.Sub(prev.at) < recheckTTL {
		p.mu.Unlock()
		return prev.folder, true
	}
	if at, ok := p.missed[sid]; ok && now.Sub(at) < missTTL {
		p.mu.Unlock()
		return "", false
	}
	p.mu.Unlock()

	folder, ok := p.scan(sid) // directory reads happen outside the lock
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case ok:
		delete(p.missed, sid)
		p.found[sid] = hit{folder, now}
	case hadHit:
		folder, ok = prev.folder, true
		p.found[sid] = hit{folder, now}
	default:
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

// scan is the folder whose `<sid>.jsonl` was modified last; the first by name when two have the
// same time.
func (p *Projects) scan(sid string) (string, bool) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return "", false
	}
	var best string
	var bestAt time.Time
	found := false
	for _, e := range entries {
		if !e.IsDir() || !ValidProject(e.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(p.dir, e.Name(), sid+".jsonl"))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if at := info.ModTime(); !found || at.After(bestAt) {
			best, bestAt, found = e.Name(), at, true
		}
	}
	return best, found
}

// size is the number of sessions remembered, for the tests.
func (p *Projects) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.found) + len(p.missed)
}
