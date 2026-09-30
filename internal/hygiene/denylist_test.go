package hygiene

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/gitenv"
)

// Cerberus is a shared package: no tracked file may describe one operator's
// estate, whether names, hosts, tenants or home directories. The committed
// patterns below are generic shapes. The specific names an operator wants
// kept out live in a local, untracked denylist, since committing them would
// publish exactly what the list guards: one regular expression per line,
// matched case-insensitively, '#' for comments, read from
// $CERBERUS_DENYLIST or ~/.config/cerberus/denylist.txt when present.

// placeholders are the home-directory names a doc or fixture may use.
const placeholders = `(?:me|you|op|operator|user|tester|example|runner|name)\b`

var genericPatterns = []string{
	// A macOS or Linux home directory under a real-looking user name.
	`/Users/(?:[a-z][a-z0-9._-]*)` + `(?:/|\b)`,
	`/home/(?:[a-z][a-z0-9._-]*)` + `(?:/|\b)`,
	// A corporate hostname.
	`\b[a-z0-9-]+\.corp\.[a-z0-9.-]+\b`,
}

var allowed = regexp.MustCompile(`(?i)/(?:Users|home)/` + placeholders)

func TestTrackedFilesCarryNoEstateSpecifics(t *testing.T) {
	root := repoRoot(t)
	patterns := compile(t, genericPatterns)
	local := localDenylist(t)
	patterns = append(patterns, local...)
	files := trackedFiles(t, root)
	var hits []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // a tracked file of this repository
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue // unreadable, or binary
		}
		for n, line := range strings.Split(string(data), "\n") {
			for _, re := range patterns {
				for _, m := range re.FindAllString(line, -1) {
					if allowed.MatchString(m) {
						continue
					}
					hits = append(hits, f+":"+itoa(n+1)+": "+m)
				}
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("tracked files name an operator's estate; make them generic (placeholders like /Users/me, host-a.example.com):\n%s", strings.Join(hits, "\n"))
	}
	if len(local) == 0 {
		t.Log("no local denylist; checked the generic patterns only")
	}
}

// The generic patterns catch what they are for and pass the placeholders.
func TestGenericPatternsMatchTheirShapes(t *testing.T) {
	patterns := compile(t, genericPatterns)
	match := func(s string) bool {
		for _, re := range patterns {
			for _, m := range re.FindAllString(s, -1) {
				if !allowed.MatchString(m) {
					return true
				}
			}
		}
		return false
	}
	// Split so this file's own text doesn't match.
	for _, s := range []string{"/Users/" + "jdoe/dev/app", "/home/" + "jdoe/.ssh", "build." + "corp.example.net"} {
		if !match(s) {
			t.Errorf("%q passed", s)
		}
	}
	for _, s := range []string{"/Users/me/src/cerberus", "/home/user/relay", "/Users/<other-user>/x", `"/Users/"+name`, "host-a.example.com"} {
		if match(s) {
			t.Errorf("%q was flagged", s)
		}
	}
}

func compile(t *testing.T, exprs []string) []*regexp.Regexp {
	t.Helper()
	out := make([]*regexp.Regexp, 0, len(exprs))
	for _, e := range exprs {
		re, err := regexp.Compile("(?i)" + e)
		if err != nil {
			t.Fatalf("denylist pattern %q: %v", e, err)
		}
		out = append(out, re)
	}
	return out
}

func localDenylist(t *testing.T) []*regexp.Regexp {
	t.Helper()
	path := os.Getenv("CERBERUS_DENYLIST")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".config", "cerberus", "denylist.txt")
	}
	f, err := os.Open(path) //nolint:gosec // the operator's own denylist
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var exprs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		exprs = append(exprs, line)
	}
	return compile(t, exprs)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// gitenv strips GIT_* variables: under a hook, GIT_DIR would point git
	// at the hook's repository, not this one.
	out, err := gitenv.Command(context.Background(), ".", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := gitenv.Command(context.Background(), root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && !strings.HasPrefix(f, "web/package-lock.json") {
			files = append(files, f)
		}
	}
	return files
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for n > 0 || i == len(b) {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
