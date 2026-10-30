package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

// localHostClient also serves the write side, in the operator's own shell.
func (l localHostClient) HostsRevision(ctx context.Context) (string, error) {
	return l.svc.Revision(inProcessContext(ctx))
}
func (l localHostClient) UpdateHost(ctx context.Context, a cerbapi.HostUpdateArgs) (cerbapi.WriteResult, error) {
	return l.svc.UpdateHost(inProcessContext(ctx), a)
}
func (l localHostClient) RemoveHost(ctx context.Context, a cerbapi.HostRemoveArgs) (cerbapi.WriteResult, error) {
	return l.svc.RemoveHost(inProcessContext(ctx), a)
}
func (l localHostClient) CreateComponent(ctx context.Context, a cerbapi.ComponentCreateArgs) (cerbapi.WriteResult, error) {
	return l.svc.CreateComponent(inProcessContext(ctx), a)
}
func (l localHostClient) UpdateComponent(ctx context.Context, a cerbapi.ComponentUpdateArgs) (cerbapi.WriteResult, error) {
	return l.svc.UpdateComponent(inProcessContext(ctx), a)
}
func (l localHostClient) MoveComponent(ctx context.Context, a cerbapi.ComponentMoveArgs) (cerbapi.WriteResult, error) {
	return l.svc.MoveComponent(inProcessContext(ctx), a)
}
func (l localHostClient) RemoveComponent(ctx context.Context, a cerbapi.ComponentRemoveArgs) (cerbapi.WriteResult, error) {
	return l.svc.RemoveComponent(inProcessContext(ctx), a)
}
func (l localHostClient) InventoryHost(ctx context.Context, a cerbapi.InventoryArgs) (cerbapi.InventoryResult, error) {
	return l.svc.Inventory(inProcessContext(ctx), a)
}
func (l localHostClient) HostHistory(ctx context.Context, name string, limit int) ([]hosts.Change, error) {
	return l.svc.HostHistory(inProcessContext(ctx), name, limit)
}
func (l localHostClient) ComponentHistory(ctx context.Context, id string, limit int) ([]hosts.Change, error) {
	return l.svc.ComponentHistory(inProcessContext(ctx), id, limit)
}

// writeFlags are shared by every write command.
type writeFlags struct {
	ack      bool
	approval string
	revision string
}

func (w *writeFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&w.ack, "ack", false, "acknowledge this change to hosts.yaml (required)")
	cmd.Flags().StringVar(&w.approval, "approval-id", "", "out-of-band approval bound to this change")
	cmd.Flags().StringVar(&w.revision, "revision", "", "hosts.yaml revision this change is based on (default: the current one)")
}

// args resolves the revision: the one pinned with --revision, else the current
// one, read just before the write. The change itself is a patch applied to the
// file as it is under the lock, so a stale default cannot overwrite another
// writer's fields.
func (w *writeFlags) args(ctx context.Context, wc cerbapi.HostWriteClient) (cerbapi.WriteArgs, error) {
	rev := w.revision
	if rev == "" {
		var err error
		if rev, err = wc.HostsRevision(ctx); err != nil {
			return cerbapi.WriteArgs{}, err
		}
	}
	return cerbapi.WriteArgs{Revision: rev, Acknowledged: w.ack, ApprovalID: w.approval}, nil
}

func printWrite(out io.Writer, res cerbapi.WriteResult) error {
	if hostWantsJSON() {
		return writeJSON(out, res)
	}
	if !res.Changed {
		fmt.Fprintf(out, "%s %s: no change (revision %s)\n", res.Kind, res.Name, res.Revision)
		return nil
	}
	fmt.Fprintf(out, "%s %s: %s (revision %s)\n", res.Kind, res.Name, res.Op, res.Revision)
	keys := map[string]bool{}
	for k := range res.Before {
		keys[k] = true
	}
	for k := range res.After {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sortStrings(sorted)
	for _, k := range sorted {
		fmt.Fprintf(out, "  %s: %v -> %v\n", k, showValue(res.Before[k]), showValue(res.After[k]))
	}
	if res.Backup != "" {
		fmt.Fprintf(out, "  backup: %s\n", res.Backup)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "  warning: %s\n", w)
	}
	return nil
}

func showValue(v any) string {
	if v == nil {
		return "(unset)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func printHistory(out io.Writer, changes []hosts.Change) error {
	if hostWantsJSON() {
		return writeJSON(out, changes)
	}
	if len(changes) == 0 {
		_, err := fmt.Fprintln(out, "No recorded changes.")
		return err
	}
	for _, c := range changes {
		fmt.Fprintf(out, "%s  %s  %s %s  by %s\n", c.Time.Format("2006-01-02T15:04:05Z"), c.Op, c.Kind, c.Name, c.Caller)
		keys := map[string]bool{}
		for k := range c.Before {
			keys[k] = true
		}
		for k := range c.After {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sortStrings(sorted)
		for _, k := range sorted {
			fmt.Fprintf(out, "    %s: %s -> %s\n", k, showValue(c.Before[k]), showValue(c.After[k]))
		}
	}
	return nil
}

// readPatch builds a merge patch from --patch / --patch-file ("-" is stdin).
func readPatch(inline, file string, in io.Reader) (map[string]any, error) {
	var data []byte
	switch {
	case inline != "":
		data = []byte(inline)
	case file == "-":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		data = b
	case file != "":
		b, err := os.ReadFile(file) //nolint:gosec // operator-named file
		if err != nil {
			return nil, err
		}
		data = b
	default:
		return map[string]any{}, nil
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the patch is not a JSON object: %w", err)
	}
	return m, nil
}

func setPath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, _ := m[p].(map[string]any)
		if next == nil {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

func writeClient(cmd *cobra.Command) (cerbapi.HostWriteClient, error) {
	client, err := newHostClient(cmd)
	if err != nil {
		return nil, err
	}
	return cerbapi.HostWriteClientOf(client), nil
}

var hostSetOpts struct {
	writeFlags
	role, owner, patch, patchFile string
	labels, unlabels              []string
	create                        bool
}

var hostSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Change a host in hosts.yaml (validated, backed up, recorded in the history)",
	Long: `Change one host with flags and/or a JSON merge patch (--patch, --patch-file;
objects merge, null deletes a field, lists replace). The whole resulting
hosts.yaml is validated before anything is written; a bad change writes
nothing. Example:

  cerberus host set worker-1 --ack --patch '{"hardware":{"memory":{"total":"16GiB"}}}'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := &hostSetOpts
		patch, err := readPatch(o.patch, o.patchFile, cmd.InOrStdin())
		if err != nil {
			return err
		}
		if o.role != "" {
			patch["role"] = o.role
		}
		if o.owner != "" {
			patch["owner"] = o.owner
		}
		labels, err := parseLabels(o.labels)
		if err != nil {
			return err
		}
		for k, v := range labels {
			setPath(patch, "labels."+k, v)
		}
		for _, k := range o.unlabels {
			setPath(patch, "labels."+k, nil)
		}
		if len(patch) == 0 && !o.create {
			return fmt.Errorf("nothing to change: give --role, --owner, --label, --unlabel, --patch or --patch-file")
		}
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		wa, err := o.args(cmd.Context(), wc)
		if err != nil {
			return err
		}
		res, err := wc.UpdateHost(cmd.Context(), cerbapi.HostUpdateArgs{WriteArgs: wa, Name: args[0], Patch: patch, Create: o.create})
		if err != nil {
			return err
		}
		return printWrite(cmd.OutOrStdout(), res)
	},
}

var hostRemoveOpts writeFlags

var hostRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a host from hosts.yaml (refused while components are located on it)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		wa, err := hostRemoveOpts.args(cmd.Context(), wc)
		if err != nil {
			return err
		}
		res, err := wc.RemoveHost(cmd.Context(), cerbapi.HostRemoveArgs{WriteArgs: wa, Name: args[0]})
		if err != nil {
			return err
		}
		return printWrite(cmd.OutOrStdout(), res)
	},
}

var hostInventoryOpts struct {
	writeFlags
	dryRun bool
}

var hostInventoryCmd = &cobra.Command{
	Use:   "inventory <name>",
	Short: "Collect a host's hardware over ssh and record it (keeps notes, storage_slots, vendor ceilings, components)",
	Long: `Runs a fixed read-only collection script on the host over its reach.ssh
(dmidecode and lsblk under ` + "`sudo -n`" + `; without passwordless sudo only the
non-root facts are collected and the recorded serials and memory layout are
kept) and records the result through the same validated, backed-up, audited
and recorded write as ` + "`host set`" + `. --dry-run collects and shows the change
without writing. Replaces sysadmin/fleet/fleet-inventory.py.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := &hostInventoryOpts
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		res, err := wc.InventoryHost(cmd.Context(), cerbapi.InventoryArgs{
			WriteArgs: cerbapi.WriteArgs{Revision: o.revision, Acknowledged: o.ack, ApprovalID: o.approval}, Name: args[0], DryRun: o.dryRun})
		if err != nil {
			return err
		}
		if o.dryRun && !hostWantsJSON() {
			fmt.Fprintln(cmd.OutOrStdout(), "dry run: nothing was written")
		}
		return printWrite(cmd.OutOrStdout(), res.WriteResult)
	},
}

var historyLimit int

var hostHistoryCmd = &cobra.Command{
	Use:   "history <name>",
	Short: "Show the change history of a host, newest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		changes, err := wc.HostHistory(cmd.Context(), args[0], historyLimit)
		if err != nil {
			return err
		}
		return printHistory(cmd.OutOrStdout(), changes)
	},
}

// ---- components ----

type componentOpts struct {
	writeFlags
	id, kind, size, typ, speed, serial, notes string
	host, slot                                string
	spare                                     bool
	patch, patchFile                          string
}

var compOpts componentOpts

func (o *componentOpts) fields(cmd *cobra.Command) (map[string]any, error) {
	m, err := readPatch(o.patch, o.patchFile, cmd.InOrStdin())
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"kind": o.kind, "size": o.size, "type": o.typ, "speed": o.speed, "serial": o.serial, "notes": o.notes} {
		if v != "" {
			m[k] = v
		}
	}
	return m, nil
}

var componentCreateCmd = &cobra.Command{
	Use:   "create --id ID --kind ram|disk|cpu|other (--spare | --host H --slot S) [--size 16GiB ...]",
	Short: "Add a spare or installed part",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		o := &compOpts
		m, err := o.fields(cmd)
		if err != nil {
			return err
		}
		if o.id != "" {
			m["id"] = o.id
		}
		loc := map[string]any{}
		switch {
		case o.spare:
			loc["spare"] = true
		case o.host != "" || o.slot != "":
			loc["host"], loc["slot"] = o.host, o.slot
		}
		m["location"] = loc
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		wa, err := o.args(cmd.Context(), wc)
		if err != nil {
			return err
		}
		res, err := wc.CreateComponent(cmd.Context(), cerbapi.ComponentCreateArgs{WriteArgs: wa, Component: m})
		if err != nil {
			return err
		}
		return printWrite(cmd.OutOrStdout(), res)
	},
}

var componentUpdateCmd = &cobra.Command{
	Use:   "update <id> [--size 16GiB] [--type ...] [--serial ...] [--notes ...] [--patch JSON]",
	Short: "Change a part's fields (not its location: use move)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := &compOpts
		m, err := o.fields(cmd)
		if err != nil {
			return err
		}
		if len(m) == 0 {
			return fmt.Errorf("nothing to change: give --size, --type, --speed, --serial, --notes or --patch")
		}
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		wa, err := o.args(cmd.Context(), wc)
		if err != nil {
			return err
		}
		res, err := wc.UpdateComponent(cmd.Context(), cerbapi.ComponentUpdateArgs{WriteArgs: wa, ID: args[0], Patch: m})
		if err != nil {
			return err
		}
		return printWrite(cmd.OutOrStdout(), res)
	},
}

var componentMoveCmd = &cobra.Command{
	Use:   "move <id> (--spare | --host H --slot S)",
	Short: "Move a part to a slot on a host, or to the spares",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := &compOpts
		var to hosts.ComponentLocation
		switch {
		case o.spare:
			to.Spare = true
		case o.host != "" && o.slot != "":
			to.Host, to.Slot = o.host, o.slot
		default:
			return fmt.Errorf("say where: --spare, or --host and --slot")
		}
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		wa, err := o.args(cmd.Context(), wc)
		if err != nil {
			return err
		}
		res, err := wc.MoveComponent(cmd.Context(), cerbapi.ComponentMoveArgs{WriteArgs: wa, ID: args[0], To: to})
		if err != nil {
			return err
		}
		return printWrite(cmd.OutOrStdout(), res)
	},
}

var componentRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Delete a part and free the slot it held",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		wa, err := compOpts.args(cmd.Context(), wc)
		if err != nil {
			return err
		}
		res, err := wc.RemoveComponent(cmd.Context(), cerbapi.ComponentRemoveArgs{WriteArgs: wa, ID: args[0]})
		if err != nil {
			return err
		}
		return printWrite(cmd.OutOrStdout(), res)
	},
}

var componentHistoryCmd = &cobra.Command{
	Use:   "history <id>",
	Short: "Show the change history of a part, newest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wc, err := writeClient(cmd)
		if err != nil {
			return err
		}
		changes, err := wc.ComponentHistory(cmd.Context(), args[0], historyLimit)
		if err != nil {
			return err
		}
		return printHistory(cmd.OutOrStdout(), changes)
	},
}

func init() {
	o := &hostSetOpts
	o.add(hostSetCmd)
	f := hostSetCmd.Flags()
	f.StringVar(&o.role, "role", "", "set the role")
	f.StringVar(&o.owner, "owner", "", "set the owner")
	f.StringArrayVar(&o.labels, "label", nil, "set a label, key=value (repeatable)")
	f.StringArrayVar(&o.unlabels, "unlabel", nil, "remove a label by key (repeatable)")
	f.StringVar(&o.patch, "patch", "", "JSON merge patch of the host record")
	f.StringVar(&o.patchFile, "patch-file", "", "file with the JSON merge patch (- for stdin)")
	f.BoolVar(&o.create, "create", false, "create the host when it does not exist")
	hostInventoryOpts.add(hostInventoryCmd)
	hostInventoryCmd.Flags().BoolVar(&hostInventoryOpts.dryRun, "dry-run", false, "collect and show the change; write nothing")
	hostCmd.AddCommand(hostInventoryCmd)
	hostRemoveOpts.add(hostRemoveCmd)
	hostHistoryCmd.Flags().IntVar(&historyLimit, "limit", 0, "most entries (default 100)")
	hostCmd.AddCommand(hostSetCmd, hostRemoveCmd, hostHistoryCmd)

	c := &compOpts
	for _, cmd := range []*cobra.Command{componentCreateCmd, componentUpdateCmd, componentMoveCmd, componentRemoveCmd} {
		c.add(cmd)
	}
	for _, cmd := range []*cobra.Command{componentCreateCmd, componentUpdateCmd} {
		fl := cmd.Flags()
		fl.StringVar(&c.kind, "kind", "", "ram|disk|cpu|other")
		fl.StringVar(&c.size, "size", "", "size, such as 16GiB or 2TB")
		fl.StringVar(&c.typ, "type", "", "type, such as DDR4 or hdd")
		fl.StringVar(&c.speed, "speed", "", "speed, such as 3200")
		fl.StringVar(&c.serial, "serial", "", "serial number")
		fl.StringVar(&c.notes, "notes", "", "notes")
		fl.StringVar(&c.patch, "patch", "", "JSON object of further fields")
		fl.StringVar(&c.patchFile, "patch-file", "", "file with a JSON object of further fields (- for stdin)")
	}
	componentCreateCmd.Flags().StringVar(&c.id, "id", "", "component id")
	for _, cmd := range []*cobra.Command{componentCreateCmd, componentMoveCmd} {
		fl := cmd.Flags()
		fl.StringVar(&c.host, "host", "", "host the part is installed in")
		fl.StringVar(&c.slot, "slot", "", "slot on that host")
		fl.BoolVar(&c.spare, "spare", false, "the part is a spare")
	}
	componentHistoryCmd.Flags().IntVar(&historyLimit, "limit", 0, "most entries (default 100)")
	componentCmd.AddCommand(componentCreateCmd, componentUpdateCmd, componentMoveCmd, componentRemoveCmd, componentHistoryCmd)
}
