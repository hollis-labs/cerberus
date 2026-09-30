package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// statusPresence is whether cerberus-presence, the helper the daemon asks the
// person at the Mac through before a passkey enrollment, is installed where
// the daemon looks: next to the cerberus binary. Without it, enrolling a
// passkey is refused, and so is every out-of-band approval that needs one.
type statusPresence struct {
	// Needed is false off macOS, where enrollment is refused anyway.
	Needed    bool   `json:"needed"`
	Path      string `json:"path,omitempty"`
	Installed bool   `json:"installed"`
	Note      string `json:"note,omitempty"`
}

// presenceRecovery is how to put the helper in place.
const presenceRecovery = "passkey enrollment is refused without it; reinstall with `brew reinstall cerberus`, or `go install github.com/hollis-labs/cerberus/cmd/cerberus-presence@latest` so it sits next to cerberus"

// statusOfPresence checks for the helper next to this cerberus binary, which
// is where `cerberus install` points the daemon.
var statusOfPresence = func() statusPresence {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	return presenceAt(runtime.GOOS, exe, err)
}

func presenceAt(goos, exe string, exeErr error) statusPresence {
	if goos != "darwin" {
		return statusPresence{Note: "not used on " + goos + ": passkey enrollment needs macOS"}
	}
	if exeErr != nil {
		return statusPresence{Needed: true, Note: "cannot find this binary: " + exeErr.Error()}
	}
	p := statusPresence{Needed: true, Path: filepath.Join(filepath.Dir(exe), "cerberus-presence")}
	info, err := os.Stat(p.Path)
	switch {
	case err != nil:
		p.Note = presenceRecovery
	case info.IsDir() || info.Mode().Perm()&0o111 == 0:
		p.Note = "it is there but not executable; " + presenceRecovery
	default:
		p.Installed = true
	}
	return p
}

// presenceLine is the status line for the helper, and whether it is an alert.
func presenceLine(p statusPresence) (string, bool) {
	switch {
	case !p.Needed:
		return p.Note, false
	case p.Installed:
		return "cerberus-presence installed at " + p.Path, false
	case p.Path == "":
		return "! " + p.Note, true
	default:
		return "! cerberus-presence MISSING at " + p.Path + ": " + p.Note, true
	}
}
