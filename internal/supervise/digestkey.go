package supervise

import (
	"context"
	"errors"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// OpenAppliedKey is supervise mode's digest key, checked against the applied digests in the
// store before anything runs (plan section 6: the migration copies audit/.digest_key and
// records its fingerprint with the digests it imports).
//
//   - With applied digests in the store, the key must exist in auditDir and its fingerprint must
//     be the recorded one. Otherwise every digest would compare unequal and the monitor would
//     stop restarting those sessions, so the start is refused.
//   - With none, the key is opened (created when absent) and its fingerprint recorded, unless
//     one is recorded already, which must then match.
func OpenAppliedKey(ctx context.Context, st *store.Store, auditDir string, now time.Time) (audit.Digester, error) {
	n, err := storage.AppliedCount(ctx, st)
	if err != nil {
		return audit.Digester{}, err
	}
	recorded, haveRecord, err := storage.AppliedKey(ctx, st)
	if err != nil {
		return audit.Digester{}, err
	}
	var d audit.Digester
	if n > 0 || haveRecord {
		if d, err = audit.OpenExistingDigester(auditDir); err != nil {
			if errors.Is(err, audit.ErrNoDigestKey) {
				return audit.Digester{}, redact.GuidanceWrap(err, "cerberus.mode supervise refuses to start: the store holds %d applied digests but the digest key they were made with is missing. "+
					"Copy the old build's audit/.digest_key into %s (the migration's step), then start again", n, auditDir)
			}
			return audit.Digester{}, err
		}
	} else if d, err = audit.OpenDigester(auditDir); err != nil {
		return audit.Digester{}, err
	}
	switch {
	case haveRecord && recorded == d.Fingerprint():
		return d, nil
	case haveRecord || n > 0:
		return audit.Digester{}, redact.Guidance("cerberus.mode supervise refuses to start: the digest key in %s is not the key the store's %d applied digests were made with "+
			"(its fingerprint does not match the one the migration recorded). Copy the old build's audit/.digest_key there and migrate again, then start", auditDir, n)
	}
	if err := storage.SetAppliedKey(ctx, st, d.Fingerprint(), now); err != nil {
		return audit.Digester{}, err
	}
	return d, nil
}
