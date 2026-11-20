package fleetprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/gitenv"
)

// maxHashSize bounds the files the probe hashes; larger files report no digest.
const maxHashSize = 512 << 20

// interpreters run a script named on their command line; the script, not the
// interpreter, is the service's artifact.
var interpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "bun": true, "deno": true,
	"php": true, "ruby": true, "perl": true, "bash": true, "sh": true, "dash": true, "zsh": true,
}

// isInterpreter reports whether base names an interpreter (python3.14 included).
func isInterpreter(base string) bool {
	if interpreters[base] {
		return true
	}
	for name := range interpreters {
		if strings.HasPrefix(base, name) && strings.Trim(strings.TrimPrefix(base, name), "0123456789.") == "" {
			return true
		}
	}
	return false
}

// hashCache remembers digests by file identity so repeated probes are cheap.
type hashCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]string
	dirty   bool
}

func loadHashCache(path string) *hashCache {
	c := &hashCache{path: path, entries: map[string]string{}}
	if path == "" {
		return c
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.entries)
	}
	return c
}

func (c *hashCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" || !c.dirty {
		return
	}
	// Bound the cache: drop it wholesale when it grows large; it refills cheaply.
	if len(c.entries) > 20000 {
		c.entries = map[string]string{}
	}
	b, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(c.path), 0o700)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

func fileKey(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%d:%d", st.Dev, st.Ino, fi.Size(), fi.ModTime().UnixNano())
	}
	return ""
}

// digest hashes path (opened through open, which may be a /proc/<pid>/exe link)
// using fi's identity as the cache key.
func (c *hashCache) digest(open string, fi os.FileInfo) (string, error) {
	if fi.Size() > maxHashSize {
		return "", nil
	}
	key := fileKey(fi)
	if key != "" {
		c.mu.Lock()
		d, ok := c.entries[key]
		c.mu.Unlock()
		if ok {
			return d, nil
		}
	}
	f, err := os.Open(open)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	d := hex.EncodeToString(h.Sum(nil))
	if key != "" {
		c.mu.Lock()
		c.entries[key] = d
		c.dirty = true
		c.mu.Unlock()
	}
	return d, nil
}

// identifier builds Artifacts. It is safe for concurrent use.
type identifier struct {
	hashes *hashCache
	gitMu  sync.Mutex
	gits   map[string]*GitInfo // keyed by directory
}

func newIdentifier(cachePath string) *identifier {
	return &identifier{hashes: loadHashCache(cachePath), gits: map[string]*GitInfo{}}
}

// fromFile identifies an executable file on disk.
func (id *identifier) fromFile(ctx context.Context, path string) Artifact {
	a := Artifact{Path: path, Kind: ArtifactOther}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	if resolved != path {
		a.ResolvedPath = resolved
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	id.fill(ctx, &a, resolved, resolved, fi)
	return a
}

// fromProcess identifies the executable a running process executes. It reads the
// binary through /proc/<pid>/exe so a replaced or deleted file is still the one
// identified, and flags that case.
func (id *identifier) fromProcess(ctx context.Context, pid int, cmdline []string) Artifact {
	link := fmt.Sprintf("/proc/%d/exe", pid)
	target, err := os.Readlink(link)
	if err != nil {
		return Artifact{Kind: ArtifactOther, Path: firstOr(cmdline, link), Error: err.Error()}
	}
	a := Artifact{Kind: ArtifactOther, Path: target}
	if strings.HasSuffix(target, " (deleted)") {
		a.Path = strings.TrimSuffix(target, " (deleted)")
		a.ReplacedOnDisk = true
	}
	fi, err := os.Stat(link)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	if !a.ReplacedOnDisk {
		// Replaced in place (same path, new inode): compare with what is on disk now.
		if cur, err := os.Stat(a.Path); err != nil || !os.SameFile(cur, fi) {
			a.ReplacedOnDisk = true
		}
	}
	id.fill(ctx, &a, link, a.Path, fi)
	if isInterpreter(filepath.Base(a.Path)) {
		if script := scriptArg(cmdline, procCwd(pid)); script != "" {
			a.Kind = ArtifactScript
			a.Script = id.script(ctx, filepath.Base(a.Path), script)
		}
	}
	return a
}

// fill hashes the file and reads its build provenance. open is the path to read
// (possibly a /proc link); logical is the path the layout is judged by.
func (id *identifier) fill(ctx context.Context, a *Artifact, open, logical string, fi os.FileInfo) {
	a.Size = fi.Size()
	a.ModTime = fi.ModTime().UTC().Truncate(time.Second)
	if d, err := id.hashes.digest(open, fi); err != nil {
		a.Error = err.Error()
	} else {
		a.SHA256 = d
	}
	a.Release = releaseOf(logical)
	if a.Release == nil && a.ResolvedPath != "" {
		a.Release = releaseOf(a.ResolvedPath)
	}
	if bi, err := buildinfo.ReadFile(open); err == nil {
		a.Kind = ArtifactGo
		a.Go = goBuild(bi)
		return
	}
	if isELF(open) {
		a.Kind = ArtifactELF
		return
	}
	if isText(open) {
		// An executable script listed directly (installed tools).
		a.Kind = ArtifactScript
		a.Script = &Script{Path: logical, Git: id.git(ctx, filepath.Dir(logical))}
		a.Script.Interpreter = shebang(open)
	}
}

func (id *identifier) script(ctx context.Context, interp, path string) *Script {
	s := &Script{Interpreter: interp, Path: path}
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		if d, err := id.hashes.digest(path, fi); err == nil {
			s.SHA256 = d
		}
	}
	s.Git = id.git(ctx, filepath.Dir(path))
	return s
}

// git reports the work tree containing dir, or nil when dir is not in one.
func (id *identifier) git(ctx context.Context, dir string) *GitInfo {
	id.gitMu.Lock()
	if g, ok := id.gits[dir]; ok {
		id.gitMu.Unlock()
		return g
	}
	id.gitMu.Unlock()
	g := readGit(ctx, dir)
	id.gitMu.Lock()
	id.gits[dir] = g
	id.gitMu.Unlock()
	return g
}

func readGit(ctx context.Context, dir string) *GitInfo {
	run := func(args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := gitenv.Command(cctx, dir, args...)
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	root, err := run("rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return nil
	}
	rev, err := run("rev-parse", "HEAD")
	if err != nil {
		return nil
	}
	g := &GitInfo{Root: root, Revision: rev}
	if b, err := run("symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		g.Branch = b
	}
	if r, err := run("remote", "get-url", "origin"); err == nil {
		g.Remote = stripCredentials(r)
	}
	if st, err := run("status", "--porcelain", "--untracked-files=no"); err == nil && st != "" {
		g.Dirty = true
	}
	return g
}

// stripCredentials removes any userinfo from a remote URL.
func stripCredentials(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 && (strings.Index(rest, "/") < 0 || at < strings.Index(rest, "/")) {
			return u[:i+3] + rest[at+1:]
		}
	}
	return u
}

func goBuild(bi *debug.BuildInfo) *GoBuild {
	g := &GoBuild{GoVersion: bi.GoVersion, Path: bi.Path, Module: bi.Main.Path, ModuleVersion: bi.Main.Version}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs":
			g.VCS = s.Value
		case "vcs.revision":
			g.Revision = s.Value
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				g.Time = t.UTC()
			}
		case "vcs.modified":
			g.Modified = s.Value == "true"
		}
	}
	if g.Revision == "" {
		// Built from the module cache (go install pkg@version): the pseudo-version
		// carries the revision prefix.
		if v := g.ModuleVersion; strings.Count(v, "-") >= 2 {
			parts := strings.Split(v, "-")
			if last := parts[len(parts)-1]; len(last) == 12 {
				g.Revision = last
			}
		}
	}
	return g
}

// SelfBuild is the running binary's own provenance.
func SelfBuild() *GoBuild {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return goBuild(bi)
}

// releaseOf recognises the layout <...>/releases/<app>/<version>/<file>.
func releaseOf(path string) *Release {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i := len(parts) - 4; i >= 0; i-- {
		if parts[i] == "releases" && i+3 < len(parts) {
			return &Release{App: parts[i+1], Version: parts[i+2], Dir: strings.Join(parts[:i+3], string(filepath.Separator))}
		}
	}
	return nil
}

func readHead(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	m, _ := io.ReadFull(f, buf)
	return buf[:m]
}

func isELF(path string) bool { return bytes.HasPrefix(readHead(path, 4), []byte("\x7fELF")) }

func isText(path string) bool {
	h := readHead(path, 512)
	return len(h) > 0 && bytes.HasPrefix(h, []byte("#!")) && !bytes.Contains(h, []byte{0})
}

func shebang(path string) string {
	h := readHead(path, 256)
	if !bytes.HasPrefix(h, []byte("#!")) {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(h[2:]), "\n", 2)[0])
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	if filepath.Base(f[0]) == "env" {
		for _, x := range f[1:] {
			if !strings.HasPrefix(x, "-") {
				return x
			}
		}
	}
	return filepath.Base(f[0])
}

// scriptArg finds the script an interpreter command line runs: the first
// argument after the interpreter that names an existing regular file.
// Flags and their values (-m module, -c code) end the search: those run no file.
func scriptArg(cmdline []string, cwd string) string {
	for i := 1; i < len(cmdline); i++ {
		arg := cmdline[i]
		if arg == "-c" || arg == "-m" || arg == "-e" || arg == "--eval" {
			return ""
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		p := arg
		if !filepath.IsAbs(p) && cwd != "" {
			p = filepath.Join(cwd, p)
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
		return ""
	}
	return ""
}

func firstOr(s []string, def string) string {
	if len(s) > 0 && s[0] != "" {
		return s[0]
	}
	return def
}
