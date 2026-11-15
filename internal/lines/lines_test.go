package lines

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func collect(t *testing.T, input string, max int) (got []string, skipped int) {
	t.Helper()
	skipped, err := Each(strings.NewReader(input), max, func(b []byte) { got = append(got, string(b)) })
	if err != nil {
		t.Fatal(err)
	}
	return got, skipped
}

func TestEachReadsEveryLineWithAndWithoutATrailingNewline(t *testing.T) {
	got, skipped := collect(t, "one\ntwo\r\n\nthree", 100)
	if strings.Join(got, "|") != "one|two|three" || skipped != 0 {
		t.Fatalf("%q skipped %d", got, skipped)
	}
}

func TestAnOversizedLineIsSkippedAndTheRestIsStillRead(t *testing.T) {
	long := strings.Repeat("x", 200_000) // longer than the reader's own buffer, shorter than a real limit would be
	got, skipped := collect(t, "before\n"+long+"\nafter\n"+long, 1000)
	if strings.Join(got, "|") != "before|after" || skipped != 2 {
		t.Fatalf("%q skipped %d", got, skipped)
	}
}

func TestALongLineWithinTheLimitIsKeptWhole(t *testing.T) {
	long := strings.Repeat("y", 200_000)
	got, skipped := collect(t, "a\n"+long+"\nb\n", 300_000)
	if len(got) != 3 || got[1] != long || skipped != 0 {
		t.Fatalf("%d lines, skipped %d", len(got), skipped)
	}
}

type failing struct{ n int }

func (f *failing) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("disk gone")
	}
	f.n--
	return copy(p, "ok\n"), nil
}

func TestAReadErrorIsReturned(t *testing.T) {
	var got []string
	_, err := Each(io.MultiReader(strings.NewReader("first\n"), &failing{}), 100, func(b []byte) { got = append(got, string(b)) })
	if err == nil || len(got) != 1 {
		t.Fatalf("err %v lines %q", err, got)
	}
}
