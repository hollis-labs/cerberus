package cerbapi

import (
	"io"
	"os"
)

// readLastNLines reads the last n lines from a file efficiently by seeking
// from the end of the file rather than loading the entire file into memory.
//
// Mirrors the identical helper in internal/mcp/tools_observability.go —
// duplicated (rather than moved) to keep internal/mcp's package surface
// stable for this scoped CERB-2 cut. A follow-up can consolidate.
func readLastNLines(path string, n int) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path originates from service config, not user input
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck

	stat, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := stat.Size()
	if size == 0 {
		return "", nil
	}

	const chunkSize = 4096
	buf := make([]byte, 0, chunkSize)
	newlines := 0
	offset := size

	for offset > 0 && newlines <= n {
		readSize := int64(chunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, rerr := f.ReadAt(chunk, offset); rerr != nil && rerr != io.EOF {
			return "", rerr
		}
		buf = append(chunk, buf...)
		for _, b := range chunk {
			if b == '\n' {
				newlines++
			}
		}
	}

	end := len(buf)
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	start := end
	found := 0
	for start > 0 && found < n {
		start--
		if buf[start] == '\n' {
			found++
			if found == n {
				start++
				break
			}
		}
	}
	return string(buf[start:end]), nil
}
