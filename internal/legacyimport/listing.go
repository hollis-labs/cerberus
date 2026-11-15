package legacyimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// topLevel says what the import does with each entry at the top of the old tree. An entry it
// does not know is listed and warned about, never dropped silently: the old tree stays where it
// is (archived at F), so nothing is lost, but the operator sees it.
func topLevel(name string) (row, kind string) {
	switch {
	case name == "audit":
		return "R4", "migrated"
	case name == "approvals":
		return "R6", "migrated"
	case name == "state":
		return "R8", "migrated"
	case name == "runtime":
		return "R9", "migrated"
	case name == "paused" || strings.HasPrefix(name, "paused."):
		return "R10", "migrated"
	case name == "credential-issuance":
		return "R11", "migrated"
	case name == "policy":
		return "R12", "migrated"
	case name == "config.yaml" || name == "events.yaml" || name == "connector-config.yaml":
		return "R13", "migrated"
	case name == "connector-secrets.yaml":
		return "R16", "migrated"
	case name == "brakes" || name == "events":
		return "R21", "migrated"
	case name == "oauth" || name == "alerts":
		return "R21", "migrated"
	case name == "registry.yaml":
		return "R14", "in_place"
	case name == "hosts.yaml":
		return "R15", "in_place"
	case name == hosts.HistoryFileName || strings.HasPrefix(name, hosts.HistoryFileName+"."):
		return "R8", "in_place"
	case name == "apps" || strings.HasPrefix(name, "plugins") || strings.HasPrefix(name, "plugin-") ||
		name == "outpost-tokens" || name == "events-tokens" || name == "secrets" || name == "transfers":
		if strings.HasSuffix(name, ".lock") {
			return "R25", "runtime"
		}
		return "R18", "in_place"
	case name == "pids":
		return "R19", "not_migrated"
	case name == "standby.d":
		return "R20", "refused"
	case name == "web":
		return "R22", "not_migrated"
	case name == "cerberus.sock" || name == "cerberus.pid" || name == "daemon.lock" || name == "locks" || strings.HasSuffix(name, ".lock"):
		return "R25", "runtime"
	case name == "cerberus.log" || name == "logs" || name == "backups" || name == "cerberus.db" || strings.Contains(name, ".bak"):
		return "R23", "archive"
	}
	return "", "unknown"
}

// listTree lists the snapshot's top level by treatment, the in-place and not-migrated rows with
// names and sizes only (passkeys, login keys and pid files are never opened).
func listTree(oldHome string, r *Report) {
	entries, err := os.ReadDir(oldHome)
	if err != nil {
		r.refuse("read %s: %v", oldHome, err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		fi, _ := os.Lstat(filepath.Join(oldHome, name))
		var size int64
		if fi != nil && !e.IsDir() {
			size = fi.Size()
		}
		row, kind := topLevel(name)
		entry := Entry{Path: name, Size: size, Dir: e.IsDir(), Row: row}
		switch kind {
		case "in_place":
			if row == "R8" || row == "R15" || name == "plugin-connectors.json" {
				entry.Note = "in place in the adopted root, which the new build reads; excluded from the D3 manifest"
			}
			r.Listed.InPlace = append(r.Listed.InPlace, entry)
		case "not_migrated":
			r.Listed.NotMigrated = append(r.Listed.NotMigrated, listChildren(oldHome, name, row)...)
		case "runtime":
			r.Listed.Runtime = append(r.Listed.Runtime, entry)
		case "archive":
			r.Listed.Archive = append(r.Listed.Archive, entry)
		case "unknown":
			entry.Note = "not part of the inventory: left in place, not migrated"
			r.Listed.NotMigrated = append(r.Listed.NotMigrated, entry)
			r.warn("%s is not in the old tree's inventory; it is left in place and not migrated", name)
		}
	}
	// R7: the passkeys, in place and unused (decision 0.1), names and sizes only.
	r.Listed.InPlace = append(r.Listed.InPlace, listChildren(oldHome, filepath.Join("approvals", "passkeys"), "R7")...)
	// R18: the issued credential values stay in the adopted root.
	if exists(filepath.Join(oldHome, "credential-issuance", "secrets")) {
		r.Listed.InPlace = append(r.Listed.InPlace, Entry{Path: "credential-issuance/secrets", Dir: true, Row: "R18", Note: "issued credential values, in place"})
	}
	// R12: policy backups stay with the old tree.
	backups, _ := filepath.Glob(filepath.Join(oldHome, "policy", "posture.yaml.bak-*"))
	for _, b := range backups {
		r.Listed.Archive = append(r.Listed.Archive, Entry{Path: filepath.Join("policy", filepath.Base(b)), Row: "R12", Note: "not copied"})
	}
	r.Listed.InPlace = append(r.Listed.InPlace, Entry{Path: "keyring service cerberus", Row: "R17", Note: "connector and plugin credentials: same service and account names, never read or written"})
	r.Listed.InPlace = append(r.Listed.InPlace, Entry{Path: "<XDG_STATE_HOME>/cerberus", Row: "R24", Note: "outside the snapshot: not read"})
	r.Listed.Archive = append(r.Listed.Archive, Entry{Path: "<XDG_DATA_HOME>/cerberus", Row: "R23", Note: "outside the snapshot: not read; main.db arrives as --old-db"})
	sortEntries(r.Listed.InPlace)
	sortEntries(r.Listed.NotMigrated)
	sortEntries(r.Listed.Archive)
	sortEntries(r.Listed.Runtime)
}

// listChildren lists dir's entries (names and sizes), or the directory alone when it is empty
// or absent.
func listChildren(oldHome, dir, row string) []Entry {
	entries, err := os.ReadDir(filepath.Join(oldHome, dir))
	if err != nil {
		return nil
	}
	var out []Entry
	for _, e := range entries {
		fi, _ := os.Lstat(filepath.Join(oldHome, dir, e.Name()))
		var size int64
		if fi != nil && !e.IsDir() {
			size = fi.Size()
		}
		out = append(out, Entry{Path: filepath.Join(dir, e.Name()), Size: size, Dir: e.IsDir(), Row: row})
	}
	if len(out) == 0 {
		out = append(out, Entry{Path: dir, Dir: true, Row: row, Note: "empty"})
	}
	return out
}

func sortEntries(e []Entry) { sort.Slice(e, func(i, j int) bool { return e[i].Path < e[j].Path }) }

// inspectRegistry is R14: registry.yaml and the descriptors it lists stay in place; the counts
// are what observe mode lists.
func inspectRegistry(oldHome string, r *Report) (*RegistryReport, *registry.ResolvedConfig) {
	rep := &RegistryReport{}
	idx, err := registry.LoadIndex(filepath.Join(oldHome, registry.DefaultIndexFilename))
	if err != nil {
		r.warn("registry.yaml: %v", err)
		return rep, nil
	}
	rep.Entries = len(idx.Entries)
	resolved, err := registry.ForRoot(oldHome).Resolve()
	if err != nil {
		r.warn("resolve the registry: %v", err)
		return rep, nil
	}
	rep.Resources = len(resolved.Config.Resources)
	rep.Warnings = resolved.Warnings
	return rep, resolved
}

// inspectHosts is R15: hosts.yaml in place, its digest and host count.
func inspectHosts(oldHome string, r *Report) *HostsReport {
	p := filepath.Join(oldHome, "hosts.yaml")
	data, err := os.ReadFile(p) //nolint:gosec // inside the snapshot
	if os.IsNotExist(err) {
		r.Listed.Absent = append(r.Listed.Absent, "hosts.yaml")
		return nil
	}
	if err != nil {
		r.warn("hosts.yaml: %v", err)
		return nil
	}
	rep := &HostsReport{}
	rep.SHA256, _ = fileSHA256(p)
	f, err := hosts.Parse(data)
	if err != nil {
		r.warn("hosts.yaml does not parse (it stays in place as it is): %v", err)
		return rep
	}
	rep.Hosts = len(f.Hosts)
	return rep
}

// pluginDigests is R18: the bundle digests plugin-connectors.json records (it stays in place).
func pluginDigests(oldHome string) map[string]string {
	data, err := os.ReadFile(filepath.Join(oldHome, "plugin-connectors.json")) //nolint:gosec // inside the snapshot
	if err != nil {
		return nil
	}
	var f struct {
		Entries []struct {
			ID           string `json:"id"`
			BundleDigest string `json:"bundle_digest"`
		} `json:"entries"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range f.Entries {
		out[e.ID] = e.BundleDigest
	}
	return out
}
