package pluginhost

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

// storeID is what a plugin id must look like to name a store directory.
var storeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// A plugin bundle is the plugin's directory: plugin.yaml, its entrypoint and
// whatever else it ships. The bundle digest covers all of it — every file's
// relative path, executable bit and content — so an edited plugin.yaml (a
// widened effect, a new secret) is a change exactly as a rebuilt binary is.
// It detects change; it certifies nothing about who built the plugin.

// MaxBundleBytes bounds the size of a plugin bundle the host will copy and
// hash.
const MaxBundleBytes = 512 << 20

// ErrPluginChanged is a plugin whose bundle no longer matches the digest
// the operator accepted in its install review. The host refuses to load it.
var ErrPluginChanged = errors.New("plugin_changed")

// ChangedError reports a bundle that is not the one reviewed.
type ChangedError struct {
	ID       string
	Accepted string
	Found    string
}

func (e *ChangedError) Error() string {
	return fmt.Sprintf("plugin %q is not the plugin you reviewed: its bundle digest is %s, and the accepted review is for %s. Nothing was loaded. "+
		"To see what changed and accept it, run `cerberus connectors plugin managed load %s --accept-changes` in a terminal",
		e.ID, shortDigest(e.Found), shortDigest(e.Accepted), e.ID)
}

func (e *ChangedError) Unwrap() error { return ErrPluginChanged }

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return "sha256:" + d[:12]
	}
	if d == "" {
		return "(unreadable)"
	}
	return "sha256:" + d
}

// BundleDigest is the digest of the plugin directory dir. A symlink or any
// file that is not a regular file refuses the bundle: what the digest covers
// must be what the host would run, and a link can point anywhere.
func BundleDigest(dir string) (string, error) {
	type entry struct{ rel, line string }
	var entries []entry
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin bundle %s: %s is not a regular file (symlinks and devices are refused)", dir, filepath.ToSlash(rel))
		}
		total += info.Size()
		if total > MaxBundleBytes {
			return fmt.Errorf("plugin bundle %s is larger than %d MiB", dir, MaxBundleBytes>>20)
		}
		sum, sumErr := fileSHA256(path)
		if sumErr != nil {
			return sumErr
		}
		exec := "-"
		if info.Mode().Perm()&0o111 != 0 {
			exec = "x"
		}
		slash := filepath.ToSlash(rel)
		entries = append(entries, entry{rel: slash, line: slash + "\x00" + exec + "\x00" + sum + "\n"})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest plugin bundle: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		_, _ = io.WriteString(h, e.line)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a file inside the plugin bundle being digested
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// A Store holds reviewed plugin bundles, one directory per accepted digest:
// <root>/<id>/<digest>/. The host runs a plugin from its store copy, never
// from the directory it was installed from, so rebuilding a checkout changes
// nothing that runs until it is installed and reviewed again.
type Store struct{ Root string }

// Staged is a bundle copied into the store's staging area and digested,
// awaiting review. What is reviewed is the staged copy, and committing moves
// that same copy into place, so a source that changes during the review
// cannot change what is stored.
type Staged struct {
	Dir    string
	Digest string
	Spec   PluginYAML
	// EntrypointSHA256 is the staged entrypoint's digest.
	EntrypointSHA256 string
}

// Stage copies source into the store's staging area, reads its plugin.yaml
// and digests the copy.
func (s Store) Stage(source string) (*Staged, error) {
	src, err := resolvePluginDir(source)
	if err != nil {
		return nil, err
	}
	if _, err = BundleDigest(src); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, fmt.Errorf("create plugin store: %w", err)
	}
	staging := filepath.Join(s.Root, ".staging-"+randomSuffix())
	if err = copyBundle(src, staging); err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	staged, err := inspectBundle(staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	return staged, nil
}

// Inspect reads and digests a bundle in place, for a development install
// that runs from its source directory.
func Inspect(dir string) (*Staged, error) {
	src, err := resolvePluginDir(dir)
	if err != nil {
		return nil, err
	}
	return inspectBundle(src)
}

func inspectBundle(dir string) (*Staged, error) {
	spec, err := ReadPluginYAML(dir)
	if err != nil {
		return nil, err
	}
	digest, err := BundleDigest(dir)
	if err != nil {
		return nil, err
	}
	entry, err := hashPluginEntrypoint(dir, spec)
	if err != nil {
		return nil, err
	}
	return &Staged{Dir: dir, Digest: digest, Spec: spec, EntrypointSHA256: entry}, nil
}

// Commit moves a staged bundle to <root>/<id>/<digest>/ and returns that
// path. Committing a digest the store already holds keeps the stored copy
// after checking it still matches.
func (s Store) Commit(staged *Staged) (string, error) {
	if !storeID.MatchString(staged.Spec.ID) {
		return "", fmt.Errorf("plugin id %q cannot name a store directory: use lowercase letters, digits, '-' and '_'", staged.Spec.ID)
	}
	final := s.Path(staged.Spec.ID, staged.Digest)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return "", fmt.Errorf("create plugin store: %w", err)
	}
	if _, err := os.Stat(final); err == nil {
		existing, digestErr := BundleDigest(final)
		if digestErr == nil && existing == staged.Digest {
			_ = os.RemoveAll(staged.Dir)
			return final, nil
		}
		// A stored copy that no longer matches its own name was altered;
		// replace it with the copy just reviewed.
		if err := os.RemoveAll(final); err != nil {
			return "", fmt.Errorf("replace altered plugin store copy: %w", err)
		}
	}
	if err := os.Rename(staged.Dir, final); err != nil {
		return "", fmt.Errorf("commit plugin bundle: %w", err)
	}
	return final, nil
}

// Discard removes a staged bundle that was not accepted.
func (s Store) Discard(staged *Staged) {
	if staged != nil && strings.HasPrefix(filepath.Base(staged.Dir), ".staging-") {
		_ = os.RemoveAll(staged.Dir)
	}
}

// Path is where the bundle with this digest is stored.
func (s Store) Path(id, digest string) string {
	return filepath.Join(s.Root, id, strings.TrimPrefix(digest, "sha256:"))
}

// Remove deletes every stored bundle of a plugin, for uninstall.
func (s Store) Remove(id string) error {
	if !storeID.MatchString(id) {
		return fmt.Errorf("refusing to remove store entry %q", id)
	}
	return os.RemoveAll(filepath.Join(s.Root, id))
}

// Owns reports whether path is inside the store, so the caller knows a
// directory is Cerberus's copy rather than the operator's checkout.
func (s Store) Owns(path string) bool {
	return s.Root != "" && pathAllowed(path, []string{s.Root})
}

func copyBundle(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin bundle %s: %s is not a regular file (symlinks and devices are refused)", src, filepath.ToSlash(rel))
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		return copyFile(path, target, mode)
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // a file inside the plugin bundle being installed
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // inside the store's staging directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("pluginhost: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// CheckBundle is the load-time check of an installed plugin: its declared
// host range includes this host's contract, and, once reviewed, its bundle
// still matches the accepted digest.
func CheckBundle(p InstalledPlugin) error {
	if err := p.Spec.Cerberus.Host.Check(pluginsdk.ContractVersion); err != nil {
		return fmt.Errorf("plugin %q not loaded: %w", p.ID, err)
	}
	if p.BundleDigest == "" {
		return nil
	}
	found, err := BundleDigest(p.Path)
	if err != nil {
		return &ChangedError{ID: p.ID, Accepted: p.BundleDigest}
	}
	if found != p.BundleDigest {
		return &ChangedError{ID: p.ID, Accepted: p.BundleDigest, Found: found}
	}
	return nil
}
