package hygiene

import (
	"bufio"
	"context"
	"fmt"
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

// placeholders are the home-directory names a doc or fixture may use. A
// name passes only when it is exactly one of these: "op" passes, "op-jdoe"
// does not.
var placeholders = map[string]bool{
	"me": true, "you": true, "op": true, "operator": true, "user": true,
	"tester": true, "example": true, "runner": true, "name": true,
}

// homeDir finds a home directory and captures its user name.
var homeDir = regexp.MustCompile(`(?i)/(?:Users|home)/([a-z][a-z0-9._-]*)`)

// guid is a GUID. One whose every digit is the same (the all-zero GUID, say)
// is a placeholder and passes.
var guid = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

var genericPatterns = []string{
	// A corporate hostname.
	`\b[a-z0-9-]+\.corp\.[a-z0-9.-]+\b`,
}

// placeholderGUID is a GUID made of one repeated digit.
func placeholderGUID(g string) bool {
	digits := strings.ReplaceAll(strings.ToLower(g), "-", "")
	return strings.Count(digits, digits[:1]) == len(digits)
}

// findings are the estate-shaped strings in text: home directories under a
// name that is not a placeholder, GUIDs that are not placeholders, and every
// match of the patterns.
func findings(text string, patterns []*regexp.Regexp) []string {
	var out []string
	for _, m := range homeDir.FindAllStringSubmatch(text, -1) {
		if !placeholders[strings.ToLower(m[1])] {
			out = append(out, m[0])
		}
	}
	for _, g := range guid.FindAllString(text, -1) {
		if !placeholderGUID(g) {
			out = append(out, g)
		}
	}
	for _, re := range patterns {
		out = append(out, re.FindAllString(text, -1)...)
	}
	return out
}

// joined is text with its line breaks, and the indentation around them,
// taken out: a term wrapped across two lines of prose or split over a
// string concatenation's lines is found whole.
var lineBreak = regexp.MustCompile(`[ \t]*\r?\n[ \t]*(?:(?://|#|\*|>)[ \t]*)?`)

func TestTrackedFilesCarryNoEstateSpecifics(t *testing.T) {
	root := repoRoot(t)
	patterns := compile(t, genericPatterns)
	local, source := localDenylist(t)
	patterns = append(patterns, local...)
	var hits []string
	for _, f := range trackedFiles(t, root) {
		// The file's name is checked like its contents.
		for _, m := range findings(f, patterns) {
			hits = append(hits, f+": (file name) "+m)
		}
		// Binary files are read as text too: a name in a fixture blob
		// is still published.
		data, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // a tracked file of this repository
		if err != nil {
			hits = append(hits, f+": unreadable: "+err.Error())
			continue
		}
		text := string(data)
		seen := map[string]bool{}
		for n, line := range strings.Split(text, "\n") {
			for _, m := range findings(line, patterns) {
				seen[strings.ToLower(m)] = true
				hits = append(hits, f+":"+itoa(n+1)+": "+m)
			}
		}
		// Whole-file, across line breaks: what no single line shows.
		for _, m := range findings(lineBreak.ReplaceAllString(text, ""), patterns) {
			if !seen[strings.ToLower(m)] {
				seen[strings.ToLower(m)] = true
				hits = append(hits, f+": (across lines) "+m)
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("tracked files name an operator's estate; make them generic (placeholders like /Users/me, host-a.example.com, the all-zero GUID):\n%s", strings.Join(hits, "\n"))
	}
	if len(local) == 0 {
		// Loud, not silent: a pass without the denylist checked the
		// generic shapes only. The hook prints the same.
		fmt.Fprintf(os.Stderr, "WARNING: no local estate denylist at %s; checked the generic patterns only\n", source)
	}
}

// The generic shapes catch what they are for and pass the placeholders.
func TestGenericPatternsMatchTheirShapes(t *testing.T) {
	patterns := compile(t, genericPatterns)
	flagged := func(s string) bool { return len(findings(s, patterns)) > 0 }
	// Split so this file's own text doesn't match.
	for _, s := range []string{
		"/Users/" + "jdoe/dev/app", "/home/" + "jdoe/.ssh", "build." + "corp.example.net",
		"/Users/" + "op-jdoe/x", "/Users/" + "mejdoe",
		"00000000" + "-0000-0000-0000-000000000000",
	} {
		if !flagged(s) {
			t.Errorf("%q passed", s)
		}
	}
	for _, s := range []string{
		"/Users/me/src/cerberus", "/home/user/relay", "/Users/op", "/Users/<other-user>/x", `"/Users/"+name`,
		"host-a.example.com", "00000000-0000-0000-0000-000000000000", "ffffffff-ffff-ffff-ffff-ffffffffffff",
	} {
		if flagged(s) {
			t.Errorf("%q was flagged", s)
		}
	}
}

// A term wrapped across lines is found in the whole-file pass.
func TestAcrossLinesFindsASplitTerm(t *testing.T) {
	patterns := compile(t, []string{`\bsecret-host\b`})
	text := "the box is secret-\n  host, reached by ssh"
	if len(findings(text, patterns)) != 0 {
		t.Fatal("a single line should not show it")
	}
	if got := findings(lineBreak.ReplaceAllString(text, ""), patterns); len(got) != 1 {
		t.Fatalf("across lines: %v", got)
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

func localDenylist(t *testing.T) ([]*regexp.Regexp, string) {
	t.Helper()
	path := os.Getenv("CERBERUS_DENYLIST")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "$CERBERUS_DENYLIST or ~/.config/cerberus/denylist.txt"
		}
		path = filepath.Join(home, ".config", "cerberus", "denylist.txt")
	}
	f, err := os.Open(path) //nolint:gosec // the operator's own denylist
	if err != nil {
		return nil, path
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
	return compile(t, exprs), path
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
