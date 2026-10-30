package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Write operation names (Hosts C). Writes change hosts.yaml and are effect
// write: audited with intent and outcome, refused when the record cannot be
// written, and gated like every other write.
const (
	OpHostUpdate       = "host_update"
	OpHostRemove       = "host_remove"
	OpComponentCreate  = "component_create"
	OpComponentUpdate  = "component_update"
	OpComponentMove    = "component_move"
	OpComponentRemove  = "component_remove"
	OpHostRevision     = "host_revision"
	OpHostHistory      = "host_history"
	OpComponentHistory = "component_history"
)

// WriteArgs are what every write carries: the revision of hosts.yaml the
// caller read, and the gate fields.
type WriteArgs struct {
	// Revision is the token from the read this change is based on. Required.
	Revision     string `json:"revision"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
	ApprovalID   string `json:"approval_id,omitempty"`
}

// HostUpdateArgs changes one host with a JSON merge patch (objects merge, null
// deletes a field, lists replace). Create adds the host when it is absent.
type HostUpdateArgs struct {
	WriteArgs
	Name   string         `json:"name"`
	Patch  map[string]any `json:"patch"`
	Create bool           `json:"create,omitempty"`
}

// HostRemoveArgs removes a host nothing references.
type HostRemoveArgs struct {
	WriteArgs
	Name string `json:"name"`
}

// ComponentCreateArgs adds a component.
type ComponentCreateArgs struct {
	WriteArgs
	Component map[string]any `json:"component"`
}

// ComponentUpdateArgs changes a component with a merge patch (not its
// location).
type ComponentUpdateArgs struct {
	WriteArgs
	ID    string         `json:"id"`
	Patch map[string]any `json:"patch"`
}

// ComponentMoveArgs moves a component to a slot on a host, or to the spares.
type ComponentMoveArgs struct {
	WriteArgs
	ID string                  `json:"id"`
	To hosts.ComponentLocation `json:"to"`
}

// ComponentRemoveArgs deletes a component.
type ComponentRemoveArgs struct {
	WriteArgs
	ID string `json:"id"`
}

// WriteResult is what a write did: the changed fields only, and the file's new
// revision for the next write.
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

// RevisionConflict is returned when hosts.yaml changed since the caller read it.
type RevisionConflict = hosts.RevisionConflictError

func (s *HostService) editor() *hosts.Editor { return &hosts.Editor{Path: s.hostsPath, Now: s.now} }

func (s *HostService) history() hosts.History { return hosts.HistoryFor(s.hostsPath) }

// Revision is the current revision of hosts.yaml: the token a write carries.
func (s *HostService) Revision(context.Context) (string, error) {
	if s == nil {
		return "", errors.New("host service is not configured")
	}
	return s.editor().Revision()
}

// invalid wraps an error from a change the caller can fix.
func invalid(err error) error {
	var nf *HostNotFoundError
	var cn *ComponentNotFoundError
	var rc *hosts.RevisionConflictError
	var rr hosts.RevisionRequiredError
	switch {
	case errors.As(err, &nf), errors.As(err, &cn), errors.As(err, &rc), errors.As(err, &rr):
		return err
	}
	var inv *HostInvalidError
	if errors.As(err, &inv) {
		return err
	}
	return &HostInvalidError{Err: redact.Guidance("%s", err.Error())}
}

type writePlan struct {
	op, kind, name string
	args           WriteArgs
	config         map[string]any
	// input is every caller-supplied value, scanned for credential material.
	input  any
	mutate func(*hosts.File) error
	// entity returns the record before and after, for the change history.
	entity func(*hosts.File) any
	hosts  func(before, after *hosts.File) []string
}

func (s *HostService) write(ctx context.Context, p writePlan) (_ WriteResult, retErr error) {
	if s == nil {
		return WriteResult{}, errors.New("host service is not configured")
	}
	if p.args.Revision == "" {
		return WriteResult{}, invalid(hosts.RevisionRequiredError{})
	}
	if where := hosts.FindSecret("", p.input); where != "" {
		return WriteResult{}, invalid(fmt.Errorf("%s looks like credential material, which hosts.yaml never holds; nothing was written", where))
	}
	def, _ := HostDefinition().Operation(p.op)
	spec := auditSpec{connector: "host", operation: p.op, op: def, known: true, config: p.config,
		acknowledged: p.args.Acknowledged, approvalID: p.args.ApprovalID, resources: s.resources}
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { call.finish(retErr) }()
	if err = requireAcknowledgment(ExternalConnectorOperationArgs{Connector: "host", Operation: p.op, Acknowledged: p.args.Acknowledged}, spec.op); err != nil {
		return WriteResult{}, err
	}

	applied, err := s.editor().Apply(p.args.Revision, p.mutate)
	if err != nil {
		return WriteResult{}, invalid(err)
	}
	res := WriteResult{Op: p.op, Kind: p.kind, Name: p.name, Changed: !applied.Unchanged, Revision: applied.RevisionAfter, Backup: applied.Backup}
	before, after, err := hosts.Diff(p.entity(applied.Before), p.entity(applied.After))
	if err != nil {
		return res, err
	}
	res.Before, res.After = before, after
	if !res.Changed {
		call.note("unchanged")
		return res, nil
	}
	ch := hosts.Change{Time: s.now().UTC(), Caller: callerLabel(principalFor(ctx, spec)), Op: p.op, Kind: p.kind, Name: p.name,
		Hosts: p.hosts(applied.Before, applied.After), Before: before, After: after,
		RevisionBefore: applied.RevisionBefore, RevisionAfter: applied.RevisionAfter, OperationID: call.intent.OperationID}
	if err := s.history().Append(ch); err != nil {
		// The file is already changed: say so rather than undo it.
		s.logger.Error("host.history_write_failed", "op", p.op, "error", redact.Text(err.Error()))
		res.Warnings = append(res.Warnings, "the change was written but could not be recorded in hosts.history.jsonl: "+redact.Text(err.Error()))
	}
	call.note(fmt.Sprintf("%s %s changed=%d", p.kind, p.name, len(res.After)+len(res.Before)))
	return res, nil
}

// callerLabel names who asked, for the history: names and ids only.
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

// componentHosts are the hosts a component sat on before and sits on after.
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

// UpdateHost changes one host.
func (s *HostService) UpdateHost(ctx context.Context, a HostUpdateArgs) (WriteResult, error) {
	if a.Name == "" {
		return WriteResult{}, invalid(errors.New("name is required"))
	}
	return s.write(ctx, writePlan{op: OpHostUpdate, kind: "host", name: a.Name, args: a.WriteArgs,
		config: map[string]any{"name": a.Name, "patch": a.Patch, "create": a.Create}, input: a.Patch,
		mutate: func(f *hosts.File) error {
			i := f.HostIndex(a.Name)
			if i < 0 {
				if !a.Create {
					return &HostNotFoundError{Name: a.Name}
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

// RemoveHost deletes a host that no component refers to.
func (s *HostService) RemoveHost(ctx context.Context, a HostRemoveArgs) (WriteResult, error) {
	return s.write(ctx, writePlan{op: OpHostRemove, kind: "host", name: a.Name, args: a.WriteArgs,
		config: map[string]any{"name": a.Name},
		mutate: func(f *hosts.File) error {
			if f.HostIndex(a.Name) < 0 {
				return &HostNotFoundError{Name: a.Name}
			}
			return f.RemoveHost(a.Name)
		},
		entity: hostEntity(a.Name), hosts: hostOnly(a.Name)})
}

// CreateComponent adds a component.
func (s *HostService) CreateComponent(ctx context.Context, a ComponentCreateArgs) (WriteResult, error) {
	c, err := hosts.DecodeComponent(a.Component)
	if err != nil {
		return WriteResult{}, invalid(err)
	}
	return s.write(ctx, writePlan{op: OpComponentCreate, kind: "component", name: c.ID, args: a.WriteArgs,
		config: map[string]any{"id": c.ID, "component": a.Component}, input: a.Component,
		mutate: func(f *hosts.File) error { return f.AddComponent(c) },
		entity: componentEntity(c.ID), hosts: componentHosts(c.ID)})
}

// UpdateComponent changes a component's fields.
func (s *HostService) UpdateComponent(ctx context.Context, a ComponentUpdateArgs) (WriteResult, error) {
	return s.write(ctx, writePlan{op: OpComponentUpdate, kind: "component", name: a.ID, args: a.WriteArgs,
		config: map[string]any{"id": a.ID, "patch": a.Patch}, input: a.Patch,
		mutate: func(f *hosts.File) error {
			i := f.ComponentIndex(a.ID)
			if i < 0 {
				return &ComponentNotFoundError{ID: a.ID}
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

// MoveComponent moves a component to a slot on a host, or to the spares.
func (s *HostService) MoveComponent(ctx context.Context, a ComponentMoveArgs) (WriteResult, error) {
	return s.write(ctx, writePlan{op: OpComponentMove, kind: "component", name: a.ID, args: a.WriteArgs,
		config: map[string]any{"id": a.ID, "to": a.To},
		mutate: func(f *hosts.File) error {
			if f.ComponentIndex(a.ID) < 0 {
				return &ComponentNotFoundError{ID: a.ID}
			}
			return f.MoveComponent(a.ID, a.To)
		},
		entity: componentEntity(a.ID), hosts: componentHosts(a.ID)})
}

// RemoveComponent deletes a component.
func (s *HostService) RemoveComponent(ctx context.Context, a ComponentRemoveArgs) (WriteResult, error) {
	return s.write(ctx, writePlan{op: OpComponentRemove, kind: "component", name: a.ID, args: a.WriteArgs,
		config: map[string]any{"id": a.ID},
		mutate: func(f *hosts.File) error {
			if f.ComponentIndex(a.ID) < 0 {
				return &ComponentNotFoundError{ID: a.ID}
			}
			return f.RemoveComponent(a.ID)
		},
		entity: componentEntity(a.ID), hosts: componentHosts(a.ID)})
}

// HostHistory returns the changes that touched a host, newest first.
func (s *HostService) HostHistory(ctx context.Context, name string, limit int) (_ []hosts.Change, retErr error) {
	return s.readHistory(ctx, OpHostHistory, "host", name, limit)
}

// ComponentHistory returns the changes to one component, newest first.
func (s *HostService) ComponentHistory(ctx context.Context, id string, limit int) (_ []hosts.Change, retErr error) {
	return s.readHistory(ctx, OpComponentHistory, "component", id, limit)
}

func (s *HostService) readHistory(ctx context.Context, op, kind, name string, limit int) (_ []hosts.Change, retErr error) {
	if s == nil {
		return nil, errors.New("host service is not configured")
	}
	call, err := s.beginRead(ctx, op, map[string]any{"name": name, "limit": limit})
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()
	out, err := s.history().Read(func(c hosts.Change) bool { return c.Touches(kind, name) }, limit)
	if out == nil {
		out = []hosts.Change{}
	}
	call.note(fmt.Sprintf("changes=%d", len(out)))
	return out, err
}
