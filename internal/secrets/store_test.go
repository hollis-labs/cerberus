package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/zalando/go-keyring"
)

// A test binary that has not mocked the store is refused the real one.
func TestTheRealStoreIsRefusedInTests(t *testing.T) {
	storeMocked.Store(false)
	t.Cleanup(func() { storeMocked.Store(true) })
	p := NewKeychainProvider()
	if _, err := p.Get(context.Background(), "github", "token"); !errors.Is(err, errRealStoreInTest) {
		t.Fatalf("Get = %v", err)
	}
	if err := p.Set(context.Background(), "github", "token", "x"); !errors.Is(err, errRealStoreInTest) {
		t.Fatalf("Set = %v", err)
	}
	if err := p.Delete(context.Background(), "github", "token"); !errors.Is(err, errRealStoreInTest) {
		t.Fatalf("Delete = %v", err)
	}
	// The environment is not the store, and still answers.
	t.Setenv("CERBERUS_GITHUB_TOKEN", "from-env")
	if v, err := p.Get(context.Background(), "github", "token"); err != nil || v != "from-env" {
		t.Fatalf("env = %q, %v", v, err)
	}
}

// A store that is not there is not a missing entry: it is credential_missing
// naming what makes the store available, and never reads as "".
func TestAnUnavailableStoreIsNotNotFound(t *testing.T) {
	keyring.MockInitWithError(errors.New("The name org.freedesktop.secrets was not provided by any .service files"))
	t.Cleanup(keyring.MockInit)
	p := NewKeychainProvider()
	value, err := p.Get(context.Background(), "cloudflare", "api_token")
	var unavailable *StoreUnavailableError
	if value != "" || !errors.As(err, &unavailable) || unavailable.ErrorCode() != "credential_missing" {
		t.Fatalf("Get = %q, %v", value, err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "credential_missing: cloudflare/api_token could not be read") || !strings.Contains(msg, "CERBERUS_<CONNECTOR>_<KEY>") {
		t.Fatalf("msg = %s", msg)
	}
	if err := p.Set(context.Background(), "cloudflare", "api_token", "v"); !errors.As(err, &unavailable) || unavailable.Op != "write" {
		t.Fatalf("Set = %v", err)
	}
	// Through the chain every connector resolves with, it stays that error.
	ref := NewReferenceProvider(p, "")
	if _, err := ref.Get(context.Background(), "cloudflare", "api_token"); !errors.As(err, &unavailable) {
		t.Fatalf("chain = %v", err)
	}
	// A real not-found is still "".
	keyring.MockInit()
	if value, err := p.Get(context.Background(), "cloudflare", "api_token"); value != "" || err != nil {
		t.Fatalf("not found = %q, %v", value, err)
	}
}

// Each platform's recovery survives redaction whole (AGENTS.md).
func TestStoreRecoveriesSurviveRedaction(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows", "plan9"} {
		msg := (&StoreUnavailableError{Op: "read", Name: "cloudflare/api_token", Cause: errors.New("no bus")}).Error()
		msg = strings.Replace(msg, storeRecovery("darwin"), storeRecovery(goos), 1)
		if got := redact.Text(msg); got != msg {
			t.Errorf("%s: redaction changed the recovery:\n  %s\n  %s", goos, msg, got)
		}
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		msg := (&StoreLimitError{Name: "keeper/ksm_config", Bytes: 5000, Limit: 2560, Platform: goos}).Error()
		if got := redact.Text(msg); got != msg {
			t.Errorf("%s limit: redaction changed it:\n  %s\n  %s", goos, msg, got)
		}
	}
}

func TestStoreLimits(t *testing.T) {
	sized := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		goos string
		n    int
		ok   bool
	}{
		{"windows", 2560, true},
		{"windows", 2561, false},
		{"darwin", 2900, true},
		{"darwin", 3100, false},
		{"linux", 100 << 10, true},
		{"linux", 100<<10 + 1, false},
	}
	for _, c := range cases {
		err := checkStoreLimit(c.goos, "cerberus", "keeper/ksm_config", sized(c.n))
		if (err == nil) != c.ok {
			t.Errorf("%s %d bytes: %v", c.goos, c.n, err)
		}
		var limit *StoreLimitError
		if err != nil && (!errors.As(err, &limit) || limit.Limit <= 0 || limit.Limit >= c.n || !strings.Contains(err.Error(), "nothing was stored")) {
			t.Errorf("%s %d bytes: %v", c.goos, c.n, err)
		}
	}
	// The darwin figure is what go-keyring would accept: a value at the
	// stated limit fits.
	err := checkStoreLimit("darwin", "cerberus", "keeper/ksm_config", sized(4000))
	var limit *StoreLimitError
	if !errors.As(err, &limit) || checkStoreLimit("darwin", "cerberus", "keeper/ksm_config", sized(limit.Limit)) != nil {
		t.Fatalf("darwin limit %v does not itself fit", err)
	}
}

// Set refuses an over-limit value before it touches the store.
func TestSetRefusesAnOverLimitValue(t *testing.T) {
	keyring.MockInit()
	p := NewKeychainProvider()
	if err := p.Set(context.Background(), "keeper", "ksm_config", strings.Repeat("a", 200<<10)); err == nil {
		t.Fatal("stored")
	}
	if v, _ := keyring.Get("cerberus", "keeper/ksm_config"); v != "" {
		t.Fatal("an over-limit value reached the store")
	}
}
