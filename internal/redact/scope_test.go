package redact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// sentinel is shaped like nothing the regex net knows: no provider prefix,
// no label, no Bearer. Only value redaction can remove it.
const sentinel = "q7Zr2mXv9pLw" //nolint:gosec // a test sentinel, not a credential

func TestScopeRemovesAnUnlabelledValueTheRegexNetMisses(t *testing.T) {
	msg := "vendor said: " + sentinel + " rejected"
	if got := Text(msg); !strings.Contains(got, sentinel) {
		t.Fatalf("precondition: the regex net alone should miss the sentinel, got %q", got)
	}
	s := NewScope()
	if !s.Add("github/token", sentinel) {
		t.Fatal("Add reported a 12-byte value as unprotected")
	}
	if got, want := s.Text(msg), "vendor said: "+Marker+" rejected"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
}

func TestScopeRemovesTheEscapedFormsAURLCarries(t *testing.T) {
	const value = "p@ss word/+=&x1" //nolint:gosec // a test sentinel
	s := NewScope()
	s.Add("svc/key", value)
	for _, form := range Forms(value) {
		msg := "GET https://api.example/v1?k=" + form + " failed"
		if got := s.Text(msg); strings.Contains(got, form) {
			t.Errorf("form %q survived: %q", form, got)
		}
	}
	if len(Forms(value)) < 2 {
		t.Fatalf("Forms(%q) = %q, want the escaped forms as well", value, Forms(value))
	}
}

func TestScopeReportsShortValuesByNameAndNeverMatchesThem(t *testing.T) {
	s := NewScope()
	if s.Add("svc/region", "us-east") {
		t.Fatal("Add covered a 7-byte value")
	}
	s.Add("svc/token", sentinel)
	if got := s.Unprotected(); len(got) != 1 || got[0] != "svc/region" {
		t.Fatalf("Unprotected = %q, want [svc/region]", got)
	}
	const msg = "no route in us-east for this account"
	if got := s.Text(msg); got != msg {
		t.Fatalf("a short value was cut out of prose: %q", got)
	}
}

func TestScopeDoesNotRegisterNamesOrReferences(t *testing.T) {
	s := NewScope()
	s.Add("svc/key", "keychain://cerberus/github/token")
	s.Add("svc/empty", "")
	if got := s.String(); got != "redact.Scope(0 values)" {
		t.Fatalf("a reference or empty value was registered: %s", got)
	}
	if got := s.Unprotected(); len(got) != 0 {
		t.Fatalf("Unprotected = %q, want none", got)
	}
}

// Without a scope, rendering is the regex net and nothing else, so a path
// that has not been given one behaves exactly as it did before.
func TestNilScopeIsTheRegexNet(t *testing.T) {
	var s *Scope
	s.Add("svc/key", sentinel)
	for _, msg := range []string{"token=abcdef123456", "vendor said: " + sentinel} {
		if got, want := s.Text(msg), Text(msg); got != want {
			t.Errorf("nil scope Text(%q) = %q, want %q", msg, got, want)
		}
	}
	if s.Error(nil) != nil {
		t.Fatal("Error(nil) != nil")
	}
	if ScopeFrom(context.Background()) != nil {
		t.Fatal("ScopeFrom on a bare context should be nil")
	}
}

func TestScopeTravelsOnContext(t *testing.T) {
	s := NewScope()
	ctx := WithScope(context.Background(), s)
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	ScopeFrom(child).Add("svc/key", sentinel)
	if got := s.Text(sentinel); got != Marker {
		t.Fatalf("a value registered through a derived context did not reach the scope: %q", got)
	}
}

// An error wrapped before the credential is resolved still loses it: Error
// renders through the scope when the message is read, not when it is wrapped.
func TestScopeErrorRendersAtReadTime(t *testing.T) {
	s := NewScope()
	err := s.Error(fmt.Errorf("dial: %s refused", sentinel))
	s.Add("svc/key", sentinel)
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, errors.Unwrap(err)) || errors.Unwrap(err) == nil {
		t.Fatal("the scoped error does not unwrap to its source")
	}
}

// Marshal hides values under sensitive keys already; the scope is what
// covers free text under keys that are not, where vendor text lands.
func TestScopeMarshalCoversFreeTextFields(t *testing.T) {
	s := NewScope()
	s.Add("svc/key", sentinel)
	data, err := s.Marshal(map[string]any{
		"error":   "upstream echoed " + sentinel,
		"command": []string{"tool", "--flag", sentinel},
		"nested":  []any{map[string]any{"stderr": sentinel}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("Marshal = %s", data)
	}
	indented, err := s.MarshalIndent(map[string]string{"message": sentinel}, "", "  ")
	if err != nil || strings.Contains(string(indented), sentinel) {
		t.Fatalf("MarshalIndent = %s, %v", indented, err)
	}
}

// A Scope holds credential values, so printing one, however it is printed,
// shows a count.
func TestScopeNeverPrintsItsValues(t *testing.T) {
	s := NewScope()
	s.Add("svc/key", sentinel)
	data, err := json.Marshal(map[string]any{"scope": s})
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s), string(data)} {
		if strings.Contains(out, sentinel) || strings.Contains(out, "svc/key") {
			t.Errorf("printed a value or name: %q", out)
		}
	}
}

// A recovery instruction is prose Cerberus composed; registering the
// credential it is about must not eat it.
func TestScopeKeepsRecoveryInstructionsIntact(t *testing.T) {
	s := NewScope()
	s.Add("github/token", sentinel)
	for _, msg := range []string{
		"credential_missing: set CERBERUS_GITHUB_TOKEN or run `cerberus secrets set github token`, then reload",
		"operation_failed: token rejected; check github/token and retry",
	} {
		if got := s.Text(msg); got != msg {
			t.Errorf("Text(%q) = %q", msg, got)
		}
	}
}

func TestScopeIsSafeForConcurrentUse(t *testing.T) {
	s := NewScope()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(2)
		go func() { defer wg.Done(); s.Add(fmt.Sprintf("svc/k%d", i), fmt.Sprintf("%s-%02d", sentinel, i)) }()
		go func() { defer wg.Done(); _ = s.Text("vendor said: " + sentinel) }()
	}
	wg.Wait()
	for i := range 32 {
		value := fmt.Sprintf("%s-%02d", sentinel, i)
		if got := s.Text(value); got != Marker {
			t.Fatalf("value %d not covered after concurrent registration: %q", i, got)
		}
	}
}
