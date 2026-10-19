// Package dataaccess owns the session-bound data contract. It never accepts a
// caller-asserted principal, database credential, SQL string or connection URL.
package dataaccess

import (
	"context"

	"github.com/hollis-labs/cerberus/pkg/plugin"
)

type proofKey struct{}
type Proof struct {
	Connector string
	Query     plugin.DataQuery
	Permit    plugin.DataPermit
}

// WithProof carries the verified service authorization after admission and audit.
func WithProof(ctx context.Context, p Proof) context.Context {
	return context.WithValue(ctx, proofKey{}, p)
}
func FromContext(ctx context.Context) (Proof, bool) {
	p, ok := ctx.Value(proofKey{}).(Proof)
	return p, ok
}
