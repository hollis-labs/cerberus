package legacyimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/storage"
)

// appliedFile is runtime/applied.json parsed (R9): resource id to "hmac-sha256:<hex>", verbatim.
type appliedFile struct {
	present bool
	digests map[string]string
	mtime   time.Time
	sha256  string
}

var digestPattern = regexp.MustCompile(`^hmac-sha256:[0-9a-f]{64}$`)

// readApplied parses runtime/applied.json strictly: a JSON object of non-empty ids to digests in
// the old format. Anything else is refused, never guessed at.
func readApplied(oldHome string, r *Report) appliedFile {
	p := filepath.Join(oldHome, "runtime", "applied.json")
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		r.Listed.Absent = append(r.Listed.Absent, "runtime/applied.json")
		return appliedFile{}
	}
	if err != nil {
		r.refuse("runtime/applied.json: %v", err)
		return appliedFile{}
	}
	if !fi.Mode().IsRegular() {
		r.refuse("runtime/applied.json is not a regular file")
		return appliedFile{}
	}
	data, err := os.ReadFile(p) //nolint:gosec // inside the snapshot
	if err != nil {
		r.refuse("runtime/applied.json: %v", err)
		return appliedFile{}
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(&raw); err != nil || raw == nil || dec.More() {
		r.refuse("runtime/applied.json is not a JSON object of resource id to digest (%v); fix or remove it in the snapshot, the import does not guess", err)
		return appliedFile{}
	}
	out := appliedFile{present: true, digests: map[string]string{}, mtime: fi.ModTime()}
	for id, v := range raw {
		s, ok := v.(string)
		switch {
		case strings.TrimSpace(id) == "":
			r.refuse("runtime/applied.json has an empty resource id")
		case !ok:
			r.refuse("runtime/applied.json: the digest of %q is a %T, not a string", id, v)
		case !digestPattern.MatchString(s):
			r.refuse("runtime/applied.json: the digest of %q is not hmac-sha256:<64 hex>", id)
		default:
			out.digests[id] = s
		}
	}
	out.sha256, _ = fileSHA256(p)
	return out
}

// pauseMarker is one paused or paused.<id> file (R10).
type pauseMarker struct {
	name   string
	target string
	mtime  time.Time
}

// readPause lists the pause markers at the snapshot's top level. A directory or link named
// paused* is refused: the old build only ever wrote files.
func readPause(oldHome string, r *Report) []pauseMarker {
	entries, err := os.ReadDir(oldHome)
	if err != nil {
		r.refuse("read %s: %v", oldHome, err)
		return nil
	}
	var out []pauseMarker
	for _, e := range entries {
		name := e.Name()
		if name != "paused" && !strings.HasPrefix(name, "paused.") {
			continue
		}
		fi, err := os.Lstat(filepath.Join(oldHome, name))
		if err != nil {
			r.refuse("%s: %v", name, err)
			continue
		}
		if !fi.Mode().IsRegular() {
			r.refuse("%s is a %s; a pause marker is a file", name, fi.Mode().Type())
			continue
		}
		target := storage.AllTargets
		if name != "paused" {
			target = strings.TrimPrefix(name, "paused.")
			if target == "" {
				r.refuse("%s names no target", name)
				continue
			}
		}
		out = append(out, pauseMarker{name: name, target: target, mtime: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].target < out[j].target })
	return out
}

// oldReceipt is a credential-issuance receipt file as the old broker wrote it (R11).
type oldReceipt struct {
	ID            string            `json:"id"`
	Connector     string            `json:"connector"`
	Operation     string            `json:"operation"`
	BindingDigest string            `json:"binding_digest"`
	State         string            `json:"state"`
	References    map[string]string `json:"references"`
	At            time.Time         `json:"at"`
}

var (
	issuanceID    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	receiptStates = map[string]bool{"preparing": true, "prepared": true, "dispatching": true, "completed": true, "uncertain": true}
)

// receipts are the parsed receipts, and the reservation directories with none.
type receipts struct {
	byFile map[string]storage.Receipt // "<id>/<state>.json"
	empty  []string
	ids    int
}

// readReceipts parses credential-issuance/<id>/<state>.json, skipping secrets/ (the issued
// values, in place). A name, state or id that does not match its path is refused.
func readReceipts(oldHome string, d Decisions, r *Report) receipts {
	out := receipts{byFile: map[string]storage.Receipt{}}
	root := filepath.Join(oldHome, "credential-issuance")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		r.Listed.Absent = append(r.Listed.Absent, "credential-issuance/")
		return out
	}
	if err != nil {
		r.refuse("credential-issuance: %v", err)
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if name == "secrets" {
			continue
		}
		if !e.IsDir() || !issuanceID.MatchString(name) {
			r.refuse("credential-issuance/%s is not a reservation directory (32 lowercase hex)", name)
			continue
		}
		out.ids++
		files, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			r.refuse("credential-issuance/%s: %v", name, err)
			continue
		}
		if len(files) == 0 {
			out.empty = append(out.empty, name)
			if d.EmptyReservations == DecisionRefuse {
				r.refuse("credential-issuance/%s is a reservation without a receipt", name)
			}
			continue
		}
		for _, f := range files {
			rel := name + "/" + f.Name()
			state := strings.TrimSuffix(f.Name(), ".json")
			if !f.Type().IsRegular() || !strings.HasSuffix(f.Name(), ".json") || !receiptStates[state] {
				r.refuse("credential-issuance/%s is not a receipt (<state>.json, state one of preparing, prepared, dispatching, completed, uncertain)", rel)
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, name, f.Name())) //nolint:gosec // inside the snapshot
			if err != nil {
				r.refuse("credential-issuance/%s: %v", rel, err)
				continue
			}
			var rc oldReceipt
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err = dec.Decode(&rc); err != nil {
				r.refuse("credential-issuance/%s does not parse as a receipt: %v", rel, err)
				continue
			}
			if rc.ID != name || rc.State != state {
				r.refuse("credential-issuance/%s records id %q and state %q, which do not match its path", rel, rc.ID, rc.State)
				continue
			}
			if rc.At.IsZero() || rc.Connector == "" || rc.Operation == "" {
				r.refuse("credential-issuance/%s lacks its time, connector or operation", rel)
				continue
			}
			out.byFile[rel] = storage.Receipt{IssuanceID: rc.ID, State: rc.State, Connector: rc.Connector, Operation: rc.Operation,
				BindingDigest: rc.BindingDigest, References: rc.References, At: rc.At}
		}
	}
	sort.Strings(out.empty)
	return out
}

// importReceipts records every receipt; a unique violation is an error (never a merge).
func importReceipts(ctx context.Context, st *store.Store, rc receipts) error {
	keys := make([]string, 0, len(rc.byFile))
	for k := range rc.byFile {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := storage.RecordReceipt(ctx, st, rc.byFile[k]); err != nil {
			return fmt.Errorf("credential-issuance/%s: %w", k, err)
		}
	}
	return nil
}

// verifyReceipts reads every issuance back and compares it with its files.
func verifyReceipts(ctx context.Context, st *store.Store, rc receipts) (rows int, err error) {
	want := map[string][]storage.Receipt{}
	for _, x := range rc.byFile {
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
	if total != len(rc.byFile) {
		return rows, fmt.Errorf("%d receipt rows for %d receipt files", total, len(rc.byFile))
	}
	return rows, nil
}

func sameRefs(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
