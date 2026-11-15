package legacyimport

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// The old tree's parsers are oldtree's, shared with export-legacy; these adapt them to the
// report (a refusal on r.Refusals, an absent file on r.Listed.Absent).

type (
	appliedFile = oldtree.Applied
	pauseMarker = oldtree.PauseMarker
	receipts    = oldtree.Receipts
)

func (r *Report) absorb(p oldtree.Problems) {
	r.Listed.Absent = append(r.Listed.Absent, p.Absent...)
	for _, x := range p.Refusals {
		r.refuse("%s", x)
	}
}

// readApplied parses runtime/applied.json (R9), strictly.
func readApplied(oldHome string, r *Report) appliedFile {
	var p oldtree.Problems
	out := oldtree.ReadApplied(oldHome, &p)
	r.absorb(p)
	return out
}

// readPause lists the pause markers at the snapshot's top level (R10).
func readPause(oldHome string, r *Report) []pauseMarker {
	var p oldtree.Problems
	out := oldtree.ReadPause(oldHome, &p)
	r.absorb(p)
	return out
}

// readReceipts parses the credential-issuance receipts (R11).
func readReceipts(oldHome string, r *Report) receipts {
	var p oldtree.Problems
	out := oldtree.ReadReceipts(oldHome, &p)
	r.absorb(p)
	return out
}

// importReceipts records every receipt; a unique violation is an error (never a merge).
func importReceipts(ctx context.Context, st *store.Store, rc receipts) error {
	keys := make([]string, 0, len(rc.ByFile))
	for k := range rc.ByFile {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := storage.RecordReceipt(ctx, st, rc.ByFile[k]); err != nil {
			return fmt.Errorf("credential-issuance/%s: %w", k, err)
		}
	}
	return nil
}

// verifyReceipts reads every issuance back and compares it with its files.
func verifyReceipts(ctx context.Context, st *store.Store, rc receipts) (rows int, err error) {
	want := map[string][]storage.Receipt{}
	for _, x := range rc.ByFile {
		want[x.IssuanceID] = append(want[x.IssuanceID], x)
	}
	for id, list := range want {
		got, err := storage.Receipts(ctx, st, id)
		if err != nil {
			return rows, err
		}
		rows += len(got)
		byState := map[string]storage.Receipt{}
		for _, g := range got {
			byState[g.State] = g
		}
		if len(got) != len(list) {
			return rows, fmt.Errorf("issuance %s: %d rows for %d receipt files", id, len(got), len(list))
		}
		for _, w := range list {
			g, ok := byState[w.State]
			if !ok || !g.At.Equal(w.At) || g.Connector != w.Connector || g.Operation != w.Operation || g.BindingDigest != w.BindingDigest ||
				!sameRefs(g.References, w.References) {
				return rows, fmt.Errorf("issuance %s %s: the row does not equal its receipt file", id, w.State)
			}
		}
	}
	var total int
	if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM credential_issuance_receipts`).Scan(&total); err != nil {
		return rows, err
	}
	if total != len(rc.ByFile) {
		return rows, fmt.Errorf("%d receipt rows for %d receipt files", total, len(rc.ByFile))
	}
	return rows, nil
}

func sameRefs(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
