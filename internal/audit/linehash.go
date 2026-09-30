package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// LineHashMatches reports whether hash is the SHA-256 of line as it was
// written, with its hash field empty. The hash-chained stores (this log, the
// approvals and brake stores) marshal a record with Hash empty, hash those
// bytes and write the same marshal with Hash set, as the last field; so the
// line on disk, with that field emptied again, is exactly what was hashed.
//
// Re-marshaling the parsed record is not: a string that was not valid UTF-8
// is written as the escape � and reads back as the character U+FFFD,
// which marshals as its own three bytes. A record like that, which a caller's
// long non-ASCII name clipped mid-character used to produce, failed every
// later verification (M2). Checking the bytes verifies it, and every record
// written before this check existed.
func LineHashMatches(line []byte, hash string) bool {
	line = bytes.TrimSpace(line)
	suffix := []byte(`,"hash":"` + hash + `"}`)
	if hash == "" || !bytes.HasSuffix(line, suffix) {
		return false
	}
	var b bytes.Buffer
	b.Write(line[:len(line)-len(suffix)])
	b.WriteString(`,"hash":""}`)
	sum := sha256.Sum256(b.Bytes())
	return "sha256:"+hex.EncodeToString(sum[:]) == hash
}
