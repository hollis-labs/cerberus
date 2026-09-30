package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

func TestHumanAccountsFromDirectoryServices(t *testing.T) {
	uids := []byte("_www 70\noperator 501\nguest 201\nalex 502\nbuild 503\nroot 0\n_spotlight 89\nrestricted 504\n")
	shells := []byte("_www /usr/bin/false\noperator /bin/zsh\nguest /bin/zsh\nalex /bin/zsh\nbuild /usr/bin/false\nroot /bin/sh\nrestricted /sbin/nologin\n")
	if got := dsclHumanAccounts(uids, shells, "operator"); !reflect.DeepEqual(got, []string{"alex"}) {
		t.Fatalf("got %v", got)
	}
}

func TestHumanAccountsFromPasswd(t *testing.T) {
	passwd := []byte("root:x:0:0:root:/root:/bin/bash\nme:x:1000:1000::/home/me:/bin/bash\nsam:x:1001:1001::/home/sam:/bin/zsh\nsvc:x:1002:1002::/:/usr/sbin/nologin\nnobody:x:65534:65534::/:/bin/sh\n# comment\n")
	if got := passwdHumanAccounts(passwd, "me"); !reflect.DeepEqual(got, []string{"sam"}) {
		t.Fatalf("got %v", got)
	}
}

// Without auth mcp-http refuses to start, leading with stdio; --no-auth runs
// only where no other account could reach the port, and names them where
// they could (H5).
func TestNoAuthNeedsASingleAccountMachine(t *testing.T) {
	saved, savedNoAuth, savedInsecure := otherHumanAccounts, mcpHTTPNoAuth, mcpHTTPInsecure
	t.Cleanup(func() { otherHumanAccounts, mcpHTTPNoAuth, mcpHTTPInsecure = saved, savedNoAuth, savedInsecure })
	const stdio = "for a local client, use `cerberus mcp` (stdio)"

	mcpHTTPNoAuth, mcpHTTPInsecure = false, false
	otherHumanAccounts = func() ([]string, error) { return nil, nil }
	err := checkNoAuth()
	if err == nil || !strings.HasPrefix(err.Error(), stdio) || !strings.Contains(err.Error(), "mcp-http token issue") {
		t.Fatalf("no auth, no flag: %v", err)
	}

	mcpHTTPNoAuth = true
	if err = checkNoAuth(); err != nil {
		t.Fatalf("--no-auth on a single-account machine: %v", err)
	}

	otherHumanAccounts = func() ([]string, error) { return []string{"alex", "sam"}, nil }
	err = checkNoAuth()
	if err == nil || !strings.HasPrefix(err.Error(), stdio) || !strings.Contains(err.Error(), "alex, sam") {
		t.Fatalf("--no-auth with other accounts: %v", err)
	}

	otherHumanAccounts = func() ([]string, error) { return nil, errors.New("dscl failed") }
	if err := checkNoAuth(); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("unreadable accounts: %v", err)
	}

	// --insecure-listen is no auth too, and gets the same check.
	mcpHTTPNoAuth, mcpHTTPInsecure = false, true
	otherHumanAccounts = func() ([]string, error) { return []string{"alex"}, nil }
	if err := checkNoAuth(); err == nil || !strings.Contains(err.Error(), "alex") {
		t.Fatalf("--insecure-listen with other accounts: %v", err)
	}
}

// Every refusal and the banner survive redaction whole (AGENTS.md).
func TestNoAuthGuidanceSurvivesRedaction(t *testing.T) {
	saved, savedNoAuth := otherHumanAccounts, mcpHTTPNoAuth
	t.Cleanup(func() { otherHumanAccounts, mcpHTTPNoAuth = saved, savedNoAuth })
	mcpHTTPNoAuth = true
	otherHumanAccounts = func() ([]string, error) { return []string{"alex"}, nil }
	for _, text := range []string{errMCPHTTPNeedsAuth.Error(), checkNoAuth().Error(), noAuthWarning("127.0.0.1:4785")} {
		if got := redact.Text(text); got != text {
			t.Errorf("redaction changed:\n  %s\n  %s", text, got)
		}
	}
}

// The help leads with the path that needs no auth at all.
func TestMCPHTTPHelpLeadsWithStdio(t *testing.T) {
	if !strings.HasPrefix(mcpHTTPCmd.Long, "For a local client, use `cerberus mcp` (stdio).") {
		t.Fatalf("help begins %q", mcpHTTPCmd.Long[:80])
	}
	if !strings.Contains(mcpHTTPCmd.Short, "cerberus mcp") {
		t.Fatalf("short = %q", mcpHTTPCmd.Short)
	}
}
