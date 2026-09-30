package secrets

import (
	"context"
	"sync"
)

// Sources records, for one request, where each credential it resolved came
// from: names and sources only, never a value. The audit outcome carries it
// as credential_sources, so an operator can see that cloudflare/api_token
// came from 1Password through the onepassword plugin rather than from the
// OS credential store.
type Sources struct {
	mu sync.Mutex
	m  map[string]string
}

type sourcesKey struct{}

// WithSources returns ctx carrying a fresh collector.
func WithSources(ctx context.Context) (context.Context, *Sources) {
	s := &Sources{m: map[string]string{}}
	return context.WithValue(ctx, sourcesKey{}, s), s
}

// SourcesFrom is ctx's collector, or nil.
func SourcesFrom(ctx context.Context) *Sources {
	s, _ := ctx.Value(sourcesKey{}).(*Sources)
	return s
}

func (s *Sources) record(name, source string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = source
}

// Snapshot is a copy of what was recorded, or nil when nothing was.
func (s *Sources) Snapshot() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}
