package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hollis-labs/cerberus/internal/fleetprobe"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var fleetCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Fleet: what runs where, and what it was built from",
	Long: `Fleet commands report what runs on a host and its build provenance.

Subcommands:
  probe        report this host's running services and installed binaries
  inventory    what runs on every host, how far behind main, and drift
  stage-probe  copy this build to hosts as their fleet probe (a write; --ack)
  audit        provenance findings across the fleet, marking what is new`,
}

var fleetProbeOpts struct {
	json        bool
	binDirs     []string
	systemUnits bool
	noHealth    bool
	noRegistry  bool
}

var fleetProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Report this host's running services and installed binaries with build provenance",
	Long: `Probe reports, for this host, every running systemd service and container and
the executables in the installed-binary directories, with:

  - the executable a service runs, its sha256, and whether it was replaced on
    disk after the process started (a restart is pending);
  - Go build info: module, VCS revision and time, and whether the tree was dirty;
  - for interpreted services, the script and the git state of its work tree;
  - for containers, the image reference, ID and digests;
  - the Cerberus resource the service belongs to, and its declared health URL's
    answer, when this host has a Cerberus registry.

It is read-only and never reads process environments or config values.
Installed-binary directories default to ~/.local/bin, or the colon-separated
list in CERBERUS_FLEET_BIN_DIRS. The JSON schema is documented in docs/fleet.md.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opts := fleetprobe.Options{
			BinDirs:     fleetBinDirs(cmd),
			SystemUnits: fleetProbeOpts.systemUnits,
			Health:      !fleetProbeOpts.noHealth,
		}
		if cache, err := os.UserCacheDir(); err == nil {
			opts.HashCachePath = filepath.Join(cache, "cerberus", "fleet-probe-hashes.json")
		}
		var warnings []string
		if !fleetProbeOpts.noRegistry {
			refs, warn := fleetResourceRefs()
			opts.Resources = refs
			if warn != "" {
				warnings = append(warnings, warn)
			}
		}
		rep, err := fleetprobe.Probe(cmd.Context(), opts)
		if err != nil {
			return err
		}
		rep.Warnings = append(warnings, rep.Warnings...)
		if fleetProbeOpts.json {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		}
		printFleetProbe(cmd.OutOrStdout(), rep)
		return nil
	},
}

func init() {
	fleetProbeCmd.Flags().BoolVar(&fleetProbeOpts.json, "json", false, "print the report as JSON ("+fleetprobe.Schema+")")
	fleetProbeCmd.Flags().StringSliceVar(&fleetProbeOpts.binDirs, "bin-dir", nil, "installed-binary directory to report (repeatable; default ~/.local/bin or $CERBERUS_FLEET_BIN_DIRS)")
	fleetProbeCmd.Flags().BoolVar(&fleetProbeOpts.systemUnits, "system-units", true, "include system services whose executable is not part of the OS")
	fleetProbeCmd.Flags().BoolVar(&fleetProbeOpts.noHealth, "no-health", false, "skip GETs against declared health URLs")
	fleetProbeCmd.Flags().BoolVar(&fleetProbeOpts.noRegistry, "no-registry", false, "do not attribute services to Cerberus resources")
	fleetCmd.AddCommand(fleetProbeCmd)
	rootCmd.AddCommand(fleetCmd)
}

func fleetBinDirs(cmd *cobra.Command) []string {
	if cmd.Flags().Changed("bin-dir") {
		return fleetProbeOpts.binDirs
	}
	if env := os.Getenv("CERBERUS_FLEET_BIN_DIRS"); env != "" {
		var dirs []string
		for _, d := range strings.Split(env, ":") {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".local", "bin")}
}

// fleetResourceRefs reads this host's Cerberus registry. A host without one
// (a fleet worker) is normal and yields no refs and no warning.
func fleetResourceRefs() ([]fleetprobe.ResourceRef, string) {
	if _, err := os.Stat(cfgPath); err != nil {
		return nil, ""
	}
	v2, err := registry.ResolveConfig(cfgPath)
	if err != nil {
		return nil, "cerberus registry: " + err.Error()
	}
	host, _ := os.Hostname()
	refs := make([]fleetprobe.ResourceRef, 0, len(v2.Resources))
	for _, r := range v2.Resources {
		if r.Host != "" && r.Host != host && r.Host != "local" {
			continue
		}
		ref := fleetprobe.ResourceRef{ID: r.ID, Project: r.Project}
		if s, ok := r.Config["service_name"].(string); ok && s != "" {
			ref.Units = append(ref.Units, s)
		}
		if c, ok := r.Config["container"].(string); ok && c != "" {
			ref.Containers = append(ref.Containers, c)
		}
		if hc, ok := r.Config["health_check"].(map[string]any); ok {
			if u, ok := hc["url"].(string); ok {
				ref.HealthURL = u
			}
		}
		if ref.HealthURL == "" {
			if u, ok := r.Config["url"].(string); ok && strings.HasPrefix(u, "http") {
				ref.HealthURL = u
			}
		}
		refs = append(refs, ref)
	}
	return refs, ""
}

func printFleetProbe(w io.Writer, r *fleetprobe.Report) {
	fmt.Fprintf(w, "host %s · %d services · %d binaries · %s\n\n", r.Host, len(r.Services), len(r.Binaries), r.Duration)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tKIND\tRESOURCE\tBUILD\tFLAGS\tUP\tHEALTH")
	for _, s := range r.Services {
		build, flags := "-", []string{}
		switch {
		case s.Image != nil:
			build = s.Image.Ref
			if s.Image.Health != "" && s.Image.Health != "healthy" {
				flags = append(flags, s.Image.Health)
			}
		case s.Artifact != nil:
			build, flags = artifactSummary(*s.Artifact)
		}
		for _, c := range s.Children {
			if c.ReplacedOnDisk {
				flags = append(flags, "child-replaced:"+filepath.Base(c.Path))
			}
			if c.Go != nil && c.Go.Modified {
				flags = append(flags, "child-dirty:"+filepath.Base(c.Path))
			}
		}
		health := "-"
		if s.Health != nil {
			if s.Health.Error != "" {
				health = "error"
			} else {
				health = fmt.Sprint(s.Health.Status)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Kind, valueOrDash(s.ResourceID), build,
			valueOrDash(strings.Join(flags, ",")), since(s.StartedAt), health)
	}
	tw.Flush()
	if len(r.Binaries) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "BINARY\tKIND\tBUILD\tFLAGS")
		for _, b := range r.Binaries {
			build, flags := artifactSummary(b)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", filepath.Base(b.Path), b.Kind, build, valueOrDash(strings.Join(flags, ",")))
		}
		tw.Flush()
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
}

// artifactSummary renders an artifact's identity in one cell plus flags.
func artifactSummary(a fleetprobe.Artifact) (string, []string) {
	var flags []string
	if a.ReplacedOnDisk {
		flags = append(flags, "replaced-on-disk")
	}
	if a.Error != "" {
		flags = append(flags, "unreadable")
	}
	switch {
	case a.Go != nil:
		if a.Go.Modified {
			flags = append(flags, "dirty")
		}
		if a.Go.Unstamped() {
			flags = append(flags, "unstamped")
		}
		build := shortModule(a.Go.Module)
		if a.Go.Revision != "" {
			build += "@" + short(a.Go.Revision)
		} else if a.Go.ModuleVersion != "" && a.Go.ModuleVersion != "(devel)" {
			build += "@" + a.Go.ModuleVersion
		}
		if a.Release != nil {
			build += " (" + a.Release.Version + ")"
		}
		return build, flags
	case a.Script != nil:
		build := a.Script.Interpreter + " " + filepath.Base(a.Script.Path)
		if g := a.Script.Git; g != nil {
			build += "@" + short(g.Revision)
			if g.Dirty {
				flags = append(flags, "dirty")
			}
		}
		return build, flags
	case a.SHA256 != "":
		return a.Kind + " sha256:" + a.SHA256[:12], flags
	}
	return "-", flags
}

func shortModule(m string) string {
	parts := strings.Split(m, "/")
	if len(parts) > 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return m
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func since(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
