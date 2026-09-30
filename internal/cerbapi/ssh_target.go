package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hollis-labs/cerberus/internal/config"
)

// ResourceLookup returns the configured resource with the given id, from
// whichever config the serving process resolved.
type ResourceLookup func(id string) (*config.ResourceDef, bool)

// SetResourceLookup gives the service the resources SSH operations resolve
// against. Without one, SSH operations are refused.
func (s *ExternalConnectorService) SetResourceLookup(lookup ResourceLookup) {
	if s != nil {
		s.resources = lookup
	}
}

// ConfigResourceLookup looks resources up in a fixed, already-resolved config.
func ConfigResourceLookup(cfg *config.ConfigV2) ResourceLookup {
	return func(id string) (*config.ResourceDef, bool) {
		def := findResourceDef(cfg, id)
		return def, def != nil
	}
}

// resolveSSHTarget merges the configured resource's connection settings into
// an SSH operation's config. The key table has already refused every field
// but the operation's own (sshconn.OperationFields), on every surface
// including the in-process CLI, so the host, key and trust settings can only
// come from the resource.
func (s *ExternalConnectorService) resolveSSHTarget(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationArgs, error) {
	lookup := s.lookupFor(ctx)
	id := stringFromConfig(args.Config, "id", "")
	if id == "" {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, errors.New("id is required: pass a configured ssh resource id (see `cerberus resource list`)"))
	}
	if lookup == nil {
		return args, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("no resource configuration is loaded, so ssh resource ids cannot be resolved"))
	}
	def, ok := lookup(id)
	if !ok {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf("resource %q not found in config; run `cerberus resource list` to see available resources", id))
	}
	if def.Connector != "ssh" {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf("resource %q uses connector %q, not ssh", id, def.Connector))
	}

	merged := make(map[string]any, len(def.Config)+len(args.Config)+2)
	for key, value := range def.Config {
		merged[key] = value
	}
	for key, value := range args.Config {
		merged[key] = value
	}
	merged["id"] = def.ID
	merged["name"] = def.Name
	args.Config = merged
	return args, nil
}

// resourceLookupKey carries a call's resource lookup (M10).
type resourceLookupKey struct{}

// withResourceLookup is ctx carrying lookup as its call's resource lookup.
func withResourceLookup(ctx context.Context, lookup func(string) (*config.ResourceDef, bool)) context.Context {
	return context.WithValue(ctx, resourceLookupKey{}, lookup)
}

// lookupFor is the resource lookup a call resolves through: the one its
// Execute began with, or the service's.
func (s *ExternalConnectorService) lookupFor(ctx context.Context) func(string) (*config.ResourceDef, bool) {
	if lookup, ok := ctx.Value(resourceLookupKey{}).(func(string) (*config.ResourceDef, bool)); ok && lookup != nil {
		return lookup
	}
	return s.resources
}

// onceLookup resolves each id through lookup once, and answers every later
// lookup of it from that: the definition a call was checked against is the
// one it runs.
func onceLookup(lookup func(string) (*config.ResourceDef, bool)) func(string) (*config.ResourceDef, bool) {
	if lookup == nil {
		return nil
	}
	type found struct {
		def *config.ResourceDef
		ok  bool
	}
	var mu sync.Mutex
	seen := map[string]found{}
	return func(id string) (*config.ResourceDef, bool) {
		mu.Lock()
		defer mu.Unlock()
		if f, ok := seen[id]; ok {
			return f.def, f.ok
		}
		def, ok := lookup(id)
		if def != nil {
			copied := *def
			def = &copied
		}
		seen[id] = found{def, ok}
		return def, ok
	}
}
