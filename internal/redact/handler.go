package redact

import (
	"context"
	"log/slog"
)

// Handler is a slog.Handler that renders every record through the redaction
// scope on the record's context before the handler it wraps writes it: the
// message, every string attribute, and every error attribute's text. A record
// logged without a context, or on one with no scope, gets the regex net
// alone. Log with the *Context variants (InfoContext, ErrorContext) where a
// request's context is in hand, or its resolved credentials are not covered.
type Handler struct {
	next slog.Handler
}

// NewHandler wraps next. Wrapping a Handler again returns it unchanged.
func NewHandler(next slog.Handler) slog.Handler {
	if h, ok := next.(Handler); ok {
		return h
	}
	return Handler{next: next}
}

func (h Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h Handler) Handle(ctx context.Context, record slog.Record) error {
	scope := ScopeFrom(ctx)
	out := slog.NewRecord(record.Time, record.Level, scope.Text(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		out.AddAttrs(redactAttr(scope, attr))
		return true
	})
	return h.next.Handle(ctx, out)
}

// WithAttrs redacts attributes bound to a logger up front, with the regex
// net: they are bound once, outside any request.
func (h Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redacted[i] = redactAttr(nil, attr)
	}
	return Handler{next: h.next.WithAttrs(redacted)}
}

func (h Handler) WithGroup(name string) slog.Handler {
	return Handler{next: h.next.WithGroup(name)}
}

func redactAttr(scope *Scope, attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		if SensitiveKey(attr.Key) && !NamesOnlyKey(attr.Key) && !IsReference(value.String()) {
			return slog.String(attr.Key, Marker)
		}
		return slog.String(attr.Key, scope.Text(value.String()))
	case slog.KindGroup:
		group := value.Group()
		redacted := make([]any, len(group))
		for i, member := range group {
			redacted[i] = redactAttr(scope, member)
		}
		return slog.Group(attr.Key, redacted...)
	case slog.KindAny:
		if err, ok := value.Any().(error); ok && err != nil {
			return slog.String(attr.Key, scope.Text(err.Error()))
		}
	case slog.KindBool, slog.KindDuration, slog.KindFloat64, slog.KindInt64, slog.KindTime, slog.KindUint64, slog.KindLogValuer:
		// Not text: nothing to redact. A LogValuer is resolved above.
	}
	return slog.Attr{Key: attr.Key, Value: value}
}
