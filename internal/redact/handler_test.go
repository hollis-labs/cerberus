package redact

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestHandlerRendersRecordsThroughTheScope(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewHandler(slog.NewJSONHandler(&buf, nil)))
	ctx, scope := EnsureScope(context.Background())
	scope.Add("svc/key", sentinel)

	logger.ErrorContext(ctx, "vendor said "+sentinel,
		"error", errors.New("dial: "+sentinel+" refused"),
		"stderr", "echo "+sentinel,
		"api_token", "not-a-scope-value-but-sensitive",
		slog.Group("step", "output", sentinel),
		"missing_secrets", "token",
	)
	out := buf.String()
	if strings.Contains(out, sentinel) || strings.Contains(out, "not-a-scope-value") {
		t.Fatalf("record leaked: %s", out)
	}
	if !strings.Contains(out, `"missing_secrets":"token"`) {
		t.Fatalf("a names-only field was redacted: %s", out)
	}
}

// Without a scope on the record's context, a record still gets the regex
// net; wrapping twice changes nothing.
func TestHandlerWithoutAScopeIsTheRegexNet(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(slog.NewJSONHandler(&buf, nil))
	if NewHandler(h) != h {
		t.Fatal("wrapping a Handler again should return it")
	}
	slog.New(h).With("bound", "token=abcdef1234567890").Info("upstream: token=abcdef1234567890")
	if out := buf.String(); strings.Contains(out, "abcdef1234567890") {
		t.Fatalf("record leaked: %s", out)
	}
}

// ReplaceValues is for encoded text: it removes values and applies no rule,
// so an encoded document still parses.
func TestReplaceValuesAppliesNoRules(t *testing.T) {
	s := NewScope()
	s.Add("svc/key", `q7Zr"2mXv9pLw`)
	doc := `{"token": "abc", "echo": "q7Zr\"2mXv9pLw"}`
	if got, want := s.ReplaceValues(doc), `{"token": "abc", "echo": "`+Marker+`"}`; got != want {
		t.Fatalf("ReplaceValues = %q, want %q", got, want)
	}
}
