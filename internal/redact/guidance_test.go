package redact

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// assignmentProse is guidance the regex net eats: "token: rotated" reads as
// an assignment, so Text turns "rotated" into the marker.
const assignmentProse = "the plugin rejected its token: rotated keys need a reload"

func TestGuidanceKeepsItsProseWhereTextEatsIt(t *testing.T) {
	if Text(assignmentProse) == assignmentProse {
		t.Fatal("precondition: the regex net should eat this prose")
	}
	err := Guidance("the plugin rejected its %s: rotated keys need a reload", "token")
	for name, got := range map[string]string{"Error": err.Error(), "Render": Render(nil, err), "ErrorText": ErrorText(err)} {
		if got != assignmentProse {
			t.Errorf("%s = %q, want the prose intact", name, got)
		}
	}
	if got := Render(nil, errors.New(assignmentProse)); got == assignmentProse {
		t.Fatal("an unmarked error skipped the rules")
	}
}

// A wrapped cause is not Cerberus's prose: it gets the rules and the scope,
// and the prose in front of it does not.
func TestGuidanceWrapRendersOnlyItsCause(t *testing.T) {
	s := NewScope()
	s.Add("svc/key", sentinel)
	cause := errors.New("upstream said token=abcdef1234567890 for " + sentinel)
	err := GuidanceWrap(cause, "the plugin rejected its %s: rotated keys need a reload", "token")
	got := Render(s, err)
	if !strings.HasPrefix(got, assignmentProse+": ") {
		t.Fatalf("prose changed: %q", got)
	}
	if strings.Contains(got, "abcdef1234567890") || strings.Contains(got, sentinel) {
		t.Fatalf("cause not redacted: %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("GuidanceWrap does not unwrap to its cause")
	}
}

func TestProseMarksAnErrorMadeElsewhere(t *testing.T) {
	source := errors.New(assignmentProse)
	err := Prose(source)
	if Render(nil, err) != assignmentProse || !errors.Is(err, source) || Prose(nil) != nil {
		t.Fatalf("Prose: Render = %q, Is = %v", Render(nil, err), errors.Is(err, source))
	}
}

// Text a request rendered once is not run through the rules again at an edge
// — values only — while any other string is.
func TestRenderedTextSkipsTheRulesAtTheEdges(t *testing.T) {
	ctx, s := EnsureScope(context.Background())
	s.Add("svc/key", sentinel)
	rendered := Render(s, Guidance("the plugin rejected its %s: rotated keys need a reload (%s)", "token", sentinel))
	ScopeFrom(ctx).MarkRendered(rendered)

	want := assignmentProse + " (" + Marker + ")"
	if got := s.Text(rendered); got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	data, err := s.Marshal(map[string]string{"error": rendered, "other": assignmentProse})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"error":"`+assignmentProse) {
		t.Fatalf("a rendered field was re-ruled: %s", data)
	}
	if strings.Contains(string(data), `"other":"`+assignmentProse) {
		t.Fatalf("an unrendered string skipped the rules: %s", data)
	}
	if !s.IsRendered(rendered) || !s.IsRendered(want) || s.IsRendered(assignmentProse) {
		t.Fatal("IsRendered does not match the marked text, raw and as written")
	}
	var none *Scope
	none.MarkRendered(rendered)
	if none.IsRendered(rendered) {
		t.Fatal("a nil scope remembered something")
	}
}

type preRendered struct{ text string }

func (e preRendered) Error() string     { return e.text }
func (e preRendered) PreRendered() bool { return true }

// ErrorText trusts a pre-rendered error, and a transparent wrapper around
// one, and nothing else.
func TestErrorTextTrustsOnlyPreRenderedText(t *testing.T) {
	pre := preRendered{text: assignmentProse}
	if ErrorText(pre) != assignmentProse {
		t.Fatalf("pre-rendered text changed: %q", ErrorText(pre))
	}
	if got := ErrorText(wrapSame{pre}); got != assignmentProse {
		t.Fatalf("a transparent wrapper lost the trust: %q", got)
	}
	if got := ErrorText(errors.Join(errors.New("prefix"), pre)); strings.Contains(got, "rotated") {
		t.Fatalf("a wrapper that adds text kept the trust: %q", got)
	}
}

type wrapSame struct{ error }

func (w wrapSame) Unwrap() error { return w.error }
