package legacyimport

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// copyFile copies src to dst byte for byte, creating dst (never replacing one) at 0600, and
// syncs it. It returns the source's digest and checks the copy's against it.
func copyFile(src, dst string) (FileDigest, error) {
	const mode = 0o600
	fi, err := os.Lstat(src)
	if err != nil {
		return FileDigest{}, err
	}
	if !fi.Mode().IsRegular() {
		return FileDigest{}, fmt.Errorf("%s is not a regular file", src)
	}
	in, err := os.Open(src) //nolint:gosec // a path inside the snapshot
	if err != nil {
		return FileDigest{}, err
	}
	defer func() { _ = in.Close() }()
	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return FileDigest{}, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // a path inside the staged data directory
	if err != nil {
		return FileDigest{}, err
	}
	_, copyErr := io.Copy(out, in)
	chErr := out.Chmod(mode)
	syncErr := out.Sync()
	if err = errors.Join(copyErr, chErr, syncErr, out.Close()); err != nil {
		return FileDigest{}, fmt.Errorf("copy %s: %w", src, err)
	}
	srcSum, err := fileDigest(src, filepath.Base(src))
	if err != nil {
		return FileDigest{}, err
	}
	dstSum, err := fileDigest(dst, filepath.Base(dst))
	if err != nil {
		return FileDigest{}, err
	}
	if srcSum.SHA256 != dstSum.SHA256 || srcSum.Size != dstSum.Size {
		return FileDigest{}, fmt.Errorf("copy of %s does not match it (%s, source %s)", src, dstSum.SHA256, srcSum.SHA256)
	}
	return srcSum, nil
}

// copyTree copies the regular files under src into dst (files 0600, directories 0700), skipping
// what skip says (relative paths), and returns each file's digest, its path relative to src.
// A symlink or another special file is refused: the import never follows one out of the
// snapshot.
func copyTree(src, dst string, skip func(rel string, d fs.DirEntry) bool) ([]FileDigest, error) {
	var out []FileDigest
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel != "." && skip != nil && skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, 0o700) //nolint:gosec // a directory: 0700 is owner-only
		case d.Type().IsRegular():
			sum, err := copyFile(p, target)
			if err != nil {
				return err
			}
			sum.Path = rel
			out = append(out, sum)
			return nil
		}
		return fmt.Errorf("%s is a %s, not a regular file or directory; the import copies neither links nor special files", p, d.Type())
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// treeDigests are the digests of the regular files under dir, relative paths.
func treeDigests(dir string) ([]FileDigest, error) {
	var out []FileDigest
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum, err := fileDigest(p, rel)
		if err != nil {
			return err
		}
		out = append(out, sum)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// sameDigests reports how two digest lists differ, or "".
func sameDigests(a, b []FileDigest) string {
	am := map[string]FileDigest{}
	for _, x := range a {
		am[x.Path] = x
	}
	bm := map[string]FileDigest{}
	for _, x := range b {
		bm[x.Path] = x
	}
	for p, x := range am {
		y, ok := bm[p]
		if !ok {
			return p + " is missing from the copy"
		}
		if x.SHA256 != y.SHA256 {
			return p + " differs in the copy"
		}
	}
	for p := range bm {
		if _, ok := am[p]; !ok {
			return p + " is in the copy but not the source"
		}
	}
	return ""
}

// exists reports whether p exists (without following a final symlink).
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// dirEmpty reports whether dir is an empty directory.
func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// writePrivate writes data to a new file at p, 0600, creating its directory 0700.
func writePrivate(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a path inside the staged data directory
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	return errors.Join(werr, f.Sync(), f.Close())
}
