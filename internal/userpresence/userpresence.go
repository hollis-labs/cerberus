// Package userpresence asks the person at the machine to prove they are
// there before the daemon does something only they should start (B1-b): a
// check the daemon raises itself, so a claim sent to its socket cannot
// stand in for it.
//
// On macOS the daemon runs cerberus-presence, which raises
// LocalAuthentication (Touch ID, or the account password) in the operator's
// login session. It raises the bar for a process running as the operator —
// it must replace the daemon or the helper, and the helper's digest is
// pinned when the daemon first uses it — but it is not a boundary against
// one, since neither binary is signed. Where it cannot be raised it
// refuses; it never falls back to a claim.
package userpresence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Verifier checks that a person is present.
type Verifier interface {
	// Verify asks the person to allow reason, and returns nil only when
	// they did.
	Verify(ctx context.Context, reason string) error
}

// ErrUnavailable is a check that could not be raised here: no helper, no
// login session, another OS. Nothing is allowed on it.
var ErrUnavailable = errors.New("the person at this Mac could not be asked")

// ErrRefused is a check the person refused or canceled, or that failed.
var ErrRefused = errors.New("the person at this Mac did not allow it")

// Recovery is what to do when the check is unavailable.
const Recovery = "run it from a terminal in this Mac's own login session, at its screen (not over ssh), with cerberus-presence installed next to cerberus"

// Helper runs the cerberus-presence binary at Path.
type Helper struct {
	Path string
	// Timeout bounds one check; zero is two minutes.
	Timeout time.Duration
	// OnPin is told the helper's digest the first time it is used, to
	// record it.
	OnPin func(sha256 string)

	mu     sync.Mutex
	pinned string
}

// Verify implements Verifier.
func (h *Helper) Verify(ctx context.Context, reason string) error {
	digest, err := fileDigest(h.Path)
	if err != nil {
		return fmt.Errorf("%w: cerberus-presence is not installed at %s (%w); %s", ErrUnavailable, h.Path, err, Recovery)
	}
	h.mu.Lock()
	switch {
	case h.pinned == "":
		h.pinned = digest
		if h.OnPin != nil {
			h.OnPin(digest)
		}
	case h.pinned != digest:
		pinned := h.pinned
		h.mu.Unlock()
		return fmt.Errorf("%w: cerberus-presence at %s changed since this daemon first used it (it was %s, it is %s); restart the daemon if you updated Cerberus", ErrRefused, h.Path, short(pinned), short(digest))
	}
	h.mu.Unlock()
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.Path, "--reason", reason) //nolint:gosec // the daemon's own helper, by absolute path
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	// A helper killed on the timeout does not hold the call open through a
	// child still writing to its stderr.
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	why := strings.TrimSpace(stderr.String())
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%w: nobody answered within %s", ErrRefused, timeout)
	case errors.As(err, &exit) && exit.ExitCode() == 2:
		return fmt.Errorf("%w: %s; %s", ErrUnavailable, orDefault(why, "LocalAuthentication is not available"), Recovery)
	default:
		return fmt.Errorf("%w: %s", ErrRefused, orDefault(why, err.Error()))
	}
}

// Refuse is the verifier where none can be raised: it refuses, with why.
type Refuse struct{ Why string }

// Verify implements Verifier.
func (r Refuse) Verify(context.Context, string) error {
	return fmt.Errorf("%w: %s; %s", ErrUnavailable, orDefault(r.Why, "this daemon cannot ask"), Recovery)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // the daemon's own helper
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func short(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
