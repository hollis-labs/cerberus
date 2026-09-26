package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/redact"
)

type fakePasskeys struct {
	st      presence.Status
	digests []string
}

func (f *fakePasskeys) PasskeyStatus(context.Context) (presence.Status, error) { return f.st, nil }
func (f *fakePasskeys) PasskeyAllowEnrollment(_ context.Context, args cerbapi.EnrollAllowArgs) error {
	f.digests = append(f.digests, args.Digest)
	return nil
}

func passkeysFixture(t *testing.T, terminal bool) (*fakePasskeys, *[]string) {
	t.Helper()
	f := &fakePasskeys{}
	var pages []string
	oldTerm, oldClient, oldURL, oldBrowser := approvalsIsTerminal, newPasskeysClient, consolePageURL, approvalsEnrollFlags.browser
	approvalsIsTerminal = func() bool { return terminal }
	newPasskeysClient = func() (passkeysClient, error) { return f, nil }
	consolePageURL = func(next string) (string, error) {
		pages = append(pages, next)
		return "http://localhost:4783/login?token=t&next=" + next, nil
	}
	approvalsEnrollFlags.browser = false
	t.Cleanup(func() {
		approvalsIsTerminal, newPasskeysClient, consolePageURL, approvalsEnrollFlags.browser = oldTerm, oldClient, oldURL, oldBrowser
		approvalsEnrollFlags.label = ""
	})
	return f, &pages
}

// Enrollment and removal are made on a terminal, which hands the operator a
// console link; from a script or an agent nothing is minted.
func TestEnrollIsTerminalOnly(t *testing.T) {
	f, pages := passkeysFixture(t, false)
	for _, args := range [][]string{{"enroll"}, {"keys", "remove", "aa"}} {
		if _, err := runApprovals(t, "", args...); !errors.Is(err, errApprovalsNotInteractive) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(f.digests) != 0 || len(*pages) != 0 {
		t.Fatal("a refused enrollment minted a token or a link")
	}
}

func TestEnrollPrintsTheConsoleLinkWithItsToken(t *testing.T) {
	f, pages := passkeysFixture(t, true)
	out, err := runApprovals(t, "", "enroll", "--label", "touch id")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.digests) != 1 || len(*pages) != 1 {
		t.Fatalf("digests = %v, pages = %v", f.digests, *pages)
	}
	// The daemon was given the digest of the token the link carries, and
	// never the token.
	link, _ := url.Parse("http://x" + (*pages)[0])
	token := link.Query().Get("enroll")
	if token == "" || presence.EnrollTokenDigest(token) != f.digests[0] || strings.Contains(f.digests[0], token) || link.Query().Get("label") != "touch id" {
		t.Fatalf("page %s, digest %s", (*pages)[0], f.digests[0])
	}
	if !strings.Contains(out, "http://localhost:4783/login?token=t") {
		t.Fatalf("output lacks the link:\n%s", out)
	}
}

func TestKeysListsTheRegistry(t *testing.T) {
	f, _ := passkeysFixture(t, false)
	out, err := runApprovals(t, "", "keys")
	if err != nil || !strings.Contains(out, "run `cerberus approvals enroll`") {
		t.Fatalf("empty registry: %v\n%s", err, out)
	}
	f.st = presence.Status{State: presence.StateOK, Keys: []presence.KeyInfo{{Fingerprint: "a1b2c3d4", Label: "laptop"}}}
	out, err = runApprovals(t, "", "keys")
	if err != nil || !strings.Contains(out, "a1b2c3d4") || !strings.Contains(out, "laptop") {
		t.Fatalf("registry: %v\n%s", err, out)
	}
	summary, _ := presence.Status{}.Summary(f.st.LastEnrolledAt)
	if redact.Text(summary) != summary {
		t.Fatalf("the not-set-up guidance did not survive redaction: %q", redact.Text(summary))
	}
}
