// Package fleetaudit turns a fleet inventory into findings: production builds
// from dirty trees, unstamped builds, processes running a replaced binary,
// builds far behind their default branch, drift across hosts, builds from a
// different module than their resource's source, descriptor copies the registry
// does not load, and resources expected to run that do not.
//
// It is pure: the caller supplies the inventory, the declared resources and
// the descriptor files it found; the package decides nothing about delivery.
package fleetaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/fleetprobe"
)

// Schema identifies the audit report format.
const Schema = "cerberus.fleet.audit.v1"

// Severities.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Finding kinds.
const (
	KindDirtyBuild      = "dirty_build"
	KindUnstamped       = "unstamped"
	KindReplacedOnDisk  = "replaced_on_disk"
	KindBehind          = "behind"
	KindDrift           = "drift"
	KindModuleMismatch  = "module_mismatch"
	KindStaleDescriptor = "stale_descriptor"
	KindNotRunning      = "not_running"
	KindHostUnreachable = "host_unreachable"
	KindNoProbe         = "no_probe"
)

// Finding is one problem. ID is stable across runs while the problem is the
// same one (same host, subject and build), so a caller can tell new from known.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Host     string `json:"host,omitempty"`
	Subject  string `json:"subject"`
	Detail   string `json:"detail" cerb:"untrusted"`
	New      bool   `json:"new"`
}

// Report is one audit run.
type Report struct {
	Schema      string    `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`
	Hosts       int       `json:"hosts"`
	Findings    []Finding `json:"findings"`
	// Resolved lists IDs that were findings last run and are gone now.
	Resolved []string `json:"resolved,omitempty"`
}

// Options tune an audit.
type Options struct {
	// BehindThreshold is the commit count above which a running service is a
	// finding (default 20). Installed binaries use twice the threshold.
	BehindThreshold int
	// Resources are the local host's declared resources.
	Resources []config.ResourceDef
	// ResourceModules maps a resource ID to the Go module its source dir
	// declares (from go.mod in config.dir), for the module-mismatch check.
	ResourceModules map[string]string
	// Descriptors are *.cerberus.yaml files found on disk; Registered are the
	// paths the registry index loads.
	Descriptors []string
	Registered  []string
}

// Run audits an inventory.
func Run(inv *cerbapi.FleetInventory, o Options) []Finding {
	if o.BehindThreshold <= 0 {
		o.BehindThreshold = 20
	}
	var out []Finding
	add := func(sev, kind, host, subject, key, detail string) {
		out = append(out, Finding{ID: fingerprint(kind, host, subject, key), Severity: sev, Kind: kind, Host: host, Subject: subject, Detail: detail})
	}
	var local *fleetprobe.Report
	for _, h := range inv.Hosts {
		switch h.Status {
		case cerbapi.FleetHostUnreachable, cerbapi.FleetHostError:
			add(Medium, KindHostUnreachable, h.Name, h.Name, h.Status, h.Reason)
		case cerbapi.FleetHostNoProbe:
			add(Low, KindNoProbe, h.Name, h.Name, "", h.Reason)
		}
		r := h.Report
		if r == nil {
			continue
		}
		if r.Host != "" && isLocalReport(h) {
			local = r
		}
		for _, s := range r.Services {
			if s.Artifact == nil {
				continue
			}
			a := s.Artifact
			subject := serviceSubject(s)
			if a.ReplacedOnDisk {
				add(Medium, KindReplacedOnDisk, h.Name, subject, a.SHA256, "running a binary that was replaced on disk after it started; restart to run the installed build ("+a.Path+")")
			}
			for _, c := range s.Children {
				if c.ReplacedOnDisk {
					add(Low, KindReplacedOnDisk, h.Name, subject+" > "+filepath.Base(c.Path), c.SHA256, "child process runs a binary replaced on disk ("+c.Path+")")
				}
			}
			if a.Go != nil {
				if a.Go.Modified {
					add(High, KindDirtyBuild, h.Name, subject, a.Go.Revision, "running a build from a tree with uncommitted changes: "+buildOf(a))
				}
				if a.Go.Unstamped() {
					add(Medium, KindUnstamped, h.Name, subject, a.SHA256, "build carries no VCS revision (built with -buildvcs=false or outside the repository): "+a.Path)
				}
				behind(add, h.Name, subject, a, o.BehindThreshold)
			}
			if a.Script != nil && a.Script.Git != nil && a.Script.Git.Dirty {
				add(Medium, KindDirtyBuild, h.Name, subject, a.Script.Git.Revision, "script runs from a work tree with uncommitted changes: "+a.Script.Git.Root)
			}
			if want := o.ResourceModules[s.ResourceID]; want != "" && a.Go != nil && a.Go.Module != "" && a.Go.Module != want {
				add(Medium, KindModuleMismatch, h.Name, subject, a.Go.Module, fmt.Sprintf("runs a build of %s, but the resource's source dir is module %s", a.Go.Module, want))
			}
		}
		for i := range r.Binaries {
			b := &r.Binaries[i]
			if b.Go == nil {
				continue
			}
			subject := "binary " + filepath.Base(b.Path)
			if b.Go.Modified {
				add(Low, KindDirtyBuild, h.Name, subject, b.Go.Revision, "installed build from a tree with uncommitted changes: "+buildOf(b))
			}
			behind(add, h.Name, subject, b, 2*o.BehindThreshold)
		}
	}
	for _, d := range inv.Drift {
		builds := make([]string, 0, len(d.Builds))
		for b, hs := range d.Builds {
			builds = append(builds, b+" on "+strings.Join(hs, ","))
		}
		sort.Strings(builds)
		add(Low, KindDrift, "", d.Name, strings.Join(builds, ";"), "different builds across hosts: "+strings.Join(builds, "; "))
	}
	registered := map[string]bool{}
	for _, p := range o.Registered {
		registered[filepath.Clean(p)] = true
	}
	for _, p := range o.Descriptors {
		if !registered[filepath.Clean(p)] {
			add(Medium, KindStaleDescriptor, "", p, "", "descriptor is not in the registry index, so Cerberus does not load it; edits to it change nothing (delete it, or register it with `cerberus register`)")
		}
	}
	if local != nil {
		running := map[string]bool{}
		for _, s := range local.Services {
			if s.ResourceID != "" {
				running[s.ResourceID] = true
			}
		}
		for _, r := range o.Resources {
			if r.Host != "" && r.Host != "local" && r.Host != local.Host {
				continue
			}
			if auto, _ := r.Config["auto_start"].(bool); !auto {
				continue
			}
			_, hasUnit := r.Config["service_name"].(string)
			_, hasContainer := r.Config["container"].(string)
			if !hasUnit && !hasContainer && r.Connector != "docker" {
				continue // not something the probe can see running
			}
			if !running[r.ID] {
				add(Medium, KindNotRunning, local.Host, "resource "+r.ID, "", "declared auto_start but no running unit or container belongs to it")
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i].Severity) != rank(out[j].Severity) {
			return rank(out[i].Severity) < rank(out[j].Severity)
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Subject < out[j].Subject
	})
	return dedupe(out)
}

func behind(add func(sev, kind, host, subject, key, detail string), host, subject string, a *fleetprobe.Artifact, threshold int) {
	u := a.Upstream
	if u == nil || u.Behind == nil || *u.Behind <= threshold {
		return
	}
	detail := fmt.Sprintf("%d commits behind %s", *u.Behind, u.Branch)
	if n := len(u.Merged); n > 0 {
		var prs []string
		for i, pr := range u.Merged {
			if i == 5 {
				prs = append(prs, fmt.Sprintf("and %d more", n-5))
				break
			}
			prs = append(prs, fmt.Sprintf("#%d %s", pr.Number, pr.Title))
		}
		detail += "; missing merged PRs: " + strings.Join(prs, "; ")
	}
	sev := Low
	if *u.Behind > 5*threshold {
		sev = Medium
	}
	add(sev, KindBehind, host, subject, a.Go.Revision, detail)
}

func serviceSubject(s fleetprobe.Service) string {
	if s.ResourceID != "" {
		return "resource " + s.ResourceID
	}
	return s.Kind + " " + s.Name
}

func buildOf(a *fleetprobe.Artifact) string {
	if a.Go == nil {
		return a.Path
	}
	rev := a.Go.Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return a.Go.Module + "@" + rev + " (" + a.Path + ")"
}

func isLocalReport(h cerbapi.FleetHost) bool {
	// The local host is probed in process with attribution; remote reports
	// carry no resource IDs.
	for _, s := range h.Report.Services {
		if s.ResourceID != "" {
			return true
		}
	}
	return false
}

func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:6])
}

func rank(sev string) int {
	switch sev {
	case High:
		return 0
	case Medium:
		return 1
	}
	return 2
}

func dedupe(in []Finding) []Finding {
	seen := map[string]bool{}
	out := in[:0]
	for _, f := range in {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	return out
}

// MarkNew sets New on findings whose ID is not in previous, and returns the
// IDs in previous that are no longer found.
func MarkNew(findings []Finding, previous []string) (resolved []string) {
	prev := map[string]bool{}
	for _, id := range previous {
		prev[id] = true
	}
	now := map[string]bool{}
	for i := range findings {
		now[findings[i].ID] = true
		findings[i].New = !prev[findings[i].ID]
	}
	for _, id := range previous {
		if !now[id] {
			resolved = append(resolved, id)
		}
	}
	sort.Strings(resolved)
	return resolved
}
