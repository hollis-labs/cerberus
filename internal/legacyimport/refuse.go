package legacyimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
)

// preflight checks everything that can refuse the run, before anything is written: the paths
// (never the live tree; a target that is empty and outside the old roots), the snapshot's shape,
// the old database, the anchors, and every file the run will transform. Refusals are collected
// on the report so the operator sees all of them at once. It reads only.
func (m *migration) preflight(ctx context.Context) error {
	o, r := m.o, m.r
	for _, p := range []struct{ name, path string }{{"--old-home", o.OldHome}, {"--old-db", o.OldDB}, {"the old anchor account", o.OldAnchorAccount},
		{"the data directory", o.DataDir}, {"database.sqlite.path", o.DBPath}, {"the config directory", o.ConfigDir}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			r.refuse("%s must be an absolute path (got %q)", p.name, p.path)
		}
	}
	if len(r.Refusals) > 0 {
		return nil
	}

	// The source is a snapshot and a backup, never the live tree.
	roots := append(append([]string(nil), o.OldRoots...), o.ConfigDir)
	for _, src := range []struct{ name, path string }{{"--old-home", o.OldHome}, {"--old-db", o.OldDB}} {
		for _, root := range roots {
			rr, sp := resolve(root), resolve(src.path)
			if within(rr, sp) || within(sp, rr) {
				r.refuse("%s %s overlaps %s, a live tree: migrate-from reads only a snapshot (cp -a of ~/.cerberus) and a .backup of main.db, never the live tree", src.name, src.path, root)
			}
		}
	}
	if within(resolve(o.OldHome), resolve(o.OldDB)) {
		r.warn("--old-db lies inside --old-home; it is read as the backup and not listed again")
	}

	// The target: outside the old roots and the source, empty, with nothing staged.
	if err := datadir.RefuseOldPaths(datadir.Dirs{Data: o.DataDir}, o.DBPath, o.OldRoots); err != nil {
		r.refuse("%v", err)
	}
	for _, src := range []string{o.OldHome, o.OldDB} {
		if within(resolve(src), resolve(o.DataDir)) || within(resolve(o.DataDir), resolve(src)) {
			r.refuse("the data directory %s overlaps the source %s", o.DataDir, src)
		}
	}
	if !within(o.DataDir, o.DBPath) || o.DBPath == o.DataDir {
		r.refuse("database.sqlite.path %s is outside the data directory %s; migrate-from builds the database inside it (use a relative path)", o.DBPath, o.DataDir)
	}
	if fi, err := os.Lstat(o.DataDir); err == nil {
		empty, derr := dirEmpty(o.DataDir)
		switch {
		case !fi.IsDir():
			r.refuse("the data directory %s exists and is not a directory", o.DataDir)
		case derr != nil:
			r.refuse("the data directory %s: %v", o.DataDir, derr)
		case !empty:
			r.refuse("the data directory %s is not empty: migrate-from writes a new data directory and never merges (point %s at a new directory)", o.DataDir, datadir.Env)
		case fi.Mode().Perm()&0o077 != 0:
			r.refuse("the data directory %s is mode %#o; it must be private (0700) or absent", o.DataDir, fi.Mode().Perm())
		}
	}
	for _, p := range []string{m.partial, m.cfgStage, o.DBPath + ".open.lock", o.DBPath + "-wal", o.DBPath + "-shm"} {
		if exists(p) {
			r.refuse("%s exists: a previous or running instance left it; inspect it and remove it", p)
		}
	}
	for _, p := range []string{filepath.Join(o.ConfigDir, "policy"), filepath.Join(o.ConfigDir, "connector-secrets.yaml")} {
		if exists(p) {
			r.refuse("%s exists: migrate-from never replaces the new build's policy or secret references", p)
		}
	}
	rel, _ := filepath.Rel(o.DataDir, o.DBPath)
	m.stagedDB = filepath.Join(m.partial, rel)

	// The snapshot's shape.
	if fi, err := os.Stat(o.OldHome); err != nil || !fi.IsDir() {
		r.refuse("--old-home %s is not a directory (%v)", o.OldHome, err)
		return nil
	}
	if fi, err := os.Lstat(o.OldDB); err != nil || !fi.Mode().IsRegular() {
		r.refuse("--old-db %s is not a regular file (%v)", o.OldDB, err)
		return nil
	}
	if exists(filepath.Join(o.OldHome, "standby.d")) {
		r.refuse("the snapshot holds standby.d/, old's own record of the window: take the snapshot without it (it is archived at F, never imported at D)")
	}
	if len(r.Refusals) > 0 {
		return nil
	}

	// R4 and R5: the chain, its key and its anchor.
	if err := m.preflightAudit(); err != nil {
		return err
	}

	// R6: the approvals store reads whole, by reading only (approval.Open would write).
	if src := filepath.Join(o.OldHome, "approvals"); exists(filepath.Join(src, approval.FileName)) {
		batch, err := approval.ReadFrom(src, approval.Cursor{}, 0)
		switch {
		case err != nil:
			r.refuse("approvals/%s: %v", approval.FileName, err)
		case batch.Stop != nil:
			r.refuse("approvals/%s does not read whole: %v", approval.FileName, batch.Stop)
		case len(batch.Damaged) > 0:
			r.warn("approvals/%s has %d damaged line(s) the store's fold passes over; they are copied as they are", approval.FileName, len(batch.Damaged))
		}
	}

	// R1 to R3.
	main, err := inspectOldDB(ctx, o.OldDB, r)
	if err != nil {
		r.refuse("%v", err)
		return nil
	}
	r.MainDB = main

	// R9 to R11, R13, R14, R15, the listings.
	m.applied = readApplied(o.OldHome, r)
	m.pause = readPause(o.OldHome, r)
	m.receipts = readReceipts(o.OldHome, m.d, r)
	m.fragment, r.Config = transformConfig(o.OldHome, o.Home, r)
	r.Registry, m.resolved = inspectRegistry(o.OldHome, r)
	r.Hosts = inspectHosts(o.OldHome, r)
	listTree(o.OldHome, m.d, r)
	if digests := pluginDigests(o.OldHome); len(digests) > 0 {
		r.PluginDigests = digests
	}
	for _, f := range []string{"connector-secrets.yaml"} {
		if fi, err := os.Lstat(filepath.Join(o.OldHome, f)); err == nil && !fi.Mode().IsRegular() {
			r.refuse("%s is not a regular file", f)
		}
	}
	if fi, err := os.Lstat(filepath.Join(o.OldHome, "policy")); err == nil && !fi.IsDir() {
		r.refuse("policy is not a directory")
	}
	m.unknownInside("state", map[string]bool{"hosts.json": true, "overview_snapshots.json": true})
	m.unknownInside("runtime", map[string]bool{"applied.json": true})
	m.unknownInside("approvals", map[string]bool{approval.FileName: true, "passkeys": true})

	// The D3 manifest, when given: the snapshot and the backup must be what it recorded.
	if o.Manifest != "" {
		if err := m.checkManifest(ctx); err != nil {
			return err
		}
	}
	return nil
}

// preflightAudit is R4 and R5's refusals.
func (m *migration) preflightAudit() error {
	o, r := m.o, m.r
	dir := filepath.Join(o.OldHome, "audit")
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		r.refuse("the snapshot has no audit/ directory (%v)", statErr)
		return nil //nolint:nilerr // a refusal, recorded on the report
	}
	key, err := os.Lstat(filepath.Join(dir, ".digest_key"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.refuse("audit/.digest_key is missing from the snapshot: the applied digests and approval seals need it, and the import never creates one")
	case err != nil:
		r.refuse("audit/.digest_key: %v", err)
	case !key.Mode().IsRegular() || key.Size() != 32:
		r.refuse("audit/.digest_key is not a 32-byte file")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		chain := strings.HasSuffix(n, ".jsonl") && e.Type().IsRegular()
		if n != ".digest_key" && n != ".lock" && !chain {
			r.warn("audit/%s is not part of the chain; it is left in place and not copied", n)
		}
	}

	raw, head, ok, err := readAnchor(o.OldAnchorAccount)
	switch {
	case err != nil:
		r.refuse("%v", err)
		return nil
	case !ok:
		r.refuse("there is no %s keychain item for %s: without the old anchor the chain's head cannot be carried (check --old-anchor-account; audit verify would treat a missing anchor as no problem, the import does not)", auditchain.AnchorService, o.OldAnchorAccount)
		return nil
	}
	m.oldAnchorRaw, m.oldHead = raw, head
	if _, verdict := checkChain(dir, auditchain.KeychainAnchor{Dir: o.OldAnchorAccount}); verdict != "ok" {
		r.refuse("the snapshot's audit chain does not verify against the old anchor: %s", verdict)
	}
	newAccount := filepath.Join(o.DataDir, "audit")
	existing, _, present, err := readAnchor(newAccount)
	switch {
	case err != nil && present:
		r.refuse("the keychain already holds an unreadable %s item for %s: %v", auditchain.AnchorService, newAccount, err)
	case err != nil:
		return err
	case present && existing != raw:
		r.refuse("the keychain already holds a different %s item for %s: another chain was anchored there; remove it only if you know whose it is", auditchain.AnchorService, newAccount)
	case present:
		m.newAnchorSame = true
		r.warn("the keychain already holds this head for %s (a previous run); it is kept", newAccount)
	}
	return nil
}

// unknownInside warns about files in a migrated directory that are not part of its row: they
// stay in the snapshot.
func (m *migration) unknownInside(dir string, known map[string]bool) {
	entries, err := os.ReadDir(filepath.Join(m.o.OldHome, dir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !known[e.Name()] && !strings.HasSuffix(e.Name(), ".tmp") {
			m.r.warn("%s/%s is not part of the inventory; it is left in place and not migrated", dir, e.Name())
		}
	}
}

// checkManifest checks the snapshot and the backup against the D3 manifest.
func (m *migration) checkManifest(ctx context.Context) error {
	o, r := m.o, m.r
	mf, sum, err := manifest.Load(o.Manifest)
	if err != nil {
		r.refuse("%v", err)
		return nil
	}
	mismatches, err := manifest.Check(ctx, mf, manifest.Options{OldHome: o.OldHome, OldDB: o.OldDB, AnchorAccount: o.OldAnchorAccount})
	if err != nil {
		return fmt.Errorf("check the manifest: %w", err)
	}
	r.Manifest = &ManifestReport{Path: o.Manifest, SHA256: sum, SnapshotMatches: len(mismatches) == 0, Mismatches: mismatches}
	if len(mismatches) > 0 {
		r.refuse("the snapshot does not match the D3 manifest %s (first: %s)", o.Manifest, mismatches[0])
	}
	return nil
}

// resolve follows symlinks where the path (or its parent) exists.
func resolve(p string) string {
	if res, err := filepath.EvalSymlinks(p); err == nil {
		return res
	}
	if res, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(res, filepath.Base(p))
	}
	return filepath.Clean(p)
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
