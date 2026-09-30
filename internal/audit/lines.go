package audit

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxLineBytes is the longest line the hash-chained stores read as a
// record. A longer one is read as damaged — one bad line — and the read
// goes on past it (H-e): a line over a scanner's limit used to end the read
// with an error, which made the whole log unreadable, so a lift and a
// reanchor were refused, and the brakes fell back to their store alone.
const MaxLineBytes = 4 << 20

// ScanLines calls fn for each line of r, without its newline, and tooLong
// for a line over limit bytes, whose content is not kept. The bytes are only
// valid during the call. It returns the reader's error, never one for a
// long line.
func ScanLines(r io.Reader, limit int, fn func(line []byte, tooLong bool)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > limit+1 {
				tooLong, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil && !errors.Is(err, io.EOF):
			return err
		}
		ended := err == nil
		if ended || len(buf) > 0 || tooLong {
			line := bytes.TrimSuffix(buf, []byte{'\n'})
			if tooLong || len(line) > 0 || ended {
				fn(line, tooLong)
			}
		}
		if !ended {
			return nil
		}
		buf, tooLong = buf[:0], false
	}
}
