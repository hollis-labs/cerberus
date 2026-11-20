package fleetprobe

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/gitenv"
)

// Upstream relates a running build to its repository's default branch, as seen
// from a local checkout of that repository.
type Upstream struct {
	// Branch is the remote-tracking ref compared against (origin/main).
	Branch string `json:"branch"`
	// Behind is the number of commits on Branch that the build does not have.
	// Nil when the build's revision is not in the local checkout.
	Behind *int `json:"behind,omitempty"`
	// Merged lists pull requests merged on Branch since the build's revision,
	// newest first, read from "(#123)" in commit subjects.
	Merged []MergedPR `json:"merged,omitempty"`
	// FetchedAt is when the checkout last fetched; old data understates Behind.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Note      string    `json:"note,omitempty"`
}

// MergedPR is one pull request merged after the running build.
type MergedPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// RepoIndex maps Go module paths to local git checkouts.
type RepoIndex struct {
	mu      sync.Mutex
	modules map[string]string // module path -> checkout dir
	results map[string]*Upstream
}

// maxIndexDepth bounds the directory walk below each root.
const maxIndexDepth = 4

// NewRepoIndex finds Go modules (go.mod at a git work tree root or below it)
// under roots. Worktrees and nested modules are indexed; the first checkout
// found for a module wins, and a checkout on its default branch is preferred.
func NewRepoIndex(roots []string) *RepoIndex {
	ix := &RepoIndex{modules: map[string]string{}, results: map[string]*Upstream{}}
	for _, root := range roots {
		root = filepath.Clean(root)
		base := strings.Count(root, string(filepath.Separator))
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata") {
					return filepath.SkipDir
				}
				if strings.Count(p, string(filepath.Separator))-base > maxIndexDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() != "go.mod" {
				return nil
			}
			mod := modulePath(p)
			if mod == "" {
				return nil
			}
			dir := filepath.Dir(p)
			if prev, ok := ix.modules[mod]; !ok || (!onDefaultBranch(prev) && onDefaultBranch(dir)) {
				ix.modules[mod] = dir
			}
			return nil
		})
	}
	return ix
}

func modulePath(gomod string) string {
	f, err := os.Open(gomod)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`)
		}
	}
	return ""
}

func onDefaultBranch(dir string) bool {
	b, err := gitenv.Command(context.Background(), dir, "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(b))
	return s == "main" || s == "master"
}

// Len is the number of indexed modules.
func (ix *RepoIndex) Len() int { return len(ix.modules) }

var prRef = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// Lookup relates module@revision to the module's default branch. It returns
// nil when the module has no local checkout or the revision is empty.
func (ix *RepoIndex) Lookup(ctx context.Context, module, revision string) *Upstream {
	if ix == nil || module == "" || revision == "" {
		return nil
	}
	// A binary's main package may live in a nested module path
	// (example.com/repo/cmd/x is reported as module example.com/repo).
	dir, ok := ix.modules[module]
	if !ok {
		return nil
	}
	key := module + "@" + revision
	ix.mu.Lock()
	if u, ok := ix.results[key]; ok {
		ix.mu.Unlock()
		return u
	}
	ix.mu.Unlock()
	u := upstreamOf(ctx, dir, revision)
	ix.mu.Lock()
	ix.results[key] = u
	ix.mu.Unlock()
	return u
}

func upstreamOf(ctx context.Context, dir, revision string) *Upstream {
	git := func(args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := gitenv.Command(cctx, dir, args...)
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	branch := "origin/main"
	if head, err := git("symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); err == nil && head != "" {
		branch = head
	} else if _, err := git("rev-parse", "--verify", "-q", "origin/main"); err != nil {
		if _, err := git("rev-parse", "--verify", "-q", "origin/master"); err == nil {
			branch = "origin/master"
		} else {
			return &Upstream{Branch: branch, Note: "checkout has no origin/main"}
		}
	}
	u := &Upstream{Branch: branch}
	if fi, err := os.Stat(gitPath(ctx, dir, "FETCH_HEAD")); err == nil {
		u.FetchedAt = fi.ModTime().UTC().Truncate(time.Second)
	}
	if _, err := git("cat-file", "-e", revision+"^{commit}"); err != nil {
		u.Note = "revision not in the local checkout (fetch, or the build is from another clone)"
		return u
	}
	n, err := git("rev-list", "--count", revision+".."+branch)
	if err != nil {
		u.Note = "could not compare with " + branch
		return u
	}
	if c, err := strconv.Atoi(n); err == nil {
		u.Behind = &c
	}
	if log, err := git("log", "--format=%s", revision+".."+branch); err == nil {
		for _, subj := range strings.Split(log, "\n") {
			if m := prRef.FindStringSubmatch(subj); m != nil {
				num, _ := strconv.Atoi(m[1])
				u.Merged = append(u.Merged, MergedPR{Number: num, Title: strings.TrimSpace(prRef.ReplaceAllString(subj, ""))})
			}
		}
	}
	return u
}

func gitPath(ctx context.Context, dir, name string) string {
	out, err := gitenv.Command(ctx, dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return filepath.Join(dir, ".git", name)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Join(p, name)
}

// Annotate sets Upstream on every Go artifact in the report that ix can relate.
func (ix *RepoIndex) Annotate(ctx context.Context, r *Report) {
	if ix == nil || r == nil {
		return
	}
	do := func(a *Artifact) {
		if a != nil && a.Go != nil && a.Go.Revision != "" && len(a.Go.Revision) >= 12 {
			a.Upstream = ix.Lookup(ctx, a.Go.Module, a.Go.Revision)
		}
	}
	for i := range r.Services {
		do(r.Services[i].Artifact)
		for j := range r.Services[i].Children {
			do(&r.Services[i].Children[j])
		}
	}
	for i := range r.Binaries {
		do(&r.Binaries[i])
	}
}

// Drift is one executable that runs or is installed at different builds
// across hosts.
type Drift struct {
	// Name is the binary or service executable name.
	Name   string `json:"name"`
	Module string `json:"module"`
	// Builds maps a build (revision, with "+dirty" when modified, or the
	// module version) to the hosts that have it.
	Builds map[string][]string `json:"builds"`
}

// FindDrift lists Go executables present on two or more hosts at different builds.
func FindDrift(reports map[string]*Report) []Drift {
	type key struct{ name, module string }
	seen := map[key]map[string]map[string]bool{}
	add := func(host string, a *Artifact) {
		if a == nil || a.Go == nil || a.Go.Module == "" {
			return
		}
		build := a.Go.Revision
		if build == "" {
			build = a.Go.ModuleVersion
		}
		if len(build) > 12 && a.Go.Revision != "" {
			build = build[:12]
		}
		if a.Go.Modified {
			build += "+dirty"
		}
		if build == "" || build == "(devel)" {
			build = "unstamped:" + shortSHA(a.SHA256)
		}
		k := key{filepath.Base(a.Path), a.Go.Module}
		if seen[k] == nil {
			seen[k] = map[string]map[string]bool{}
		}
		if seen[k][build] == nil {
			seen[k][build] = map[string]bool{}
		}
		seen[k][build][host] = true
	}
	for host, r := range reports {
		if r == nil {
			continue
		}
		for i := range r.Services {
			add(host, r.Services[i].Artifact)
		}
		for i := range r.Binaries {
			add(host, &r.Binaries[i])
		}
	}
	var out []Drift
	for k, builds := range seen {
		if len(builds) < 2 {
			continue
		}
		hostsSeen := map[string]bool{}
		d := Drift{Name: k.name, Module: k.module, Builds: map[string][]string{}}
		for b, hs := range builds {
			for h := range hs {
				d.Builds[b] = append(d.Builds[b], h)
				hostsSeen[h] = true
			}
			sort.Strings(d.Builds[b])
		}
		if len(hostsSeen) < 2 {
			continue // different builds on one host only (an old copy beside the current one)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
