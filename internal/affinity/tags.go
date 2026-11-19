package affinity

import (
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	rootHeader = "X-Wapps-Project-Root"
	maxRoot    = 4096 // the longest header value read; a longer one is not a path of a project
	maxTagged  = 5000 // sessions whose folder came from a header; the least recently seen is dropped first
)

// RootFolder is the project folder named by a request's X-Wapps-Project-Root header, written the way
// Claude Code names a folder under its projects directory (see EncodePath). A client that cannot
// write transcripts there, such as a job runner, sends it. The value must be an absolute path of
// at most maxRoot bytes, valid UTF-8 without control characters, and its folder name must be a
// valid project; otherwise the header is ignored and "" is returned. The path is cleaned first, so
// "/a/b/" and "/a/b" are one folder. The file system root names no project.
func RootFolder(h map[string][]string) string {
	root := header(h, rootHeader)
	if root == "" || len(root) > maxRoot || !utf8.ValidString(root) || !strings.HasPrefix(root, "/") {
		return ""
	}
	for _, r := range root {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	if root = path.Clean(root); root == "/" {
		return ""
	}
	if folder := EncodePath(root); ValidProject(folder) {
		return folder
	}
	return ""
}

// tag is the folder a header gave a session, and when the session last sent it.
type tag struct {
	folder string
	at     time.Time
}

// Resolve is Folder for a request that may carry a header's folder (see RootFolder; "" when it does
// not). A transcript's folder wins and tags nothing. Without one, the header's folder is remembered
// as the session's and returned; a request without a header keeps the folder an earlier one gave.
func (p *Projects) Resolve(sid, fromHeader string) (string, bool) {
	if folder, ok := p.transcript(sid); ok {
		return folder, true
	}
	p.Tag(sid, fromHeader)
	return p.Folder(sid)
}

// Tag remembers the folder a request's header gave a root session. It is the session's folder only
// while no transcript holds one (see Folder). A bad session id or an invalid folder is ignored.
func (p *Projects) Tag(sid, folder string) { p.Restore(sid, folder, p.now()) }

// Restore is Tag for a folder seen at a given time, such as one the history recorded before a
// restart. Memory is bounded: past maxTagged the least recently seen tag is dropped.
func (p *Projects) Restore(sid, folder string, at time.Time) {
	if !ValidSID(sid) || !ValidProject(folder) {
		return
	}
	p.tagMu.Lock()
	defer p.tagMu.Unlock()
	if p.tags == nil {
		p.tags = map[string]tag{}
	}
	if _, ok := p.tags[sid]; !ok && len(p.tags) >= maxTagged {
		oldest, first := "", true
		for k, t := range p.tags {
			if first || t.at.Before(p.tags[oldest].at) {
				oldest, first = k, false
			}
		}
		delete(p.tags, oldest)
	}
	p.tags[sid] = tag{folder, at}
}

// Tagged is the folder a header gave a root session, whether or not a transcript holds another.
func (p *Projects) Tagged(sid string) string {
	p.tagMu.Lock()
	defer p.tagMu.Unlock()
	return p.tags[sid].folder
}

// tagCount is the number of tags, for the tests.
func (p *Projects) tagCount() int {
	p.tagMu.Lock()
	defer p.tagMu.Unlock()
	return len(p.tags)
}
