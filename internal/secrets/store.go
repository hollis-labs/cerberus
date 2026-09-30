package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"
)

// StoreUnavailableError is the OS credential store not being there to ask:
// no Secret Service on the session bus (a headless Linux box, a systemd user
// unit), a locked or refusing keychain, a Credential Manager the process
// cannot reach. It is not "not found", which would let a missing store look
// like a missing credential and hide the fix. Every surface reads it as
// credential_missing.
type StoreUnavailableError struct {
	// Op is read, write or delete.
	Op string
	// Name is the entry, <service>/<key>: a name by construction.
	Name  string
	Cause error
}

func (e *StoreUnavailableError) Error() string {
	return fmt.Sprintf("credential_missing: %s could not be %s because the OS credential store is unavailable here (%s). %s",
		e.Name, opPast(e.Op), e.Cause, storeRecovery(runtime.GOOS))
}

func (e *StoreUnavailableError) Unwrap() error { return e.Cause }

// ErrorCode is the error's code on every surface.
func (e *StoreUnavailableError) ErrorCode() string { return "credential_missing" }

func opPast(op string) string {
	switch op {
	case "write":
		return "stored"
	case "delete":
		return "removed"
	default:
		return "read"
	}
}

// storeRecovery names what makes the store available on goos. The
// environment variable is always an alternative for a credential, so it is
// named last. Worded to survive redact.Text: no "name: value" shapes.
func storeRecovery(goos string) string {
	const env = "Or supply the credential through its CERBERUS_<CONNECTOR>_<KEY> environment variable in the daemon's environment."
	switch goos {
	case "linux":
		return "Cerberus stores credentials through the Secret Service on the D-Bus session bus. Run it in a session with an unlocked keyring (GNOME Keyring, KWallet or KeePassXC with its Secret Service enabled). " + env
	case "darwin":
		return "The login keychain may be locked, or access to the entry was denied. Unlock it with `security unlock-keychain` and retry, allowing access if asked. " + env
	case "windows":
		return "Cerberus stores credentials in the signed-in user's Windows Credential Manager, so it must run as that user, not as a system service. " + env
	default:
		return "This platform has no credential store Cerberus can use. " + env
	}
}

// StoreLimitError is a value too large for the platform's credential store.
type StoreLimitError struct {
	Name     string
	Bytes    int
	Limit    int
	Platform string
}

func (e *StoreLimitError) Error() string {
	limit := ""
	if e.Limit > 0 {
		limit = fmt.Sprintf(" of about %d bytes", e.Limit)
	}
	return fmt.Sprintf("%s is %d bytes, over the %s credential store's limit%s, so nothing was stored. Store a reference to it instead (for example a keeper:// or op:// reference), or shorten it",
		e.Name, e.Bytes, platformName(e.Platform), limit)
}

func platformName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS keychain"
	case "windows":
		return "Windows Credential Manager"
	case "linux":
		return "Secret Service"
	}
	return goos
}

// Platform limits, as go-keyring enforces them.
const (
	// windowsValueLimit is Credential Manager's blob limit.
	windowsValueLimit = 2560
	// darwinCommandLimit is the security(1) interactive command go-keyring
	// writes: "add-generic-password -U -s <service> -a <account> -w
	// <value>", with the value base64-encoded behind a prefix, in 4096
	// bytes.
	darwinCommandLimit = 4096
	// linuxValueLimit is a sanity bound: the Secret Service has none, but a
	// credential this large is a mistake.
	linuxValueLimit = 100 << 10
)

// CheckStoreLimit refuses a value the platform's store would refuse, before
// anything is written, naming the limit.
func CheckStoreLimit(service, user, value string) error {
	return checkStoreLimit(runtime.GOOS, service, user, value)
}

func checkStoreLimit(goos, service, user, value string) error {
	switch goos {
	case "windows":
		if len(value) > windowsValueLimit {
			return &StoreLimitError{Name: user, Bytes: len(value), Limit: windowsValueLimit, Platform: goos}
		}
	case "darwin":
		// Conservative: each argument is counted as if quoted.
		encoded := len("go-keyring-base64:") + base64.StdEncoding.EncodedLen(len(value))
		command := len("add-generic-password -U -s  -a  -w \n") + len(service) + 2 + len(user) + 2 + encoded + 2
		if command > darwinCommandLimit {
			fixed := command - encoded
			raw := (darwinCommandLimit - fixed - len("go-keyring-base64:")) / 4 * 3
			return &StoreLimitError{Name: user, Bytes: len(value), Limit: raw, Platform: goos}
		}
	case "linux":
		if len(value) > linuxValueLimit {
			return &StoreLimitError{Name: user, Bytes: len(value), Limit: linuxValueLimit, Platform: goos}
		}
	}
	return nil
}

// storeMocked is set once a test binary has replaced the real store.
var storeMocked atomic.Bool

// errRealStoreInTest is a test reaching the operator's real credential store.
var errRealStoreInTest = errors.New("a test reached the real OS credential store; call secrets.MockStoreForTests() from the package's TestMain, so tests never read or write the operator's keychain")

// MockStoreForTests replaces the OS credential store with go-keyring's
// in-memory mock for the rest of the process. Every package whose tests can
// reach KeychainProvider calls it from TestMain.
func MockStoreForTests() {
	keyring.MockInit()
	storeMocked.Store(true)
}

// realStoreGuard refuses the real store inside a test binary that has not
// mocked it. Outside tests it is a no-op.
func realStoreGuard() error {
	if testing.Testing() && !storeMocked.Load() {
		return errRealStoreInTest
	}
	return nil
}
