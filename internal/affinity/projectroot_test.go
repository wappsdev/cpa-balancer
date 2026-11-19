package affinity

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRootFolderIsTheClaudeCodeFolderNameOfTheHeaderPath(t *testing.T) {
	cases := []struct {
		name, value, want string
	}{
		{"a plain path", "/home/u/proj", "-home-u-proj"},
		{"dots, underscores and spaces become dashes", "/home/u/my_proj v1.2", "-home-u-my-proj-v1-2"},
		{"a non-ASCII letter becomes a dash", "/home/u/projé", "-home-u-proj-"},
		{"an unclean path is cleaned first", "/home/u/proj/", "-home-u-proj"},
		{"dot segments are resolved", "/home/u/x/../proj", "-home-u-proj"},
		{"surrounding space is trimmed", "  /home/u/proj ", "-home-u-proj"},
		{"a relative path is ignored", "home/u/proj", ""},
		{"a dot path is ignored", "./proj", ""},
		{"the file system root is ignored", "/", ""},
		{"an empty value is ignored", "", ""},
		{"a control character is ignored", "/home/u/pr\noj", ""},
		{"a name too long for a folder is ignored", "/home/u/" + strings.Repeat("a", 300), ""},
		{"a huge value is ignored", "/" + strings.Repeat("a/", 5000), ""},
		{"invalid UTF-8 is ignored", "/home/u/\xff\xfe", ""},
	}
	for _, c := range cases {
		h := headers("X-Wapps-Project-Root", c.value)
		if got := RootFolder(h); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if c.want != "" && !ValidProject(c.want) {
			t.Errorf("%s: %q is not a valid project", c.name, c.want)
		}
	}
	if got := RootFolder(headers("x-wapps-project-root", "/home/u/proj")); got != "-home-u-proj" {
		t.Errorf("the header name is matched in any case: %q", got)
	}
	if RootFolder(nil) != "" || RootFolder(headers("Content-Type", "x")) != "" {
		t.Error("no header is no folder")
	}
}

func TestATaggedSessionHasItsFolderOnlyWhileNoTranscriptHoldsOne(t *testing.T) {
	dir := t.TempDir()
	p := NewProjects(dir, time.Now)
	p.Tag(sid, "-home-u-proj")
	if folder, ok := p.Folder(sid); !ok || folder != "-home-u-proj" {
		t.Fatalf("a tag is the folder when there is no transcript: %q %v", folder, ok)
	}
	if got := p.Tagged(sid); got != "-home-u-proj" {
		t.Fatalf("tagged: %q", got)
	}
	transcript(t, dir, "-work-alpha", sid)
	p.mu.Lock()
	p.index = nil // force a new pass
	p.mu.Unlock()
	if folder, _ := p.Folder(sid); folder != "-work-alpha" {
		t.Fatalf("a transcript wins over the tag: %q", folder)
	}
	if _, ok := p.Folder("00000000-2222-3333-4444-555555555555"); ok {
		t.Fatal("an untagged session without a transcript has no folder")
	}
	p.Tag("a/b", "-x") // not a session id: ignored
	p.Tag(sid, "")     // an empty folder is no tag
	if p.Tagged("a/b") != "" || p.Tagged(sid) != "-home-u-proj" {
		t.Fatal("a bad tag changes nothing")
	}
}

func TestResolveTagsOnlyASessionWithNoTranscript(t *testing.T) {
	dir := t.TempDir()
	transcript(t, dir, "-work-alpha", sid)
	p := NewProjects(dir, time.Now)
	other := "99999999-2222-3333-4444-555555555555"
	if folder, ok := p.Resolve(sid, "-home-u-proj"); !ok || folder != "-work-alpha" || p.Tagged(sid) != "" {
		t.Fatalf("a transcript wins and nothing is tagged: %q %v %q", folder, ok, p.Tagged(sid))
	}
	if folder, ok := p.Resolve(other, "-home-u-proj"); !ok || folder != "-home-u-proj" {
		t.Fatalf("a session with no transcript takes the header's folder: %q %v", folder, ok)
	}
	if folder, ok := p.Resolve(other, ""); !ok || folder != "-home-u-proj" {
		t.Fatalf("a request without a header keeps the earlier folder: %q %v", folder, ok)
	}
	if folder, _ := p.Resolve(other, "-home-u-other"); folder != "-home-u-other" {
		t.Fatalf("the latest header wins: %q", folder)
	}
	if _, ok := p.Resolve("00000000-2222-3333-4444-555555555555", ""); ok {
		t.Fatal("no header and no transcript is no folder")
	}
}

func TestTagsAreBoundedAndTheOldestIsDroppedFirst(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := NewProjects("", func() time.Time { return now })
	for i := 0; i < maxTagged+10; i++ {
		now = now.Add(time.Second)
		p.Tag(fmt.Sprintf("s%d", i), "-f")
	}
	if n := p.tagCount(); n != maxTagged {
		t.Fatalf("%d tags, the bound is %d", n, maxTagged)
	}
	if p.Tagged("s0") != "" || p.Tagged(fmt.Sprintf("s%d", maxTagged+9)) != "-f" {
		t.Fatal("the oldest tag is dropped, the newest is kept")
	}
	now = now.Add(time.Second)
	p.Tag("s10", "-f") // seen again: it is no longer the oldest
	now = now.Add(time.Second)
	p.Tag("new", "-f")
	if p.Tagged("s10") == "" || p.Tagged("s11") != "" {
		t.Fatal("a tag seen again is kept longer")
	}
}
