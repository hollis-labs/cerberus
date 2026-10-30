package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

func (l localHostClient) HostStatus(ctx context.Context, name string) (cerbapi.HostStatusView, error) {
	return l.svc.HostStatus(inProcessContext(ctx), name)
}
func (l localHostClient) AllHostStatus(ctx context.Context) ([]cerbapi.HostStatusView, error) {
	return l.svc.AllHostStatus(inProcessContext(ctx))
}
func (l localHostClient) HostSamples(ctx context.Context, name string, q cerbapi.SamplesQuery) ([]hosts.Sample, error) {
	return l.svc.HostSamples(inProcessContext(ctx), name, q)
}
func (l localHostClient) HostRollups(ctx context.Context, name, grain string, q cerbapi.SamplesQuery) ([]hosts.Rollup, error) {
	return l.svc.HostRollups(inProcessContext(ctx), name, grain, q)
}
func (l localHostClient) MonitorState(ctx context.Context) (cerbapi.MonitorSnapshot, error) {
	return l.svc.MonitorState(inProcessContext(ctx))
}

func (l localHostClient) HostEvents(ctx context.Context, q cerbapi.EventsQuery) (cerbapi.EventsResult, error) {
	return l.svc.HostEvents(inProcessContext(ctx), q)
}
func (l localHostClient) SubscribeHostEvents(ctx context.Context) (<-chan struct{}, func(), error) {
	return nil, nil, fmt.Errorf("host monitoring is not available from this serving host: events come from the daemon")
}

var eventsOpts struct {
	afterID int64
	host    string
	limit   int
	follow  bool
}

var hostEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Host status-change events after an id (--follow keeps polling)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		ec := cerbapi.HostEventsClientOf(client)
		out := cmd.OutOrStdout()
		last := eventsOpts.afterID
		for {
			res, err := ec.HostEvents(cmd.Context(), cerbapi.EventsQuery{AfterID: last, Host: eventsOpts.host, Limit: eventsOpts.limit})
			if err != nil {
				return err
			}
			if res.Gap != nil {
				fmt.Fprintf(out, "gap: events up to id %d were pruned (the log now starts at %d)\n", res.Gap.AfterID, res.Gap.OldestID)
			}
			if hostWantsJSON() {
				for _, e := range res.Events {
					if err := writeJSON(out, e); err != nil {
						return err
					}
				}
			} else {
				for _, e := range res.Events {
					exp := ""
					if e.Expected {
						exp = " (expected)"
					}
					fmt.Fprintf(out, "%d  %s  %s  %s -> %s  %s%s\n", e.ID, e.At.Format("2006-01-02T15:04:05Z"), e.Host, e.From, e.To, strings.Join(e.Reasons, ","), exp)
				}
			}
			last = res.LastID
			if !eventsOpts.follow {
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

func monitorClient(cmd *cobra.Command) (cerbapi.HostMonitorClient, error) {
	client, err := newHostClient(cmd)
	if err != nil {
		return nil, err
	}
	return cerbapi.HostMonitorClientOf(client), nil
}

var hostStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Health of a host, or of every host: online, degraded, offline or unknown, with reasons",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mc, err := monitorClient(cmd)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(args) == 1 {
			v, err := mc.HostStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if hostWantsJSON() {
				return writeJSON(out, v)
			}
			return printStatusDetail(out, v)
		}
		all, err := mc.AllHostStatus(cmd.Context())
		if err != nil {
			return err
		}
		if hostWantsJSON() {
			return writeJSON(out, all)
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tSINCE\tLAST OK\tREASONS")
		for _, v := range all {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Status.Status, ago(&v.Status.Since), ago(v.Status.LastOK), reasonText(v.Status.Reasons))
		}
		return w.Flush()
	},
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
	s := ""
	for i, r := range rs {
		if i > 0 {
			s += "; "
		}
		s += r.Code + ": " + r.Detail
	}
	return s
}

func printStatusDetail(out io.Writer, v cerbapi.HostStatusView) error {
	fmt.Fprintf(out, "%s  [%s]\n", v.Name, v.Status.Status)
	row := func(k, val string) {
		if val != "" && val != "-" {
			fmt.Fprintf(out, "  %-14s %s\n", k+":", val)
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
		fmt.Fprintf(out, "  reason:        %s: %s\n", r.Code, r.Detail)
	}
	t := v.Thresholds
	fmt.Fprintf(out, "  thresholds:    disk_low <%.0f%% or <%s free; memory_low <%.0f%% available; load_high >%gx cores for %d samples; offline after %d failed probes; degraded needs %d consistent results; stale after %d intervals\n",
		t.DiskFreePercent, humanBytes(t.DiskFreeBytes), t.MemoryAvailablePercent, t.LoadFactor, t.LoadSamples, t.OfflineAfter, t.DegradeConfirm, t.StaleIntervals)
	return nil
}

var samplesOpts struct {
	since, until, grain string
	limit               int
}

var hostSamplesCmd = &cobra.Command{
	Use:   "samples <name>",
	Short: "A host's retained measurements in time order",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mc, err := monitorClient(cmd)
		if err != nil {
			return err
		}
		now := time.Now()
		since, err := cerbapi.ParseSamplesTime(samplesOpts.since, now)
		if err != nil {
			return err
		}
		until, err := cerbapi.ParseSamplesTime(samplesOpts.until, now)
		if err != nil {
			return err
		}
		q := cerbapi.SamplesQuery{Since: since, Until: until, Limit: samplesOpts.limit}
		if g := samplesOpts.grain; g != "" && g != "raw" {
			rs, err := mc.HostRollups(cmd.Context(), args[0], g, q)
			if err != nil {
				return err
			}
			return printRollups(cmd.OutOrStdout(), rs)
		}
		ss, err := mc.HostSamples(cmd.Context(), args[0], q)
		if err != nil {
			return err
		}
		if hostWantsJSON() {
			return writeJSON(cmd.OutOrStdout(), ss)
		}
		if len(ss) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "No samples.")
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tLOAD1\tMEM AVAIL\tDISK FREE\tUPTIME\tPROBE")
		for _, s := range ss {
			load := "-"
			if s.Load1 != nil {
				load = fmt.Sprintf("%.2f", *s.Load1)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%dms\n", s.At.Format("2006-01-02T15:04:05Z"), load, humanBytes(s.MemoryAvailable), humanBytes(s.DiskFree),
				(time.Duration(s.UptimeSeconds) * time.Second).String(), s.ProbeMS)
		}
		return w.Flush()
	},
}

func printRollups(out io.Writer, rs []hosts.Rollup) error {
	if hostWantsJSON() {
		return writeJSON(out, rs)
	}
	if len(rs) == 0 {
		_, err := fmt.Fprintln(out, "No rollups.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUCKET\tN\tLOAD1 MIN/AVG/MAX\tMEM AVAIL MIN\tDISK FREE MIN\tPROBE AVG/MAX")
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
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", r.At.Format("2006-01-02T15:04Z"), r.N, load, mem, disk, probe)
	}
	return w.Flush()
}

var hostMonitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "What the host monitor is doing: settings and each host's next probe",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mc, err := monitorClient(cmd)
		if err != nil {
			return err
		}
		snap, err := mc.MonitorState(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if hostWantsJSON() {
			return writeJSON(out, snap)
		}
		s := snap.Settings
		fmt.Fprintf(out, "monitor: enabled=%t running=%t  outpost every %s, ssh every %s (floor %s), jitter ±%.0f%%, backoff to %s, %d at a time (%d over ssh), probe timeout %s\n",
			snap.Enabled, snap.Running, s.OutpostInterval, s.SSHInterval, s.MinInterval, s.Jitter*100, s.MaxBackoff, s.Concurrency, s.SSHConcurrency, s.ProbeTimeout)
		if !snap.Enabled {
			fmt.Fprintln(out, "Monitoring is off: set host_monitor.enabled: true in the daemon config and restart the daemon.")
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVIA\tEVERY\tLAST ATTEMPT\tNEXT\tFAILURES")
		for _, h := range snap.Hosts {
			next := "-"
			if h.NextDue != nil {
				next = time.Until(*h.NextDue).Round(time.Second).String()
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", h.Name, h.Transport, h.Interval, ago(h.LastAttempt), next, h.ConsecutiveFailures)
		}
		return w.Flush()
	},
}

func init() {
	hostSamplesCmd.Flags().StringVar(&samplesOpts.since, "since", "", "RFC 3339 time or a duration such as 6h")
	hostSamplesCmd.Flags().StringVar(&samplesOpts.until, "until", "", "RFC 3339 time or a duration ago")
	hostSamplesCmd.Flags().StringVar(&samplesOpts.grain, "grain", "", "raw (default), 5m, 1h or 1d rollups")
	hostSamplesCmd.Flags().IntVar(&samplesOpts.limit, "limit", 0, "most samples (default 500, the newest)")
	hostEventsCmd.Flags().Int64Var(&eventsOpts.afterID, "after-id", 0, "events after this id")
	hostEventsCmd.Flags().StringVar(&eventsOpts.host, "host", "", "only this host")
	hostEventsCmd.Flags().IntVar(&eventsOpts.limit, "limit", 0, "most events per batch (default 200)")
	hostEventsCmd.Flags().BoolVar(&eventsOpts.follow, "follow", false, "keep polling for new events")
	hostCmd.AddCommand(hostStatusCmd, hostSamplesCmd, hostMonitorCmd, hostEventsCmd)
}
