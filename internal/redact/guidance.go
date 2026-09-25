package redact

import (
	"errors"
	"fmt"
)

// The regex net cannot read. Ten times it has eaten Cerberus's own guidance —
// a recovery instruction, an error code, a placeholder — because it ran over
// prose Cerberus composed as if it were text from somewhere else. Guidance is
// how Cerberus says which text is its own: it is rendered once, where it is
// made, with its prose kept and only its cause (vendor, remote or child text)
// passed through the rules.

// Renderer is an error that knows which part of its text is Cerberus's own.
// RenderRedacted returns that prose as it is, and anything else in its text —
// a wrapped cause — through s: the scope's values, then the rules.
type Renderer interface {
	RenderRedacted(s *Scope) string
}

// Guidance is an error whose text Cerberus composed: a refusal, a recovery
// instruction. Its arguments must be names by construction — an operation,
// a field, a connector id, an effect — never a value that came from a
// provider or a user's secret, because the rules never run over it. Values a
// request registered are still removed.
func Guidance(format string, args ...any) error {
	return guidanceError{prose: fmt.Sprintf(format, args...)}
}

// GuidanceWrap is Guidance with a cause: prose, then ": ", then the cause's
// text through the scope and the rules. Unwrap returns the cause.
func GuidanceWrap(cause error, format string, args ...any) error {
	return guidanceError{prose: fmt.Sprintf(format, args...), cause: cause}
}

// Prose declares that err's whole text is Cerberus-composed prose, for an
// error made where redact cannot be imported (pkg/connector's input checks).
// Unwrap returns err, so errors.Is and errors.As still see it.
func Prose(err error) error {
	if err == nil {
		return nil
	}
	return proseError{source: err}
}

type guidanceError struct {
	prose string
	cause error
}

func (e guidanceError) Error() string { return e.RenderRedacted(nil) }
func (e guidanceError) Unwrap() error { return e.cause }
func (e guidanceError) RenderRedacted(s *Scope) string {
	prose := s.ReplaceValues(e.prose)
	if e.cause == nil {
		return prose
	}
	return prose + ": " + Render(s, e.cause)
}

type proseError struct{ source error }

func (e proseError) Error() string                  { return e.source.Error() }
func (e proseError) Unwrap() error                  { return e.source }
func (e proseError) RenderRedacted(s *Scope) string { return s.ReplaceValues(e.source.Error()) }

// Render is err's operator-facing text: a Renderer renders itself, and any
// other error is its text through the scope and the rules.
func Render(s *Scope, err error) string {
	if err == nil {
		return ""
	}
	if r, ok := err.(Renderer); ok { //nolint:errorlint // only the outermost error composed the whole text
		return r.RenderRedacted(s)
	}
	return s.Text(err.Error())
}

// PreRendered is an error whose text was already rendered — its prose kept,
// its detail redacted — by whoever made it: in this process, or by a daemon
// that said so on the wire. ErrorText does not run the rules over it again.
type PreRendered interface {
	error
	PreRendered() bool
}

// ErrorText is the text to show for err at an edge that has no request scope,
// such as the CLI printing a returned error. A pre-rendered error, or a
// transparent wrapper around one whose text is the same, is shown as it is.
// Anything else gets the regex net.
func ErrorText(err error) string { return (*Scope)(nil).ErrorText(err) }

// ErrorText is ErrorText in s: a pre-rendered error loses the scope's values
// and skips the rules; anything else is rendered through s.
func (s *Scope) ErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	var pre PreRendered
	if errors.As(err, &pre) && pre.PreRendered() && pre.Error() == text {
		return s.ReplaceValues(text)
	}
	return Render(s, err)
}
