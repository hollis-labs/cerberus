package redact

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"sync"
)

// MinValueLength is the shortest resolved value a value redactor looks for.
// Value redaction matches anywhere in the text, so a short or common value —
// a region, an IP octet, "true" — would be cut out of every sentence that
// happens to contain it, recovery instructions included. A credential worth
// protecting this way is longer than this.
const MinValueLength = 8

// Forms returns the strings value takes in text a connector might render:
// raw, and the escaped forms it takes in a URL, which is where a transport
// error echoes it back. It returns nil for a value too short to redact safely
// (see MinValueLength) and for a secret reference, which is a name.
func Forms(value string) []string {
	if len(value) < MinValueLength || IsReference(value) {
		return nil
	}
	forms := []string{value}
	for _, escaped := range []string{url.QueryEscape(value), url.PathEscape(value)} {
		if !slices.Contains(forms, escaped) {
			forms = append(forms, escaped)
		}
	}
	return forms
}

// Scope is the value redactor for one request. Credentials are registered
// where they are resolved, while they are still values, and every error and
// log path on that request renders through it — so a credential cannot reach
// text even when a vendor SDK composes the message and gives it no label.
// Text, Error and Marshal remove the registered values first and then apply
// the regex net, which stays as the last resort for text Cerberus did not
// resolve.
//
// A nil *Scope is valid and is the regex net alone, so a path without a
// request scope renders exactly as it did before scopes existed. A Scope is
// safe for concurrent use: a request's goroutines share it.
type Scope struct {
	mu          sync.Mutex
	values      map[string]struct{}
	unprotected map[string]struct{}
	redactor    *Redactor
}

func NewScope() *Scope { return &Scope{} }

// Add registers the value of the credential called name. It reports whether
// the value is now covered: a value shorter than MinValueLength is not, and
// its name is kept for Unprotected so a caller can say so. The name is a
// label for that report and is never matched against text. An empty value
// or a secret reference registers nothing and is not reported.
func (s *Scope) Add(name, value string) bool {
	if s == nil || value == "" || IsReference(value) {
		return false
	}
	forms := Forms(value)
	s.mu.Lock()
	defer s.mu.Unlock()
	if forms == nil {
		if s.unprotected == nil {
			s.unprotected = map[string]struct{}{}
		}
		s.unprotected[name] = struct{}{}
		return false
	}
	if s.values == nil {
		s.values = map[string]struct{}{}
	}
	for _, form := range forms {
		if _, ok := s.values[form]; !ok {
			s.values[form] = struct{}{}
			s.redactor = nil
		}
	}
	return true
}

// Unprotected names, sorted, the credentials registered with a value too
// short to redact.
func (s *Scope) Unprotected() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.unprotected))
	for name := range s.unprotected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Redactor is a snapshot of the scope: the values registered so far, then
// the regex net. A value registered after the snapshot is not in it, so
// render through the Scope rather than holding one.
func (s *Scope) Redactor() Redactor {
	if s == nil {
		return Redactor{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.redactor == nil {
		values := make([]string, 0, len(s.values))
		for value := range s.values {
			values = append(values, value)
		}
		r := New(values...)
		s.redactor = &r
	}
	return *s.redactor
}

func (s *Scope) Text(value string) string    { return s.Redactor().Text(value) }
func (s *Scope) Args(args []string) []string { return s.Redactor().Args(args) }

// Error renders err through the scope at the time Error() is called, so a
// value registered after the error was wrapped is still removed.
func (s *Scope) Error(err error) error {
	if err == nil {
		return nil
	}
	return scopedError{source: err, scope: s}
}

func (s *Scope) Marshal(value any) ([]byte, error) { return s.Redactor().Marshal(value) }
func (s *Scope) MarshalIndent(value any, prefix, indent string) ([]byte, error) {
	return s.Redactor().MarshalIndent(value, prefix, indent)
}
func (s *Scope) JSON(data []byte) ([]byte, error) { return s.Redactor().JSON(data) }

// String prints a count, never a value: a Scope holds credentials, so every
// way of printing one — String for %v and %s, GoString for %#v, and
// MarshalJSON — prints this instead.
func (s *Scope) String() string {
	if s == nil {
		return "redact.Scope(nil)"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("redact.Scope(%d values)", len(s.values))
}
func (s *Scope) GoString() string             { return s.String() }
func (s *Scope) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

type scopedError struct {
	source error
	scope  *Scope
}

func (e scopedError) Error() string { return e.scope.Text(e.source.Error()) }
func (e scopedError) Unwrap() error { return e.source }

type scopeKey struct{}

// WithScope carries s on ctx. The request entry points create one per
// request; resolution registers into it and rendering reads it back.
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// EnsureScope returns ctx carrying a scope, and that scope: the one ctx
// already has, or a new one. A request has one scope, so an entry point
// reached from inside another request — the web console's in-process
// service, a CLI command that calls a helper that marks itself — joins the
// scope it is already in instead of starting one that the outer render
// edges cannot see.
func EnsureScope(ctx context.Context) (context.Context, *Scope) {
	if s := ScopeFrom(ctx); s != nil {
		return ctx, s
	}
	s := NewScope()
	return WithScope(ctx, s), s
}

// ScopeFrom returns ctx's scope, or nil — which renders as the regex net
// alone — when there is none. Render with ScopeFrom(ctx).Text(msg).
func ScopeFrom(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	return s
}
