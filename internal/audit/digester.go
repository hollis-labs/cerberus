package audit

import (
	"fmt"
	"os"
	"path/filepath"
)

// Digester digests with an audit directory's digest key, without opening its chain. The
// Substrate build keys applied definitions with it before its audit chain arrives (slice 6),
// so a digest migrated from the old build's runtime/applied.json, made with the same key,
// compares equal.
type Digester struct{ key []byte }

// OpenDigester loads dir's digest key, creating the directory and the key when absent.
func OpenDigester(dir string) (Digester, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return Digester{}, fmt.Errorf("audit: create %s: %w", dir, err)
	}
	key, err := loadOrCreateKey(filepath.Join(dir, keyName))
	if err != nil {
		return Digester{}, err
	}
	return Digester{key: key}, nil
}

// Digest is the keyed digest a FileSink over the same directory computes.
func (d Digester) Digest(v any) string { return digest(d.key, v) }
