package main

import (
	"errors"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"
)

func TestResolveDaemonBinaryPathReturnsExecutablePath(t *testing.T) {
	got, err := resolveDaemonBinaryPath(func() (string, error) {
		return "/usr/local/bin/cerberus", nil
	})
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	if got != "/usr/local/bin/cerberus" {
		t.Fatalf("expected /usr/local/bin/cerberus, got %q", got)
	}
}

func TestResolveDaemonBinaryPathResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "real-cerberus")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	linkBin := filepath.Join(dir, "cerberus")
	if err := os.Symlink(realBin, linkBin); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := resolveDaemonBinaryPath(func() (string, error) {
		return linkBin, nil
	})
	if err != nil {
		t.Fatalf("resolve daemon binary path: %v", err)
	}
	// EvalSymlinks also resolves any symlinks in the parent path, so resolve
	// the expected real binary the same way before comparing.
	wantResolved, err := filepath.EvalSymlinks(realBin)
	if err != nil {
		t.Fatalf("eval symlinks on target: %v", err)
	}
	if got != wantResolved {
		t.Fatalf("expected symlink-resolved path %q, got %q", wantResolved, got)
	}
}

func TestResolveDaemonBinaryPathPropagatesExecutableError(t *testing.T) {
	_, err := resolveDaemonBinaryPath(func() (string, error) {
		return "", errors.New("boom")
	})
	if err == nil {
		t.Fatalf("expected error from executable() to propagate")
	}
}

// Without an EnvironmentVariables key launchd gives the daemon only
// /usr/bin:/bin:/usr/sbin:/sbin, where neither `go` nor `docker` lives. Every
// install produced a daemon that could not run a build.
func TestDaemonLaunchPathCarriesTheInstallingEnvironment(t *testing.T) {
	got := daemonLaunchPath("/opt/homebrew/bin:/Users/me/go/bin")
	want := "/opt/homebrew/bin:/Users/me/go/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if got != want {
		t.Fatalf("daemonLaunchPath:\n got: %s\nwant: %s", got, want)
	}
}

// launchd's own entries are the tail, not the head: they are the fallback for
// an odd install environment, not a preference over the user's toolchain.
func TestDaemonLaunchPathAlwaysKeepsTheLaunchdBaseline(t *testing.T) {
	for _, envPath := range []string{"", "/opt/homebrew/bin"} {
		got := daemonLaunchPath(envPath)
		for _, base := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
			if !slices.Contains(filepath.SplitList(got), base) {
				t.Errorf("daemonLaunchPath(%q) = %q, missing baseline %s", envPath, got, base)
			}
		}
	}
}

// A relative entry in a long-lived background job's PATH is a way to get
// arbitrary code run as the operator; duplicates are just noise.
func TestDaemonLaunchPathDropsRelativeAndDuplicateEntries(t *testing.T) {
	got := daemonLaunchPath(".:/opt/homebrew/bin::relative/bin:/opt/homebrew/bin:/usr/bin")
	want := "/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if got != want {
		t.Fatalf("daemonLaunchPath:\n got: %s\nwant: %s", got, want)
	}
}

// The plist is what launchd actually reads, so assert on the rendered
// document rather than only on the composed string.
func TestLaunchdPlistDeclaresDaemonPath(t *testing.T) {
	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var out strings.Builder
	err = tmpl.Execute(&out, launchdData{
		BinaryPath: "/usr/local/bin/cerberus",
		WorkingDir: "/Users/me",
		HomeDir:    "/Users/me",
		DaemonPath: html.EscapeString(daemonLaunchPath("/opt/homebrew/bin")),
	})
	if err != nil {
		t.Fatalf("execute template: %v", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"<string>/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered plist is missing %q:\n%s", want, rendered)
		}
	}
}

// A PATH entry containing an XML metacharacter must not produce a plist
// launchd cannot parse — an unparseable plist is a daemon that never starts.
func TestDaemonPathIsXMLEscaped(t *testing.T) {
	if got := html.EscapeString(daemonLaunchPath("/opt/tools & more/bin")); !strings.Contains(got, "&amp;") {
		t.Fatalf("expected escaped ampersand, got %q", got)
	}
}
