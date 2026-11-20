package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/fleetprobe"
	"github.com/spf13/cobra"
)

var fleetInvOpts struct {
	json     bool
	hosts    []string
	role     string
	service  string
	issues   bool
	binaries bool
	refresh  bool
}

var fleetInventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "What runs on every host and what it was built from",
	Long: `Inventory probes every host in hosts.yaml (or --host / --role) and reports each
service, container and installed binary with its build, flags (dirty,
unstamped, replaced-on-disk), how many commits it is behind its repository's
default branch with the merged pull requests it lacks, and executables that
differ across hosts.

The local host is probed in process. Other hosts are probed over their
reach.ssh with a fixed command that runs a probe staged by
'cerberus fleet stage-probe'. A host's fleet.bin_dirs label (colon-separated)
names the installed-binary directories to report there.

"Behind" is computed from local checkouts found under CERBERUS_FLEET_REPO_ROOTS
(default ~/dev) against their last fetch of origin's default branch.
Results are cached for a minute; --refresh probes again.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		q := cerbapi.FleetInventoryQuery{Hosts: fleetInvOpts.hosts, Role: fleetInvOpts.role, Refresh: fleetInvOpts.refresh}
		inv, err := fleetInventory(cmd, q)
		if err != nil {
			return err
		}
		if fleetInvOpts.json {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(inv)
		}
		printFleetInventory(cmd.OutOrStdout(), inv)
		return nil
	},
}

// fleetInventory reads through the daemon when it serves the inventory, and
// in process otherwise. It is a read, so falling back after a failed daemon
// call is safe.
func fleetInventory(cmd *cobra.Command, q cerbapi.FleetInventoryQuery) (*cerbapi.FleetInventory, error) {
	if flag := cmd.Flag("config"); flag == nil || !flag.Changed {
		if client, err := commandSocket(cmd.Context()); err == nil && client != nil {
			if inv, err := client.FleetInventory(cmd.Context(), q); err == nil {
				return inv, nil
			}
		}
	}
	return app.NewLocalHostService(cfgPath).FleetInventory(inProcessContext(cmd.Context()), q)
}

var fleetStageOpts struct {
	hosts []string
	role  string
	ack   bool
}

var fleetStageProbeCmd = &cobra.Command{
	Use:   "stage-probe",
	Short: "Copy this cerberus build to hosts as their fleet probe",
	Long: `Stage-probe copies this cerberus binary to each selected host over its
reach.ssh, as ~/.local/share/cerberus/fleet-probe/cerberus, so 'cerberus fleet
inventory' can probe it. Hosts of another OS or architecture and hosts already
at this build are skipped. It writes to the hosts, so it needs --ack.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !fleetStageOpts.ack {
			return fmt.Errorf("stage-probe writes a file on each selected host; pass --ack to proceed")
		}
		svc := app.NewLocalHostService(cfgPath)
		res, err := svc.StageFleetProbe(inProcessContext(cmd.Context()), cerbapi.FleetStageArgs{Hosts: fleetStageOpts.hosts, Role: fleetStageOpts.role, Acknowledged: true})
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tSTATUS\tREASON")
		for _, r := range res {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Status, valueOrDash(r.Reason))
		}
		return tw.Flush()
	},
}

func init() {
	f := fleetInventoryCmd.Flags()
	f.BoolVar(&fleetInvOpts.json, "json", false, "print the inventory as JSON ("+cerbapi.FleetInventorySchema+")")
	f.StringSliceVar(&fleetInvOpts.hosts, "host", nil, "only these hosts (repeatable or comma-separated)")
	f.StringVar(&fleetInvOpts.role, "role", "", "only hosts with this role")
	f.StringVar(&fleetInvOpts.service, "service", "", "only services and binaries whose name or resource contains this")
	f.BoolVar(&fleetInvOpts.issues, "issues", false, "only rows with a flag or commits behind")
	f.BoolVar(&fleetInvOpts.binaries, "binaries", false, "also list installed binaries")
	f.BoolVar(&fleetInvOpts.refresh, "refresh", false, "probe again instead of using the one-minute cache")
	g := fleetStageProbeCmd.Flags()
	g.StringSliceVar(&fleetStageOpts.hosts, "host", nil, "only these hosts (repeatable or comma-separated)")
	g.StringVar(&fleetStageOpts.role, "role", "", "only hosts with this role")
	g.BoolVar(&fleetStageOpts.ack, "ack", false, "acknowledge that this writes the probe to each host")
	fleetCmd.AddCommand(fleetInventoryCmd, fleetStageProbeCmd)
}

type invRow struct {
	host, name, kind, resource, build, flags, behind, up, health string
	interesting                                                  bool
}

func printFleetInventory(w io.Writer, inv *cerbapi.FleetInventory) {
	cached := ""
	if inv.Cached {
		cached = " (cached)"
	}
	fmt.Fprintf(w, "fleet inventory · %d hosts · %s%s\n\n", len(inv.Hosts), inv.Duration, cached)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tROLE\tSTATUS\tSERVICES\tBINARIES\tNOTE")
	for _, h := range inv.Hosts {
		ns, nb := "-", "-"
		if h.Report != nil {
			ns, nb = fmt.Sprint(len(h.Report.Services)), fmt.Sprint(len(h.Report.Binaries))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, valueOrDash(h.Role), h.Status, ns, nb, valueOrDash(h.Reason))
	}
	tw.Flush()

	var rows []invRow
	match := func(parts ...string) bool {
		if fleetInvOpts.service == "" {
			return true
		}
		for _, p := range parts {
			if strings.Contains(strings.ToLower(p), strings.ToLower(fleetInvOpts.service)) {
				return true
			}
		}
		return false
	}
	for _, h := range inv.Hosts {
		if h.Report == nil {
			continue
		}
		for _, s := range h.Report.Services {
			if !match(s.Name, s.ResourceID) {
				continue
			}
			row := invRow{host: h.Name, name: s.Name, kind: s.Kind, resource: valueOrDash(s.ResourceID), up: since(s.StartedAt), health: "-", behind: "-"}
			var flags []string
			switch {
			case s.Image != nil:
				row.build = s.Image.Ref
			case s.Artifact != nil:
				row.build, flags = artifactSummary(*s.Artifact)
				row.behind = behindCell(s.Artifact.Upstream)
			}
			for _, c := range s.Children {
				if c.ReplacedOnDisk {
					flags = append(flags, "child-replaced:"+filepath.Base(c.Path))
				}
				if c.Go != nil && c.Go.Modified {
					flags = append(flags, "child-dirty:"+filepath.Base(c.Path))
				}
			}
			if s.Health != nil {
				if s.Health.Error != "" {
					row.health = "error"
				} else {
					row.health = fmt.Sprint(s.Health.Status)
				}
			}
			row.flags = valueOrDash(strings.Join(flags, ","))
			row.interesting = len(flags) > 0 || (row.behind != "-" && row.behind != "0")
			rows = append(rows, row)
		}
		if fleetInvOpts.binaries {
			for _, b := range h.Report.Binaries {
				name := filepath.Base(b.Path)
				if !match(name) {
					continue
				}
				build, flags := artifactSummary(b)
				row := invRow{host: h.Name, name: name, kind: "binary", resource: "-", build: build, up: "-", health: "-",
					flags: valueOrDash(strings.Join(flags, ",")), behind: behindCell(b.Upstream)}
				row.interesting = len(flags) > 0 || (row.behind != "-" && row.behind != "0")
				rows = append(rows, row)
			}
		}
	}
	if fleetInvOpts.issues {
		kept := rows[:0]
		for _, r := range rows {
			if r.interesting {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if len(rows) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tSERVICE\tKIND\tRESOURCE\tBUILD\tFLAGS\tBEHIND\tUP\tHEALTH")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.host, r.name, r.kind, r.resource, r.build, r.flags, r.behind, r.up, r.health)
		}
		tw.Flush()
	}
	if len(inv.Drift) > 0 {
		fmt.Fprintln(w, "\nDRIFT (same executable, different builds across hosts)")
		for _, d := range inv.Drift {
			if !match(d.Name, d.Module) {
				continue
			}
			builds := make([]string, 0, len(d.Builds))
			for b := range d.Builds {
				builds = append(builds, b)
			}
			sort.Strings(builds)
			parts := make([]string, 0, len(builds))
			for _, b := range builds {
				parts = append(parts, b+" on "+strings.Join(d.Builds[b], ","))
			}
			fmt.Fprintf(w, "  %s (%s): %s\n", d.Name, shortModule(d.Module), strings.Join(parts, "; "))
		}
	}
}

// behindCell renders commits behind, with the first merged PRs it lacks.
func behindCell(u *fleetprobe.Upstream) string {
	if u == nil || u.Behind == nil {
		return "-"
	}
	cell := fmt.Sprint(*u.Behind)
	if n := len(u.Merged); n > 0 {
		var prs []string
		for i, pr := range u.Merged {
			if i == 3 {
				prs = append(prs, fmt.Sprintf("+%d", n-3))
				break
			}
			prs = append(prs, fmt.Sprintf("#%d", pr.Number))
		}
		cell += " (" + strings.Join(prs, " ") + ")"
	}
	return cell
}
