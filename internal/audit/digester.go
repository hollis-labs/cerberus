package audit

import (
	"errors"
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

// ErrNoDigestKey is OpenExistingDigester's answer for a directory without a digest key.
var ErrNoDigestKey = errors.New("audit: no digest key")

// OpenExistingDigester loads dir's digest key and never creates one: a key that is absent where
// one was expected (a migration that did not copy it) must not be silently replaced.
func OpenExistingDigester(dir string) (Digester, error) {
	data, err := os.ReadFile(filepath.Join(dir, keyName)) //nolint:gosec // the audit directory's own key file
	if errors.Is(err, os.ErrNotExist) {
		return Digester{}, fmt.Errorf("%w at %s", ErrNoDigestKey, filepath.Join(dir, keyName))
	}
	if err != nil {
		return Digester{}, fmt.Errorf("audit: digest key: %w", err)
	}
	if len(data) != 32 {
		return Digester{}, fmt.Errorf("audit: digest key %s is not 32 bytes", filepath.Join(dir, keyName))
	}
	return Digester{key: data}, nil
}

// Fingerprint names the key without revealing it: a keyed digest of a fixed label. Two
// directories with the same key have the same fingerprint.
func (d Digester) Fingerprint() string {
	return digest(d.key, "cerberus applied digest key fingerprint v1")
}

// Digest is the keyed digest a FileSink over the same directory computes.
func (d Digester) Digest(v any) string { return digest(d.key, v) }
