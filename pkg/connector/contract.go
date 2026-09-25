package connector

import (
	"fmt"
	"sort"
	"strings"
)

// The operation contract: what an operation does, what it touches, and what
// it accepts. Policy, the ack gate, MCP annotations and the audit log read
// these fields, so they are declared once, on the operation, and everything
// else is derived. See docs/plans/live-systems-security-target.md, section 1.

// Effect is an operation's effect class. Default policy keys off it, not off
// the operation's name.
type Effect string

const (
	// EffectRead is inventory and status, with no free text of unknown origin.
	EffectRead Effect = "read"
	// EffectReadSensitive returns text Cerberus did not compose, or content
	// that can carry secrets or personal data: logs, file contents, scripts.
	EffectReadSensitive Effect = "read_sensitive"
	// EffectWrite creates or changes configuration or data.
	EffectWrite Effect = "write"
	// EffectLifecycle starts, stops, restarts, scales or deploys.
	EffectLifecycle Effect = "lifecycle"
	// EffectDestructive removes something or cannot be undone.
	EffectDestructive Effect = "destructive"
	// EffectExec runs caller-supplied code or commands.
	EffectExec Effect = "exec"
	// EffectAdmin changes Cerberus itself.
	EffectAdmin Effect = "admin"
)

// Effects is the whole vocabulary, in increasing order of reach.
var Effects = []Effect{EffectRead, EffectReadSensitive, EffectWrite, EffectLifecycle, EffectDestructive, EffectExec, EffectAdmin}

// Valid reports whether e is one of Effects. The empty effect is not valid.
func (e Effect) Valid() bool {
	for _, known := range Effects {
		if e == known {
			return true
		}
	}
	return false
}

// ReadOnly reports whether the effect only reads.
func (e Effect) ReadOnly() bool {
	return e == EffectRead || e == EffectReadSensitive
}

// RequiresAck reports whether an operation of this effect needs operator
// acknowledgment: everything except the two read classes (Decision 14). An
// unknown or missing effect needs it, so a gap fails closed.
func (e Effect) RequiresAck() bool {
	return !e.ReadOnly()
}

// PreviewKind is where an operation's dry-run preview comes from.
type PreviewKind string

const (
	// PreviewServer: the provider evaluates the change, e.g. k8s dryRun=All.
	PreviewServer PreviewKind = "server"
	// PreviewHost: Cerberus computes the preview itself.
	PreviewHost PreviewKind = "host"
	// PreviewPlugin: a plugin claims a preview the host cannot verify.
	PreviewPlugin PreviewKind = "plugin"
	// PreviewNone: no preview; a dry run is refused as preview_unsupported.
	PreviewNone PreviewKind = "none"
)

var previewKinds = []PreviewKind{PreviewServer, PreviewHost, PreviewPlugin, PreviewNone}

// OutputKind is the shape of what an operation returns.
type OutputKind string

const (
	// OutputStructured is a Cerberus DTO.
	OutputStructured OutputKind = "structured"
	// OutputFreeText carries text Cerberus did not compose.
	OutputFreeText OutputKind = "free_text"
	// OutputFile writes a file rather than returning its content.
	OutputFile OutputKind = "file"
)

var outputKinds = []OutputKind{OutputStructured, OutputFreeText, OutputFile}

// Cost is whether an operation can incur a provider charge.
type Cost string

const (
	CostNone     Cost = "none"
	CostBillable Cost = "billable"
)

var costs = []Cost{CostNone, CostBillable}

// LocalFS is an operation's access to the filesystem of the machine running
// Cerberus: ssh put reads a local file, ssh get writes one.
type LocalFS string

const (
	LocalFSNone   LocalFS = "none"
	LocalFSReads  LocalFS = "reads"
	LocalFSWrites LocalFS = "writes"
)

var localFSKinds = []LocalFS{LocalFSNone, LocalFSReads, LocalFSWrites}

// TargetDescriptor names what an operation acts on: a kind, and the input
// fields that identify the instance. An operation over a whole account, such
// as a list, has a kind and no fields.
type TargetDescriptor struct {
	Kind string   `json:"kind" yaml:"kind"`
	From []string `json:"from,omitempty" yaml:"from,omitempty"`
}

// InputScope is which callers may supply an input.
type InputScope string

const (
	// InputCaller may come from any surface: CLI, socket, web or MCP.
	InputCaller InputScope = "caller"
	// InputLocal comes only from the operator's own shell, in-process. The
	// socket, the web console and MCP are refused it by name. It is the
	// scope for ad-hoc targets: `cerberus docker --host`.
	InputLocal InputScope = "local"
)

// Input is one row of an operation's key table: a config key the operation
// accepts, its JSON schema, whether it is required, and who may send it. A key
// that is not in the table is refused on every surface.
type Input struct {
	Name     string         `json:"name" yaml:"name"`
	Schema   map[string]any `json:"schema,omitempty" yaml:"schema,omitempty"`
	Required bool           `json:"required,omitempty" yaml:"required,omitempty"`
	Scope    InputScope     `json:"scope,omitempty" yaml:"scope,omitempty"`
}

// Field is an optional input any caller may send.
func Field(name string, schema map[string]any) Input {
	return Input{Name: name, Schema: schema, Scope: InputCaller}
}

// RequiredField is a required input any caller may send.
func RequiredField(name string, schema map[string]any) Input {
	return Input{Name: name, Schema: schema, Required: true, Scope: InputCaller}
}

// LocalOnly returns in with the local scope: accepted from the operator's own
// shell and refused from every other surface.
func (in Input) LocalOnly() Input {
	in.Scope = InputLocal
	return in
}

func (in Input) scope() InputScope {
	if in.Scope == "" {
		return InputCaller
	}
	return in.Scope
}

// Finalize derives an operation's compatibility and discovery fields from its
// contract, so nothing downstream can drift from it:
//
//   - InputSchema is built from the caller-scope Inputs. Local inputs are
//     not advertised, because only the operator's shell may send them.
//   - Destructive is Effect == destructive.
//   - RequiresAck follows Effect (Decision 14).
//   - SupportsDry is Preview != none.
//
// An operation with no Inputs but an InputSchema — a plugin manifest — gets
// its Inputs from the schema instead. Finalize is idempotent.
func (op Operation) Finalize() Operation {
	switch {
	case op.schemaIsSource:
		// Already derived from a plugin's schema; leave the schema as written.
	case len(op.Inputs) == 0 && op.InputSchema != nil:
		op.Inputs, op.InputsOpen = InputsFromSchema(op.InputSchema)
		op.schemaIsSource = true
	default:
		op.InputSchema = schemaFromInputs(op.Inputs)
		op.InputsOpen = false
	}
	if op.Effect != "" {
		op.Destructive = op.Effect == EffectDestructive
	}
	op.RequiresAck = op.Effect.RequiresAck()
	if op.Preview != "" {
		op.SupportsDry = op.Preview != PreviewNone
	}
	return op
}

// Finalize finalizes every operation of def. Each built-in connector's
// Definition() returns its definition finalized.
func Finalize(def Definition) Definition {
	ops := make([]Operation, len(def.Operations))
	for i, op := range def.Operations {
		ops[i] = op.Finalize()
	}
	def.Operations = ops
	return def
}

func schemaFromInputs(inputs []Input) map[string]any {
	props := map[string]any{}
	var required []string
	for _, in := range inputs {
		if in.scope() != InputCaller {
			continue
		}
		props[in.Name] = cloneSchema(in.Schema)
		if in.Required {
			required = append(required, in.Name)
		}
	}
	return ObjectSchema(props, required...)
}

// InputsFromSchema reads a key table out of a JSON object schema: one caller
// input per property, required as the schema says. open reports that the
// schema accepts properties it does not name — additionalProperties is not
// false — so undeclared keys cannot be refused.
func InputsFromSchema(schema map[string]any) (inputs []Input, open bool) {
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	switch req := schema["required"].(type) {
	case []string:
		for _, name := range req {
			required[name] = true
		}
	case []any:
		for _, name := range req {
			if text, ok := name.(string); ok {
				required[text] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		propSchema, _ := props[name].(map[string]any)
		inputs = append(inputs, Input{Name: name, Schema: cloneSchema(propSchema), Required: required[name], Scope: InputCaller})
	}
	additional, isBool := schema["additionalProperties"].(bool)
	return inputs, !isBool || additional
}

// Validate checks that an operation declares a complete, consistent contract.
// A built-in operation must pass it; a plugin operation missing an effect is
// a gap, reported rather than refused (see Manifest.ContractGaps).
func (op Operation) Validate() error {
	var problems []string
	if !op.Effect.Valid() {
		problems = append(problems, fmt.Sprintf("effect %q is not one of %s", op.Effect, joinKinds(Effects)))
	}
	if !contains(previewKinds, op.Preview) {
		problems = append(problems, fmt.Sprintf("preview %q is not one of %s", op.Preview, joinKinds(previewKinds)))
	}
	if !contains(outputKinds, op.Output) {
		problems = append(problems, fmt.Sprintf("output %q is not one of %s", op.Output, joinKinds(outputKinds)))
	}
	if !contains(costs, op.Cost) {
		problems = append(problems, fmt.Sprintf("cost %q is not one of %s", op.Cost, joinKinds(costs)))
	}
	if !contains(localFSKinds, op.LocalFS) {
		problems = append(problems, fmt.Sprintf("local_fs %q is not one of %s", op.LocalFS, joinKinds(localFSKinds)))
	}
	if op.Target.Kind == "" {
		problems = append(problems, "target.kind is required")
	}
	declared := map[string]bool{}
	for _, in := range op.Inputs {
		if in.Name == "" {
			problems = append(problems, "an input has no name")
			continue
		}
		if declared[in.Name] {
			problems = append(problems, fmt.Sprintf("input %q is declared twice", in.Name))
		}
		declared[in.Name] = true
		if in.scope() != InputCaller && in.scope() != InputLocal {
			problems = append(problems, fmt.Sprintf("input %q scope %q is not caller or local", in.Name, in.Scope))
		}
		if in.Required && in.scope() == InputLocal {
			problems = append(problems, fmt.Sprintf("input %q is required but local-only, so no remote caller could run the operation", in.Name))
		}
	}
	for _, field := range op.Target.From {
		if !declared[field] {
			problems = append(problems, fmt.Sprintf("target field %q is not a declared input", field))
		}
	}
	for _, group := range op.OneOf {
		for _, field := range group {
			if !declared[field] {
				problems = append(problems, fmt.Sprintf("one_of field %q is not a declared input", field))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("operation %q: %s", op.Name, strings.Join(problems, "; "))
	}
	return nil
}

// ValidateDefinition validates every operation of a built-in definition.
func ValidateDefinition(def Definition) error {
	var problems []string
	for _, op := range def.Operations {
		if err := op.Validate(); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("connector %q contract incomplete: %s", def.ID, strings.Join(problems, "; "))
	}
	return nil
}

// InputError is a caller config the key table refuses. Each list names keys,
// never values.
type InputError struct {
	// Undeclared keys are not in the operation's key table.
	Undeclared []string
	// LocalOnly keys are declared, but only the operator's own shell may send
	// them, and this caller is not it.
	LocalOnly []string
	// Missing required keys.
	Missing []string
	// MissingOneOf groups had none of their keys present.
	MissingOneOf [][]string
}

// Error names the refused keys in parentheses, never directly before a colon
// or an equals sign: a key called token or password followed by ": the ..."
// reads to redact.Text as an assignment, and it would eat the next word.
func (e *InputError) Error() string {
	var parts []string
	if len(e.Undeclared) > 0 {
		parts = append(parts, "refusing fields ("+strings.Join(e.Undeclared, ", ")+"): the operation does not declare them")
	}
	if len(e.LocalOnly) > 0 {
		parts = append(parts, "refusing fields ("+strings.Join(e.LocalOnly, ", ")+"): they are accepted only from your own shell, in-process, never over the socket, the web console or MCP")
	}
	if len(e.Missing) > 0 {
		parts = append(parts, "missing required fields ("+strings.Join(e.Missing, ", ")+")")
	}
	for _, group := range e.MissingOneOf {
		parts = append(parts, "one of ("+strings.Join(group, ", ")+") is required")
	}
	return strings.Join(parts, "; ")
}

// CheckInputs checks a caller's config against the operation's key table. It
// runs before anything is resolved, so a refusal never depends on having a
// credential. local is true only for the operator's own shell, in-process.
// It returns nil or an *InputError.
func (op Operation) CheckInputs(config map[string]any, local bool) error {
	declared := make(map[string]Input, len(op.Inputs))
	for _, in := range op.Inputs {
		declared[in.Name] = in
	}
	var ierr InputError
	for key := range config {
		in, ok := declared[key]
		switch {
		case !ok:
			if !op.InputsOpen {
				ierr.Undeclared = append(ierr.Undeclared, key)
			}
		case in.scope() == InputLocal && !local:
			ierr.LocalOnly = append(ierr.LocalOnly, key)
		}
	}
	for _, in := range op.Inputs {
		if in.Required && !present(config, in.Name) {
			ierr.Missing = append(ierr.Missing, in.Name)
		}
	}
	for _, group := range op.OneOf {
		found := false
		for _, key := range group {
			if present(config, key) {
				found = true
				break
			}
		}
		if !found {
			ierr.MissingOneOf = append(ierr.MissingOneOf, group)
		}
	}
	if len(ierr.Undeclared)+len(ierr.LocalOnly)+len(ierr.Missing)+len(ierr.MissingOneOf) == 0 {
		return nil
	}
	sort.Strings(ierr.Undeclared)
	sort.Strings(ierr.LocalOnly)
	return &ierr
}

// present reports whether key carries a value: an empty string, a nil or an
// empty list does not count as supplying a required field.
func present(config map[string]any, key string) bool {
	value, ok := config[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	}
	return true
}

func contains[T comparable](list []T, v T) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func joinKinds[T ~string](list []T) string {
	out := make([]string, len(list))
	for i, item := range list {
		out[i] = string(item)
	}
	return strings.Join(out, ", ")
}
