// Package lines reads a file line by line with a size limit per line.
package lines

import (
	"bufio"
	"bytes"
	"io"
)

// Each calls fn for every non-empty line of r, without its line break. fn gets a slice that is
// only valid during the call. A line longer than max bytes is skipped, not read into memory, and
// counted; the lines after it are still read. A read error ends the loop and is returned.
func Each(r io.Reader, max int, fn func(line []byte)) (skipped int, err error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var long []byte // the start of a line longer than the reader's buffer
	over := false   // the line in progress is past max: its rest is dropped
	for {
		chunk, rerr := br.ReadSlice('\n')
		if rerr == bufio.ErrBufferFull {
			if !over {
				if len(long)+len(chunk) > max {
					over, long = true, nil
				} else {
					long = append(long, chunk...)
				}
			}
			continue
		}
		switch {
		case over:
			skipped++
		default:
			line := chunk
			if len(long) > 0 {
				line = append(long, chunk...)
			}
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > max {
				skipped++
			} else if len(line) > 0 {
				fn(line)
			}
		}
		over, long = false, nil
		if rerr != nil {
			if rerr == io.EOF {
				return skipped, nil
			}
			return skipped, rerr
		}
	}
}
