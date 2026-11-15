package legacyimport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Fingerprint is the source as it stood: every entry of the snapshot (path, type, mode, size,
// mtime, sha256 of a file's bytes, a symlink's target, never followed) and the backup database
// with any -wal, -shm or -journal beside it. The run takes it before anything is read and again
// at the end; source_unchanged is their equality.
type Fingerprint struct {
	Digest  string
	Entries int
}

// TakeFingerprint walks oldHome and hashes oldDB.
func TakeFingerprint(oldHome, oldDB string) (Fingerprint, error) {
	h := sha256.New()
	n := 0
	err := filepath.WalkDir(oldHome, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(oldHome, p)
		line, lerr := entryLine(p, rel)
		if lerr != nil {
			return lerr
		}
		n++
		_, _ = io.WriteString(h, line)
		return nil
	})
	if err != nil {
		return Fingerprint{}, fmt.Errorf("fingerprint %s: %w", oldHome, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		p := oldDB + suffix
		if _, statErr := os.Lstat(p); statErr != nil {
			if os.IsNotExist(statErr) {
				fmt.Fprintf(h, "db%s absent\n", suffix)
				continue
			}
			return Fingerprint{}, statErr
		}
		line, lerr := entryLine(p, "db"+suffix)
		if lerr != nil {
			return Fingerprint{}, lerr
		}
		_, _ = io.WriteString(h, line)
	}
	return Fingerprint{Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Entries: n}, nil
}

func entryLine(p, rel string) (string, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	head := fmt.Sprintf("%q %s %d %d", rel, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s -> %q\n", head, target), nil
	case fi.Mode().IsRegular():
		sum, err := fileSHA256(p)
		if err != nil {
			return "", err
		}
		return head + " " + sum + "\n", nil
	}
	return head + "\n", nil
}

// fileSHA256 is "sha256:<hex>" of a file's bytes.
func fileSHA256(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // a path inside the snapshot or the new data directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// fileDigest is a file's size and sha256, its path reported as rel.
func fileDigest(p, rel string) (FileDigest, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return FileDigest{}, err
	}
	sum, err := fileSHA256(p)
	if err != nil {
		return FileDigest{}, err
	}
	return FileDigest{Path: rel, Size: fi.Size(), SHA256: sum}, nil
}
