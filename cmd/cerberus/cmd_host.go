package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/spf13/cobra"
)

var hostCmd = &cobra.Command{
	Use:   "host",
	Short: "Hosts: the machines Cerberus keeps a record of",
	Long: `Hosts are named machines declared in ~/.cerberus/hosts.yaml, with facts
Cerberus measured over SSH or a shared Outpost. See docs/hosts.md.

Cerberus reports facts. It does not rank hosts, reserve them or choose where
work goes; whatever consumes these facts decides that.

Subcommands:
  list      declared hosts with state and probed facts
  get       one host
  probe     measure a host through its selected transport
  plan      preview an Outpost unit operation
  act       execute an Outpost unit operation through policy
  capacity  hosts that satisfy constraints, from recorded facts`,
}

var (
	hostJSONFlag   bool
	hostOutput     string
	hostFilterOpts struct {
		role, provider, state string
		labels                []string
	}
	hostCapacityOpts struct {
		minMemory, minDisk, role, provider string
		minCPU                             int
		labels, tools                      []string
		maxAge                             time.Duration
		includeOffline, explain            bool
	}
)

// hostWantsJSON is true for --json and for -o json.
func hostWantsJSON() bool { return hostJSONFlag || hostOutput == outputFormatJSON }

// newHostClient picks the daemon when one is up and the in-process service
// otherwise. The choice is made before anything is sent: a host call is a
// read, but an error after delivery is still returned, never retried, which
// keeps a probe from running twice.
func newHostClient(cmd *cobra.Command) (cerbapi.HostClient, error) {
	if flag := cmd.Flag("config"); flag == nil || !flag.Changed {
		client, err := commandSocket(cmd.Context())
		if err != nil {
			return nil, err
		}
		if client != nil {
			return client, nil
		}
	}
	return localHostClient{svc: app.NewLocalHostService(cfgPath)}, nil
}

// localHostClient is the in-process lane: the operator's own shell.
type localHostClient struct{ svc *cerbapi.HostService }

func (l localHostClient) ListHosts(ctx context.Context, f cerbapi.HostFilter) ([]cerbapi.HostView, error) {
	return l.svc.List(inProcessContext(ctx), f)
}
func (l localHostClient) GetHost(ctx context.Context, name string) (cerbapi.HostView, error) {
	return l.svc.Get(inProcessContext(ctx), name)
}
func (l localHostClient) ProbeHost(ctx context.Context, name string) (cerbapi.HostView, error) {
	return l.svc.Probe(inProcessContext(ctx), name)
}
func (l localHostClient) HostCapacity(ctx context.Context, q cerbapi.CapacityQuery) (cerbapi.CapacityResult, error) {
	return l.svc.Capacity(inProcessContext(ctx), q)
}

// parseLabels reads repeated key=value flags.
func parseLabels(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("label %q must be key=value", p)
		}
		out[k] = v
	}
	return out, nil
}

var hostListCmd = &cobra.Command{
	Use:   "list",
	Short: "List declared hosts with state and probed facts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		labels, err := parseLabels(hostFilterOpts.labels)
		if err != nil {
			return err
		}
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		return runHostList(cmd.Context(), client, cerbapi.HostFilter{Role: hostFilterOpts.role,
			Provider: hostFilterOpts.provider, State: hostFilterOpts.state, Labels: labels}, hostWantsJSON(), cmd.OutOrStdout())
	},
}

var hostGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show one host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		v, err := client.GetHost(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return printHost(cmd.OutOrStdout(), v, hostWantsJSON(), time.Now())
	},
}

var hostProbeCmd = &cobra.Command{
	Use:   "probe <name>",
	Short: "Measure a host over ssh and record the facts",
	Long: `Runs one fixed, read-only script on the host over its declared ssh reach and
records what it reports. A host that does not answer is state offline, not an
error. Nothing on the host changes.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		v, err := client.ProbeHost(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return printHost(cmd.OutOrStdout(), v, hostWantsJSON(), time.Now())
	},
}

var hostCapacityCmd = &cobra.Command{
	Use:   "capacity",
	Short: "List the hosts that satisfy constraints, from recorded facts",
	Long: `Lists the hosts whose recorded facts satisfy every constraint given, in name
order. This is a filter, not a placement: Cerberus does not rank, reserve or
choose a host, and a consumer decides where work goes.

It reads recorded facts and does not probe. A host with no probe data, or data
older than --max-age, is listed as unknown with the reason; it is never
guessed to fit. Run 'cerberus host probe <name>' for fresh facts.
--explain also lists the excluded hosts and why.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		o := hostCapacityOpts
		q := cerbapi.CapacityQuery{MinCPU: o.minCPU, Role: o.role, Provider: o.provider, RequireTools: o.tools,
			MaxStaleAge: o.maxAge, IncludeOffline: o.includeOffline, Explain: o.explain}
		var err error
		if o.minMemory != "" {
			if q.MinMemoryAvailable, err = hosts.ParseSize(o.minMemory); err != nil {
				return fmt.Errorf("--min-memory: %w", err)
			}
		}
		if o.minDisk != "" {
			if q.MinDiskFree, err = hosts.ParseSize(o.minDisk); err != nil {
				return fmt.Errorf("--min-disk: %w", err)
			}
		}
		if q.Labels, err = parseLabels(o.labels); err != nil {
			return err
		}
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		res, err := client.HostCapacity(cmd.Context(), q)
		if err != nil {
			return err
		}
		return printCapacity(cmd.OutOrStdout(), res, hostWantsJSON(), time.Now())
	},
}

func init() {
	hostCmd.PersistentFlags().BoolVar(&hostJSONFlag, "json", false, "print JSON (same as -o json)")
	hostCmd.PersistentFlags().StringVarP(&hostOutput, "output", "o", outputFormatText, "output format: text|json")

	f := hostListCmd.Flags()
	f.StringVar(&hostFilterOpts.role, "role", "", "only hosts with this role")
	f.StringVar(&hostFilterOpts.provider, "provider", "", "only hosts with this provider")
	f.StringVar(&hostFilterOpts.state, "state", "", "only hosts in this state: online|offline|unknown")
	f.StringArrayVar(&hostFilterOpts.labels, "label", nil, "only hosts with this label, key=value (repeatable)")

	c := hostCapacityCmd.Flags()
	c.StringVar(&hostCapacityOpts.minMemory, "min-memory", "", "minimum available memory, such as 4GiB")
	c.StringVar(&hostCapacityOpts.minDisk, "min-disk", "", "minimum free disk on the data mount, such as 50GB")
	c.IntVar(&hostCapacityOpts.minCPU, "min-cpu", 0, "minimum probed logical cores")
	c.StringVar(&hostCapacityOpts.role, "role", "", "declared role")
	c.StringVar(&hostCapacityOpts.provider, "provider", "", "provider")
	c.StringArrayVar(&hostCapacityOpts.labels, "label", nil, "label that must match, key=value (repeatable)")
	c.StringArrayVar(&hostCapacityOpts.tools, "tool", nil, "probed tool that must be installed: docker|tmux|tailscale|caddy (repeatable)")
	c.DurationVar(&hostCapacityOpts.maxAge, "max-age", 0, "oldest probe to judge on (default 5m)")
	c.BoolVar(&hostCapacityOpts.includeOffline, "include-offline", false, "judge offline hosts on their last measurement")
	c.BoolVar(&hostCapacityOpts.explain, "explain", false, "also list excluded hosts with the reason for each")

	hostCmd.AddCommand(hostListCmd, hostGetCmd, hostProbeCmd, hostCapacityCmd)
	rootCmd.AddCommand(hostCmd)
}

func runHostList(ctx context.Context, client cerbapi.HostClient, f cerbapi.HostFilter, asJSON bool, out io.Writer) error {
	views, err := client.ListHosts(ctx, f)
	if err != nil {
		return err
	}
	if views == nil {
		views = []cerbapi.HostView{}
	}
	if asJSON {
		return writeJSON(out, views)
	}
	if len(views) == 0 {
		_, err := fmt.Fprintln(out, "No hosts match. Hosts are declared in ~/.cerberus/hosts.yaml (see docs/hosts.md).")
		return err
	}
	return printHostTable(out, views, time.Now())
}

func printHostTable(out io.Writer, views []cerbapi.HostView, now time.Time) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tROLE\tCPU\tMEM AVAIL\tDISK FREE\tPROBED")
	for _, v := range views {
		cpu, mem, disk := "-", "-", "-"
		if p := v.Probed; p != nil {
			cpu, mem, disk = hostCell(p.CPU > 0, fmt.Sprint(p.CPU)), hostCell(p.MemoryAvailable > 0, humanBytes(p.MemoryAvailable)), hostCell(p.DiskTotal > 0, humanBytes(p.DiskFree))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Host.Name, v.State, hostCell(v.Host.Role != "", v.Host.Role), cpu, mem, disk, probedAge(v, now))
	}
	return w.Flush()
}

func hostCell(ok bool, s string) string {
	if !ok {
		return "-"
	}
	return s
}

// probedAge says when the facts were measured; a host never measured says so,
// and stale facts say so beside their age.
func probedAge(v cerbapi.HostView, now time.Time) string {
	if v.ProbedAt == nil {
		return "never"
	}
	age := now.Sub(*v.ProbedAt).Round(time.Second)
	if v.Stale {
		return age.String() + " ago (stale)"
	}
	return age.String() + " ago"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func printHost(out io.Writer, v cerbapi.HostView, asJSON bool, now time.Time) error {
	if asJSON {
		return writeJSON(out, v)
	}
	h := v.Host
	fmt.Fprintf(out, "%s  [%s]\n", h.Name, v.State)
	row := func(k, val string) {
		if val != "" {
			fmt.Fprintf(out, "  %-14s %s\n", k+":", val)
		}
	}
	row("role", h.Role)
	row("provider", h.Provider)
	row("hostname", h.Hostname)
	row("tailnet", strings.TrimSpace(h.TailnetName+" "+h.TailnetIP))
	row("lan ip", h.LanIP)
	row("ssh reach", h.Reach.SSH)
	if len(h.Labels) > 0 {
		var kv []string
		for k, val := range h.Labels {
			kv = append(kv, k+"="+val)
		}
		sortStrings(kv)
		row("labels", strings.Join(kv, " "))
	}
	d := h.Declared
	row("declared", strings.TrimSpace(fmt.Sprintf("%s %s cpu=%d memory=%s disk=%s gpu=%s", d.OS, d.Arch, d.CPU, humanBytes(int64(d.Memory)), humanBytes(int64(d.Disk)), d.GPU)))
	if hw := h.Hardware; hw != nil {
		row("hardware", strings.Join(nonEmpty(hw.Vendor, hw.Model, hw.Version), " "))
		row("serial", strings.TrimSpace(hw.Serial+" "+tagged("asset", hw.AssetTag)))
		row("board", hw.Board)
		row("bios", hw.BIOS)
		row("cpu", strings.Join(nonEmpty(hw.CPUModel, tagged("socket", hw.CPUSocket)), " "))
		if m := hw.Memory; m != nil {
			mem := strings.Join(nonEmpty(sizeOrEmpty(int64(m.Total)), m.Type), " ")
			if m.Slots > 0 {
				mem += fmt.Sprintf(", %d of %d slots used", m.SlotsUsed, m.Slots)
			}
			if m.Max > 0 {
				mem += ", max " + humanBytes(int64(m.Max))
			}
			row("memory", mem)
		}
		var disks []string
		for _, d := range hw.Disks {
			disks = append(disks, strings.Join(nonEmpty(d.Name, sizeOrEmpty(int64(d.Size)), d.Type, d.Bus, d.Model, tagged("sn", d.Serial)), " "))
		}
		row("disks", strings.Join(disks, "; "))
		var nics []string
		for _, n := range hw.NICs {
			nics = append(nics, strings.TrimSpace(n.Name+" "+n.MAC))
		}
		row("nics", strings.Join(nics, "; "))
		row("notes", hw.Notes)
		if hw.RecordedAt != nil {
			row("recorded", hw.RecordedAt.UTC().Format(time.RFC3339))
		}
	}
	if p := v.Probed; p != nil {
		row("probed hw", strings.Join(nonEmpty(p.HWVendor, p.HWModel, p.HWVersion, tagged("bios", p.BIOS)), " "))
		var disks []string
		for _, d := range p.Disks {
			kind := ""
			if d.Rotational != nil {
				kind = map[bool]string{true: "rotational", false: "solid-state"}[*d.Rotational]
			}
			disks = append(disks, strings.Join(nonEmpty(d.Name, sizeOrEmpty(d.Size), kind, d.Transport, d.Model), " "))
		}
		row("probed disks", strings.Join(disks, "; "))
		var nics []string
		for _, n := range p.NICs {
			nics = append(nics, n.Name+" "+n.MAC)
		}
		row("probed nics", strings.Join(nics, "; "))
		row("probed", strings.TrimSpace(fmt.Sprintf("%s %s %s cpu=%d memory=%s/%s disk=%s/%s", p.OS, p.OSVersion, p.Arch, p.CPU,
			humanBytes(p.MemoryAvailable), humanBytes(p.MemoryTotal), humanBytes(p.DiskFree), humanBytes(p.DiskTotal))))
		if p.Load1 != nil {
			row("load", fmt.Sprintf("%.2f", *p.Load1))
		}
		var tools []string
		for _, t := range hosts.ProbedTools {
			if have, ok := p.Tools[t]; ok {
				tools = append(tools, fmt.Sprintf("%s=%t", t, have))
			}
		}
		row("tools", strings.Join(tools, " "))
	}
	row("measured", probedAge(v, now))
	row("reason", v.Reason)
	switch {
	case v.Probed == nil:
		fmt.Fprintf(out, "  No probe data: the host's facts are unknown. Run `cerberus host probe %s`.\n", h.Name)
	case v.Stale:
		fmt.Fprintf(out, "  These facts are stale. Run `cerberus host probe %s` for current ones.\n", h.Name)
	}
	return nil
}

func printCapacity(out io.Writer, res cerbapi.CapacityResult, asJSON bool, now time.Time) error {
	if asJSON {
		return writeJSON(out, res)
	}
	fmt.Fprintf(out, "%d matched, %d unknown, of %d declared hosts (probe age limit %s). Facts only: not ranked, nothing reserved.\n",
		len(res.Matched), len(res.Unknown), res.Total, res.MaxStaleAge)
	if len(res.Matched) > 0 {
		fmt.Fprintln(out)
		if err := printHostTable(out, res.Matched, now); err != nil {
			return err
		}
	}
	for _, u := range res.Unknown {
		fmt.Fprintf(out, "\nunknown  %s: %s\n         %s\n", u.Name, strings.Join(u.Reasons, "; "), u.NextStep)
	}
	for _, x := range res.Excluded {
		fmt.Fprintf(out, "\nexcluded %s: %s\n", x.Name, strings.Join(x.Reasons, "; "))
	}
	return nil
}

func sortStrings(s []string) { sort.Strings(s) }

// nonEmpty drops empty strings, for joining optional parts.
func nonEmpty(parts ...string) []string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tagged renders "key=value", or "" when value is empty.
func tagged(key, value string) string {
	if value == "" {
		return ""
	}
	return key + "=" + value
}

// sizeOrEmpty is humanBytes, or "" for an unknown (zero) size.
func sizeOrEmpty(n int64) string {
	if n <= 0 {
		return ""
	}
	return humanBytes(n)
}
