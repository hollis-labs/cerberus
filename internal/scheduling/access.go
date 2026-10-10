package scheduling

import (
	"context"
	"database/sql"
)

// AccessCheck is a trusted serving dependency, never decoded from a request.
// It rechecks the host's authenticated proof and grant after storage waits.
type AccessCheck func(context.Context) error
type accessCheckKey struct{}
type quotaKey struct{}

func WithAccessCheck(ctx context.Context, check AccessCheck, maxJobs int) context.Context {
	ctx = context.WithValue(ctx, accessCheckKey{}, check)
	return context.WithValue(ctx, quotaKey{}, maxJobs)
}
func recheckAccess(ctx context.Context) error {
	if check, ok := ctx.Value(accessCheckKey{}).(AccessCheck); ok {
		return check(ctx)
	}
	return nil // Programmatic core callers remain trusted host dependencies.
}

// Durable completion/outbox/log bookkeeping uses writeTx directly, so a caller
// revocation cannot prevent recording a refused or uncertain outcome.
func (c *Core) writeAccessTx(ctx context.Context) (*sql.Tx, func(), error) {
	tx, done, err := c.writeTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err = recheckAccess(ctx); err != nil {
		done()
		return nil, nil, err
	}
	return tx, done, nil
}
func commitAccess(ctx context.Context, tx *sql.Tx) error {
	if err := recheckAccess(ctx); err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Core) checkQuota(ctx context.Context, tx *sql.Tx, app string, added int) error {
	limit, ok := ctx.Value(quotaKey{}).(int)
	if !ok {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gosched_schedules WHERE substr(id,1,length(?)+1)=?||'/'`, app, app).Scan(&count); err != nil {
		return err
	}
	if limit < 1 || count+added > limit {
		return Refusal("quota_exceeded", "app job quota would be exceeded")
	}
	return nil
}
