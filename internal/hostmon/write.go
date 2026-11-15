package hostmon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The host and component writes and their history, main's cerbapi host_write.go and
// host_inventory.go moved: hosts.yaml is changed through internal/hosts' Editor (revision,
// lock, backup, atomic write, as main), each change is gated and audited as the host contract's
// operation, and each applied change is appended to hosts.history.jsonl beside it.

// ErrConflict marks a revision conflict: hosts.yaml changed since the caller read it.
var ErrConflict = errors.New("revision conflict")

// ErrRevisionRequired marks a write without the revision the caller read.
var ErrRevisionRequired = errors.New("revision required")

// WriteArgs are what every write carries.
type WriteArgs struct {
	Revision     string `json:"revision"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
	ApprovalID   string `json:"approval_id,omitempty"`
}

// HostUpdateArgs patch one host (a JSON merge patch), creating it when Create is set.
type HostUpdateArgs struct {
	WriteArgs
	Name   string         `json:"name"`
	Patch  map[string]any `json:"patch"`
	Create bool           `json:"create,omitempty"`
}

// HostRemoveArgs remove one host.
type HostRemoveArgs struct {
	WriteArgs
	Name string `json:"name"`
}

// ComponentCreateArgs add one component.
type ComponentCreateArgs struct {
	WriteArgs
	Component map[string]any `json:"component"`
}

// ComponentUpdateArgs patch one component.
type ComponentUpdateArgs struct {
	WriteArgs
	ID    string         `json:"id"`
	Patch map[string]any `json:"patch"`
}

// ComponentMoveArgs move one component.
type ComponentMoveArgs struct {
	WriteArgs
	ID string                  `json:"id"`
	To hosts.ComponentLocation `json:"to"`
}

// ComponentRemoveArgs remove one component.
type ComponentRemoveArgs struct {
	WriteArgs
	ID string `json:"id"`
}

// InventoryArgs collect one host's hardware over ssh into its record.
type InventoryArgs struct {
	WriteArgs
	Name   string `json:"name"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// WriteResult is main's: what a write changed.
type WriteResult struct {
	Op       string         `json:"op"`
	Kind     string         `json:"kind"`
	Name     string         `json:"name"`
	Changed  bool           `json:"changed"`
	Revision string         `json:"revision"`
	Before   map[string]any `json:"before,omitempty"`
	After    map[string]any `json:"after,omitempty"`
	Backup   string         `json:"backup,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

// InventoryResult is main's.
type InventoryResult struct {
	WriteResult
	DryRun bool `json:"dry_run,omitempty"`
}

// Writer changes hosts.yaml. Gate records and gates every write; Prober reaches a host over ssh
// for the inventory.
type Writer struct {
	HostsPath string
	Gate      *gate.Gate
	Prober    *Prober
	Resources gate.ResourceLookup
	Logger    *slog.Logger
	Now       func() time.Time
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Writer) editor() *hosts.Editor { return &hosts.Editor{Path: w.HostsPath, Now: w.Now} }

func (w *Writer) history() hosts.History { return hosts.HistoryFor(w.HostsPath) }

// Revision is hosts.yaml's current revision.
func (w *Writer) Revision() (string, error) { return w.editor().Revision() }

// invalid classifies a write's error: not found, conflict and revision required keep their
// class; anything else is the caller's input (main's invalid).
func invalid(err error) error {
	var rc *hosts.RevisionConflictError
	var rr hosts.RevisionRequiredError
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalid), errors.Is(err, ErrConflict), errors.Is(err, ErrRevisionRequired):
		return err
	case errors.As(err, &rc):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case errors.As(err, &rr):
		return fmt.Errorf("%w: %w", ErrRevisionRequired, err)
	}
	var coded *gate.Error
	if errors.As(err, &coded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInvalid, redact.Guidance("%s", err.Error()))
}

func notFound(kind, name string) error {
	if kind == "component" {
		return fmt.Errorf("%w: %w", ErrNotFound, redact.Guidance("component %q is not recorded in hosts.yaml", name))
	}
	return fmt.Errorf("%w: %w", ErrNotFound, redact.Guidance("host %q is not declared; see `cerberus host list` (hosts are declared in hosts.yaml)", name))
}

type writePlan struct {
	op, kind, name string
	args           WriteArgs
	config         map[string]any
	// input is what the caller sent, scanned for credential material before anything runs.
	input        any
	mutate       func(*hosts.File) error
	prepare      func(ctx context.Context, call *gate.Call) error
	autoRevision bool
	dryRun       bool
	entity       func(*hosts.File) any
	hosts        func(before, after *hosts.File) []string
}

// write is main's HostService.write: refuse a missing revision and credential material, enter
// the gate, require acknowledgment, prepare, apply (retrying a conflict for an auto-revision
// write), diff, and record the change in the history.
func (w *Writer) write(ctx context.Context, p writePlan) (_ WriteResult, retErr error) {
	if p.args.Revision == "" && !p.autoRevision {
		return WriteResult{}, invalid(hosts.RevisionRequiredError{})
	}
	if where := hosts.FindSecret("", p.input); where != "" {
		return WriteResult{}, invalid(fmt.Errorf("%s looks like credential material, which hosts.yaml never holds; nothing was written", where))
	}
	def, known := HostDefinition().Operation(p.op)
	call, err := w.Gate.Begin(ctx, gate.Spec{Connector: "host", Operation: p.op, Op: def, Known: known, Config: p.config,
		Acknowledged: p.args.Acknowledged, ApprovalID: p.args.ApprovalID, DryRun: p.dryRun, Resources: w.Resources})
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { call.Finish(retErr) }()
	if def.RequiresAck && !p.args.Acknowledged {
		return WriteResult{}, gate.NewError("host", p.op, gate.CodeAckRequired, redact.Guidance("%s operation %q requires operator acknowledgment", def.Effect, p.op))
	}
	if p.prepare != nil {
		if err = p.prepare(ctx, call); err != nil {
			return WriteResult{}, invalid(err)
		}
	}
	var applied *hosts.Applied
	if p.dryRun {
		applied, err = w.editor().Preview(p.mutate)
	} else {
		rev := p.args.Revision
		for attempt := 0; ; attempt++ {
			if rev == "" {
				if rev, err = w.editor().Revision(); err != nil {
					return WriteResult{}, err
				}
			}
			applied, err = w.editor().Apply(rev, p.mutate)
			var conflict *hosts.RevisionConflictError
			if errors.As(err, &conflict) && p.autoRevision && p.args.Revision == "" && attempt < 3 {
				rev = ""
				continue
			}
			break
		}
	}
	if err != nil {
		return WriteResult{}, invalid(err)
	}
	res := WriteResult{Op: p.op, Kind: p.kind, Name: p.name, Changed: !applied.Unchanged, Revision: applied.RevisionAfter, Backup: applied.Backup}
	before, after, err := hosts.Diff(p.entity(applied.Before), p.entity(applied.After))
	if err != nil {
		return res, err
	}
	res.Before, res.After = before, after
	if p.dryRun {
		call.Note("dry run")
		return res, nil
	}
	if !res.Changed {
		call.Note("unchanged")
		return res, nil
	}
	ch := hosts.Change{Time: w.now().UTC(), Caller: callerLabel(gate.AuditPrincipal(ctx)), Op: p.op, Kind: p.kind, Name: p.name,
		Hosts: p.hosts(applied.Before, applied.After), Before: before, After: after,
		RevisionBefore: applied.RevisionBefore, RevisionAfter: applied.RevisionAfter, OperationID: call.Intent().OperationID}
	if err := w.history().Append(ch); err != nil {
		if w.Logger != nil {
			w.Logger.Error("host.history_write_failed", "op", p.op, "error", redact.Text(err.Error()))
		}
		res.Warnings = append(res.Warnings, "the change was written but could not be recorded in hosts.history.jsonl: "+redact.Text(err.Error()))
	}
	call.Note(fmt.Sprintf("%s %s changed=%d", p.kind, p.name, len(res.After)+len(res.Before)))
	return res, nil
}

// callerLabel is who changed hosts.yaml, as the history records it (main's).
func callerLabel(p audit.Principal) string {
	who := p.Subject
	if who == "" {
		who = p.Client
	}
	parts := []string{p.Surface}
	if p.Via != "" {
		parts = append(parts, p.Via)
	}
	label := strings.Join(parts, "/")
	if who != "" {
		label += " " + who
	}
	if p.OnBehalfOf != "" {
		label += " (for " + p.OnBehalfOf + ")"
	}
	return label
}

func hostEntity(name string) func(*hosts.File) any {
	return func(f *hosts.File) any {
		if i := f.HostIndex(name); i >= 0 {
			return f.Hosts[i]
		}
		return nil
	}
}

func componentEntity(id string) func(*hosts.File) any {
	return func(f *hosts.File) any {
		if i := f.ComponentIndex(id); i >= 0 {
			return f.Components[i]
		}
		return nil
	}
}

func hostOnly(name string) func(_, _ *hosts.File) []string {
	return func(_, _ *hosts.File) []string { return []string{name} }
}

func componentHosts(id string) func(before, after *hosts.File) []string {
	return func(before, after *hosts.File) []string {
		seen := map[string]bool{}
		var out []string
		for _, f := range []*hosts.File{before, after} {
			if i := f.ComponentIndex(id); i >= 0 {
				if h := f.Components[i].Location.Host; h != "" && !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
			}
		}
		return out
	}
}

// UpdateHost patches one host, creating it when asked.
func (w *Writer) UpdateHost(ctx context.Context, a HostUpdateArgs) (WriteResult, error) {
	if a.Name == "" {
		return WriteResult{}, invalid(errors.New("name is required"))
	}
	return w.write(ctx, writePlan{op: OpHostUpdate, kind: "host", name: a.Name, args: a.WriteArgs,
		config: map[string]any{"name": a.Name, "patch": a.Patch, "create": a.Create}, input: a.Patch,
		mutate: func(f *hosts.File) error {
			i := f.HostIndex(a.Name)
			if i < 0 {
				if !a.Create {
					return notFound("host", a.Name)
				}
				h, err := hosts.PatchHost(hosts.Host{Name: a.Name}, a.Patch)
				if err != nil {
					return err
				}
				f.Hosts = append(f.Hosts, h)
				return nil
			}
			h, err := hosts.PatchHost(f.Hosts[i], a.Patch)
			if err != nil {
				return err
			}
			f.Hosts[i] = h
			return nil
		},
		entity: hostEntity(a.Name), hosts: hostOnly(a.Name)})
}

// RemoveHost removes one host no component refers to.
func (w *Writer) RemoveHost(ctx context.Context, a HostRemoveArgs) (WriteResult, error) {
	return w.write(ctx, writePlan{op: OpHostRemove, kind: "host", name: a.Name, args: a.WriteArgs,
		config: map[string]any{"name": a.Name},
		mutate: func(f *hosts.File) error {
			if f.HostIndex(a.Name) < 0 {
				return notFound("host", a.Name)
			}
			return f.RemoveHost(a.Name)
		},
		entity: hostEntity(a.Name), hosts: hostOnly(a.Name)})
}

// CreateComponent adds one component.
func (w *Writer) CreateComponent(ctx context.Context, a ComponentCreateArgs) (WriteResult, error) {
	c, err := hosts.DecodeComponent(a.Component)
	if err != nil {
		return WriteResult{}, invalid(err)
	}
	return w.write(ctx, writePlan{op: OpComponentCreate, kind: "component", name: c.ID, args: a.WriteArgs,
		config: map[string]any{"id": c.ID, "component": a.Component}, input: a.Component,
		mutate: func(f *hosts.File) error { return f.AddComponent(c) },
		entity: componentEntity(c.ID), hosts: componentHosts(c.ID)})
}

// UpdateComponent patches one component's fields (not its location).
func (w *Writer) UpdateComponent(ctx context.Context, a ComponentUpdateArgs) (WriteResult, error) {
	return w.write(ctx, writePlan{op: OpComponentUpdate, kind: "component", name: a.ID, args: a.WriteArgs,
		config: map[string]any{"id": a.ID, "patch": a.Patch}, input: a.Patch,
		mutate: func(f *hosts.File) error {
			i := f.ComponentIndex(a.ID)
			if i < 0 {
				return notFound("component", a.ID)
			}
			c, err := hosts.PatchComponent(f.Components[i], a.Patch)
			if err != nil {
				return err
			}
			f.Components[i] = c
			return nil
		},
		entity: componentEntity(a.ID), hosts: componentHosts(a.ID)})
}

// MoveComponent moves one component to a slot on a host, or to the spares.
func (w *Writer) MoveComponent(ctx context.Context, a ComponentMoveArgs) (WriteResult, error) {
	return w.write(ctx, writePlan{op: OpComponentMove, kind: "component", name: a.ID, args: a.WriteArgs,
		config: map[string]any{"id": a.ID, "to": a.To},
		mutate: func(f *hosts.File) error {
			if f.ComponentIndex(a.ID) < 0 {
				return notFound("component", a.ID)
			}
			return f.MoveComponent(a.ID, a.To)
		},
		entity: componentEntity(a.ID), hosts: componentHosts(a.ID)})
}

// RemoveComponent deletes one component.
func (w *Writer) RemoveComponent(ctx context.Context, a ComponentRemoveArgs) (WriteResult, error) {
	return w.write(ctx, writePlan{op: OpComponentRemove, kind: "component", name: a.ID, args: a.WriteArgs,
		config: map[string]any{"id": a.ID},
		mutate: func(f *hosts.File) error {
			if f.ComponentIndex(a.ID) < 0 {
				return notFound("component", a.ID)
			}
			return f.RemoveComponent(a.ID)
		},
		entity: componentEntity(a.ID), hosts: componentHosts(a.ID)})
}

// History is the changes that touched a host or a component, newest first (main's
// readHistory; the caller records the read).
func (w *Writer) History(kind, name string, limit int) ([]hosts.Change, error) {
	out, err := w.history().Read(func(c hosts.Change) bool { return c.Touches(kind, name) }, limit)
	if out == nil {
		out = []hosts.Change{}
	}
	return out, err
}

// Inventory collects one host's hardware over ssh (`sudo -n`, main's InventoryCommand) and
// merges it into the host's record; a dry run shows what would change.
func (w *Writer) Inventory(ctx context.Context, a InventoryArgs) (InventoryResult, error) {
	f, err := w.Prober.Load()
	if err != nil {
		return InventoryResult{}, err
	}
	i := f.HostIndex(a.Name)
	if i < 0 {
		return InventoryResult{}, notFound("host", a.Name)
	}
	declared := f.Hosts[i]
	var collected hosts.Collected
	var warnings []string
	res, err := w.write(ctx, writePlan{op: OpHostInventory, kind: "host", name: a.Name, args: a.WriteArgs,
		config:       map[string]any{"name": a.Name, "dry_run": a.DryRun},
		autoRevision: true, dryRun: a.DryRun,
		prepare: func(ctx context.Context, call *gate.Call) error {
			c, cerr := w.collect(call.WithTelemetry(ctx), declared)
			collected = c
			return cerr
		},
		mutate: func(f *hosts.File) error {
			_, ws, merr := hosts.MergeInventory(f, a.Name, collected, w.now())
			warnings = ws
			return merr
		},
		entity: hostEntity(a.Name), hosts: hostOnly(a.Name)})
	res.Warnings = append(warnings, res.Warnings...)
	return InventoryResult{WriteResult: res, DryRun: a.DryRun}, err
}

func (w *Writer) collect(ctx context.Context, h hosts.Host) (hosts.Collected, error) {
	if h.Reach.SSH == "" {
		return hosts.Collected{}, redact.Guidance("host %q declares no reach.ssh, so it cannot be inventoried; the inventory needs `sudo -n` over ssh (there is no privileged Outpost operation for it yet). Set reach.ssh to a server/ssh resource id in hosts.yaml", h.Name)
	}
	def, err := w.Prober.reach(h)
	if err != nil {
		return hosts.Collected{}, err
	}
	if w.Prober.o.SSH == nil {
		return hosts.Collected{}, redact.Guidance("host %q cannot be inventoried: the ssh connector is not available", h.Name)
	}
	ctx, scope := redact.EnsureScope(ctx)
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := w.Prober.execFixed(pctx, def, hosts.InventoryCommand)
	if err != nil {
		return hosts.Collected{}, redact.Guidance("%s", hostReason(scope, "the host did not answer the inventory over ssh", err))
	}
	return hosts.ParseInventory(out)
}
