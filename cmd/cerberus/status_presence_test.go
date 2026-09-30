package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cerberus status says whether the passkey helper is installed next to
// cerberus on macOS, and names the recovery when it is not.
func TestStatusReportsThePresenceHelper(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cerberus")
	helper := filepath.Join(dir, "cerberus-presence")

	missing := presenceAt("darwin", exe, nil)
	text, alert := presenceLine(missing)
	if missing.Installed || !alert || !strings.Contains(text, "MISSING at "+helper) || !strings.Contains(text, "brew reinstall cerberus") || !strings.Contains(text, "go install github.com/hollis-labs/cerberus/cmd/cerberus-presence@latest") {
		t.Fatalf("missing: %+v %q", missing, text)
	}

	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := presenceAt("darwin", exe, nil); p.Installed || !strings.Contains(p.Note, "not executable") {
		t.Fatalf("not executable: %+v", p)
	}

	if err := os.Chmod(helper, 0o700); err != nil { //nolint:gosec // the test's own helper
		t.Fatal(err)
	}
	installed := presenceAt("darwin", exe, nil)
	if text, alert := presenceLine(installed); !installed.Installed || alert || text != "cerberus-presence installed at "+helper {
		t.Fatalf("installed: %+v %q %v", installed, text, alert)
	}

	if p := presenceAt("linux", exe, nil); p.Needed || !strings.Contains(p.Note, "not used on linux") {
		t.Fatalf("linux: %+v", p)
	}
	if p := presenceAt("darwin", "", errors.New("no binary")); !p.Needed || p.Installed || !strings.Contains(p.Note, "no binary") {
		t.Fatalf("no exe: %+v", p)
	}

	// The line is in the status output.
	var out bytes.Buffer
	if err := writeStatus(&out, statusReport{Presence: missing}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "presence ! cerberus-presence MISSING at "+helper) {
		t.Fatalf("status output:\n%s", out.String())
	}
}
