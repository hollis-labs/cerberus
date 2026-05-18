package config

import (
	"fmt"

	"github.com/hollis-labs/go-apppaths/paths"
)

// appName is Cerberus's go-apppaths application identity. It drives the XDG
// roots (~/.local/share/cerberus, ~/.local/state/cerberus, …) and the
// CERBERUS_* env-var prefix go-apppaths reads natively (CERBERUS_DB_PATH,
// CERBERUS_WORKSPACE).
const appName = "cerberus"

// ResolveLayout resolves Cerberus's on-disk layout via go-apppaths in the
// default (XDG) mode. Project mode is deliberately not used.
//
// Scope note (CW-20260517-0065): this resolves ONLY the main database path
// (~/.local/share/cerberus/workspaces/default/main.db). Everything else under
// the legacy ~/.cerberus/ dotdir — config.yaml, registry.yaml,
// projects/*.cerberus.yaml, the apps/<project>/ install roots, sockets, pids,
// locks — STAYS where it is by design: it is the deploy substrate every other
// hollis-labs app's plists and migration runbooks point into, and is
// referenced by absolute path across the codebase. Do not route those through
// go-apppaths.
//
// WithLegacyNames is deliberately omitted: it only adopts XDG-root -> XDG-root
// by app name and cannot reach the hand-made ~/.cerberus/ dotdir. The old
// cerberus.db is empty (0 bytes, lazily opened, never populated) so there is
// nothing to adopt anyway.
//
// Callers that only introspect (the `cerberus path` subcommand) pass
// paths.WithoutMaterialize().
func ResolveLayout(extra ...paths.Option) (paths.Layout, error) {
	layout, err := paths.Resolve(appName, extra...)
	if err != nil {
		return paths.Layout{}, fmt.Errorf("resolve app paths: %w", err)
	}
	return layout, nil
}
