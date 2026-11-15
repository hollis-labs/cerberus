package main

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

// The host reads on the CLI: the old `host list|get|probe|status|samples|monitor|events`
// (cmd/cerberus cmd_host.go and cmd_host_monitor.go on main), with the old flags and output,
// over the hosts lane's GET /api/hosts… and POST /api/hosts/{name}/probe with the CLI's key.
// The host writes, inventory, history, Outpost unit actions and capacity have no route yet
// (pending, cli_pending.go).

// hostCommand is `host` against the server client returns.
func hostCommand(client func() (*apiClient, error)) *cobra.Command {
	var jsonFlag bool
	var output string
	wantsJSON := func() bool { return jsonFlag || output == outputJSON }
	group := &cobra.Command{
		Use:   "host",
		Short: "Hosts: the machines Cerberus keeps a record of",
		Long: `Hosts are named machines declared in hosts.yaml in the adopted root, with facts
Cerberus measured over SSH or a shared Outpost. See docs/hosts.md.

Cerberus reports facts. It does not rank hosts, reserve them or choose where
work goes; whatever consumes these facts decides that.

Subcommands:
  list      declared hosts with state and probed facts
  get       one host
  probe     measure a host through its selected transport
  status    a host's health, or every host's
  samples   a host's retained measurements
  monitor   what the host monitor is doing
  events    host status-change events`,
	}
	group.PersistentFlags().BoolVar(&jsonFlag, "json", false, "print JSON (same as -o json)")
	group.PersistentFlags().StringVarP(&output, "output", "o", outputText, "output format: text|json")

	var role, provider, state string
	var labels []string
	list := &cobra.Command{
		Use:   "list",
		Short: "List declared hosts with state and probed facts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for k, v := range map[string]string{"role": role, "provider": provider, "state": state} {
				if v != "" {
					q.Set(k, v)
				}
			}
			for _, l := range labels {
				if k, _, ok := strings.Cut(l, "="); !ok || k == "" {
					return fmt.Errorf("label %q must be key=value", l)
				}
				q.Add("label", l)
			}
			path := "/hosts"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var views []api.HostView
			if err := getWith(cmd, client, path, &views); err != nil {
				return err
			}
			if views == nil {
				views = []api.HostView{}
			}
			out := cmd.OutOrStdout()
			if wantsJSON() {
				return printJSON(out, views)
			}
			if len(views) == 0 {
				_, err := fmt.Fprintln(out, "No hosts match. Hosts are declared in ~/.cerberus/hosts.yaml (see docs/hosts.md).")
				return err
			}
			return printHostTable(out, views, time.Now())
		},
	}
	f := list.Flags()
	f.StringVar(&role, "role", "", "only hosts with this role")
	f.StringVar(&provider, "provider", "", "only hosts with this provider")
	f.StringVar(&state, "state", "", "only hosts in this state: online|offline|unknown")
	f.StringArrayVar(&labels, "label", nil, "only hosts with this label, key=value (repeatable)")

	get := &cobra.Command{
		Use:   "get <name>",
		Short: "Show one host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var v api.HostView
			if err := getWith(cmd, client, "/hosts/"+url.PathEscape(args[0]), &v); err != nil {
				return err
			}
			return printHost(cmd.OutOrStdout(), v, wantsJSON(), time.Now())
		},
	}

	probe := &cobra.Command{
		Use:   "probe <name>",
		Short: "Measure a host over ssh and record the facts",
		Long: `Runs one fixed, read-only script on the host over its declared ssh reach and
records what it reports. A host that does not answer is state offline, not an
error. Nothing on the host changes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var v api.HostView
			if err = cl.post(cmd.Context(), "/hosts/"+url.PathEscape(args[0])+"/probe", struct{}{}, &v); err != nil {
				return err
			}
			return printHost(cmd.OutOrStdout(), v, wantsJSON(), time.Now())
		},
	}

	status := &cobra.Command{
		Use:   "status [name]",
		Short: "Health of a host, or of every host: online, degraded, offline or unknown, with reasons",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				var v api.HostStatusView
				if err := getWith(cmd, client, "/hosts/"+url.PathEscape(args[0])+"/status", &v); err != nil {
					return err
				}
				if wantsJSON() {
					return printJSON(out, v)
				}
				return printStatusDetail(out, v)
			}
			var all []api.HostStatusView
			if err := getWith(cmd, client, "/hosts/_status", &all); err != nil {
				return err
			}
			if wantsJSON() {
				return printJSON(out, all)
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tSINCE\tLAST OK\tREASONS")
			for _, v := range all {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Status.Status, ago(&v.Status.Since), ago(v.Status.LastOK), reasonText(v.Status.Reasons))
			}
			return w.Flush()
		},
	}

	var since, until, grain string
	var sampleLimit int
	samples := &cobra.Command{
		Use:   "samples <name>",
		Short: "A host's retained measurements in time order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			for k, v := range map[string]string{"since": since, "until": until, "grain": grain} {
				if v != "" {
					q.Set(k, v)
				}
			}
			if sampleLimit != 0 {
				q.Set("limit", strconv.Itoa(sampleLimit))
			}
			path := "/hosts/" + url.PathEscape(args[0]) + "/samples"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			out := cmd.OutOrStdout()
			if grain != "" && grain != "raw" {
				var rs []hosts.Rollup
				if err := getWith(cmd, client, path, &rs); err != nil {
					return err
				}
				return printRollups(out, rs, wantsJSON())
			}
			var ss []hosts.Sample
			if err := getWith(cmd, client, path, &ss); err != nil {
				return err
			}
			if wantsJSON() {
				return printJSON(out, ss)
			}
			return printSamples(out, ss)
		},
	}
	samples.Flags().StringVar(&since, "since", "", "RFC 3339 time or a duration such as 6h")
	samples.Flags().StringVar(&until, "until", "", "RFC 3339 time or a duration ago")
	samples.Flags().StringVar(&grain, "grain", "", "raw (default), 5m, 1h or 1d rollups")
	samples.Flags().IntVar(&sampleLimit, "limit", 0, "most samples (default 500, the newest)")

	monitor := &cobra.Command{
		Use:   "monitor",
		Short: "What the host monitor is doing: settings and each host's next probe",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var snap api.MonitorSnapshot
			if err := getWith(cmd, client, "/hosts/_monitor", &snap); err != nil {
				return err
			}
			if wantsJSON() {
				return printJSON(cmd.OutOrStdout(), snap)
			}
			return printMonitor(cmd.OutOrStdout(), snap)
		},
	}

	var afterID int64
	var eventsHost string
	var eventsLimit int
	var follow bool
	events := &cobra.Command{
		Use:   "events",
		Short: "Host status-change events after an id (--follow keeps polling)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			last := afterID
			for {
				q := url.Values{}
				if last != 0 {
					q.Set("after_id", strconv.FormatInt(last, 10))
				}
				if eventsHost != "" {
					q.Set("host", eventsHost)
				}
				if eventsLimit != 0 {
					q.Set("limit", strconv.Itoa(eventsLimit))
				}
				path := "/hosts/_events"
				if len(q) > 0 {
					path += "?" + q.Encode()
				}
				var res api.EventsResult
				if err := getWith(cmd, client, path, &res); err != nil {
					return err
				}
				if err := printEvents(out, res, wantsJSON()); err != nil {
					return err
				}
				last = res.LastID
				if !follow {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
	events.Flags().Int64Var(&afterID, "after-id", 0, "events after this id")
	events.Flags().StringVar(&eventsHost, "host", "", "only this host")
	events.Flags().IntVar(&eventsLimit, "limit", 0, "most events per batch (default 200)")
	events.Flags().BoolVar(&follow, "follow", false, "keep polling for new events")

	group.AddCommand(list, get, probe, status, samples, monitor, events)
	return group
}

// getWith is one GET with the CLI's key.
func getWith(cmd *cobra.Command, client func() (*apiClient, error), path string, out any) error {
	cl, err := client()
	if err != nil {
		return err
	}
	return cl.get(cmd.Context(), path, out)
}

func printHostTable(out io.Writer, views []api.HostView, now time.Time) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATE\tROLE\tCPU\tMEM AVAIL\tDISK FREE\tPROBED")
	for _, v := range views {
		cpu, mem, disk := "-", "-", "-"
		if p := v.Probed; p != nil {
			cpu, mem, disk = hostCell(p.CPU > 0, fmt.Sprint(p.CPU)), hostCell(p.MemoryAvailable > 0, humanBytes(p.MemoryAvailable)), hostCell(p.DiskTotal > 0, humanBytes(p.DiskFree))
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Host.Name, v.State, hostCell(v.Host.Role != "", v.Host.Role), cpu, mem, disk, probedAge(v, now))
	}
	return w.Flush()
}

func hostCell(ok bool, s string) string {
	if !ok {
		return "-"
	}
	return s
}

// probedAge says when the facts were measured; stale facts say so beside their age.
func probedAge(v api.HostView, now time.Time) string {
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

// printHost is old's host record.
func printHost(out io.Writer, v api.HostView, asJSON bool, now time.Time) error {
	if asJSON {
		return printJSON(out, v)
	}
	h := v.Host
	_, _ = fmt.Fprintf(out, "%s  [%s]\n", h.Name, v.State)
	row := func(k, val string) {
		if val != "" {
			_, _ = fmt.Fprintf(out, "  %-14s %s\n", k+":", val)
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
		sort.Strings(kv)
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
		printParts(row, v)
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
		_, _ = fmt.Fprintf(out, "  No probe data: the host's facts are unknown. Run `cerberus host probe %s`.\n", h.Name)
	case v.Stale:
		_, _ = fmt.Fprintf(out, "  These facts are stale. Run `cerberus host probe %s` for current ones.\n", h.Name)
	}
	return nil
}

// printParts writes what the machine can take. A fact that was not recorded reads "not
// recorded", never zero.
func printParts(row func(k, val string), v api.HostView) {
	if v.StorageSummary == nil {
		row("storage slots", "not recorded (free bays unknown)")
	} else {
		var kinds []string
		for k := range v.StorageSummary {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		var parts []string
		for _, k := range kinds {
			c := v.StorageSummary[k]
			parts = append(parts, fmt.Sprintf("%s %d of %d used, %d free", k, c.Used, c.Total, c.Free))
		}
		if len(parts) == 0 {
			parts = []string{"none"}
		}
		row("storage slots", strings.Join(parts, "; "))
	}
	if c := v.Ceilings; c == nil {
		row("ceilings", "not recorded")
	} else {
		if m := c.Memory; m != nil {
			var parts []string
			if m.MaxTotal != nil {
				parts = append(parts, "max "+humanBytes(int64(m.MaxTotal.Value))+" ("+m.MaxTotal.Source+")")
			}
			if m.MaxPerSlot != nil {
				parts = append(parts, "per slot "+humanBytes(int64(m.MaxPerSlot.Value))+" ("+m.MaxPerSlot.Source+")")
			}
			row("mem ceiling", strings.Join(parts, ", "))
		}
		if m := c.CPU; m != nil && m.MaxSupported != nil {
			row("cpu ceiling", fmt.Sprintf("%d cores %d threads %dW (%s)", m.MaxSupported.Cores, m.MaxSupported.Threads, m.MaxSupported.TDPW, m.MaxSupported.Source))
		}
		if len(c.PCIeSlots) > 0 {
			free := 0
			for _, p := range c.PCIeSlots {
				if p.OccupantComponent == "" {
					free++
				}
			}
			row("pcie slots", fmt.Sprintf("%d, %d free", len(c.PCIeSlots), free))
		}
	}
	for _, c := range v.Components {
		row("component", strings.Join(nonEmpty(c.ID, c.Kind, sizeOrEmpty(int64(c.Size)), c.Type, "@"+c.Location.Slot), " "))
	}
	for _, w := range v.Warnings {
		row("warning", w)
	}
}

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

func ago(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return time.Since(*t).Round(time.Second).String() + " ago"
}

func reasonText(rs []hosts.Reason) string {
	if len(rs) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.Code+": "+r.Detail)
	}
	return strings.Join(parts, "; ")
}

func printStatusDetail(out io.Writer, v api.HostStatusView) error {
	_, _ = fmt.Fprintf(out, "%s  [%s]\n", v.Name, v.Status.Status)
	row := func(k, val string) {
		if val != "" && val != "-" {
			_, _ = fmt.Fprintf(out, "  %-14s %s\n", k+":", val)
		}
	}
	row("monitored", fmt.Sprint(v.Monitored))
	row("interval", v.Interval)
	row("since", ago(&v.Status.Since))
	row("last ok", ago(v.Status.LastOK))
	row("last attempt", ago(v.Status.LastAttempt))
	if v.Status.NextDue != nil {
		row("next probe", v.Status.NextDue.UTC().Format(time.RFC3339))
	}
	if v.Status.ConsecutiveFailures > 0 {
		row("failures", fmt.Sprint(v.Status.ConsecutiveFailures, " in a row"))
	}
	for _, r := range v.Status.Reasons {
		_, _ = fmt.Fprintf(out, "  reason:        %s: %s\n", r.Code, r.Detail)
	}
	t := v.Thresholds
	_, err := fmt.Fprintf(out, "  thresholds:    disk_low <%.0f%% or <%s free; memory_low <%.0f%% available; load_high >%gx cores for %d samples; offline after %d failed probes; degraded needs %d consistent results; stale after %d intervals\n",
		t.DiskFreePercent, humanBytes(t.DiskFreeBytes), t.MemoryAvailablePercent, t.LoadFactor, t.LoadSamples, t.OfflineAfter, t.DegradeConfirm, t.StaleIntervals)
	return err
}

func printSamples(out io.Writer, ss []hosts.Sample) error {
	if len(ss) == 0 {
		_, err := fmt.Fprintln(out, "No samples.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "TIME\tLOAD1\tMEM AVAIL\tDISK FREE\tUPTIME\tPROBE")
	for _, s := range ss {
		load := "-"
		if s.Load1 != nil {
			load = fmt.Sprintf("%.2f", *s.Load1)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%dms\n", s.At.Format("2006-01-02T15:04:05Z"), load, humanBytes(s.MemoryAvailable), humanBytes(s.DiskFree),
			(time.Duration(s.UptimeSeconds) * time.Second).String(), s.ProbeMS)
	}
	return w.Flush()
}

func printRollups(out io.Writer, rs []hosts.Rollup, asJSON bool) error {
	if asJSON {
		return printJSON(out, rs)
	}
	if len(rs) == 0 {
		_, err := fmt.Fprintln(out, "No rollups.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "BUCKET\tN\tLOAD1 MIN/AVG/MAX\tMEM AVAIL MIN\tDISK FREE MIN\tPROBE AVG/MAX")
	for _, r := range rs {
		load, mem, disk, probe := "-", "-", "-", "-"
		if r.Load1 != nil {
			load = fmt.Sprintf("%.2f/%.2f/%.2f", r.Load1.Min, r.Load1.Avg, r.Load1.Max)
		}
		if r.MemoryAvailable != nil {
			mem = humanBytes(int64(r.MemoryAvailable.Min))
		}
		if r.DiskFree != nil {
			disk = humanBytes(int64(r.DiskFree.Min))
		}
		if r.ProbeMS != nil {
			probe = fmt.Sprintf("%.0f/%.0fms", r.ProbeMS.Avg, r.ProbeMS.Max)
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", r.At.Format("2006-01-02T15:04Z"), r.N, load, mem, disk, probe)
	}
	return w.Flush()
}

func printMonitor(out io.Writer, snap api.MonitorSnapshot) error {
	s := snap.Settings
	_, _ = fmt.Fprintf(out, "monitor: enabled=%t running=%t  outpost every %s, ssh every %s (floor %s), jitter ±%.0f%%, backoff to %s, %d at a time (%d over ssh), probe timeout %s\n",
		snap.Enabled, snap.Running, s.OutpostInterval, s.SSHInterval, s.MinInterval, s.Jitter*100, s.MaxBackoff, s.Concurrency, s.SSHConcurrency, s.ProbeTimeout)
	if !snap.Enabled {
		_, _ = fmt.Fprintln(out, "Monitoring is off: set host_monitor.enabled: true in the daemon config and restart the daemon.")
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVIA\tEVERY\tLAST ATTEMPT\tNEXT\tFAILURES")
	for _, h := range snap.Hosts {
		next := "-"
		if h.NextDue != nil {
			next = time.Until(*h.NextDue).Round(time.Second).String()
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", h.Name, h.Transport, h.Interval, ago(h.LastAttempt), next, h.ConsecutiveFailures)
	}
	return w.Flush()
}

func printEvents(out io.Writer, res api.EventsResult, asJSON bool) error {
	if res.Gap != nil {
		_, _ = fmt.Fprintf(out, "gap: events up to id %d were pruned (the log now starts at %d)\n", res.Gap.AfterID, res.Gap.OldestID)
	}
	for _, e := range res.Events {
		if asJSON {
			if err := printJSON(out, e); err != nil {
				return err
			}
			continue
		}
		exp := ""
		if e.Expected {
			exp = " (expected)"
		}
		_, _ = fmt.Fprintf(out, "%d  %s  %s  %s -> %s  %s%s\n", e.ID, e.At.Format("2006-01-02T15:04:05Z"), e.Host, e.From, e.To, strings.Join(e.Reasons, ","), exp)
	}
	return nil
}
