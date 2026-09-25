// Package conformance checks that an operation's contract is complete and
// that everything derived from it agrees: the ack requirement, the MCP
// hints, the preview flag and the advertised input schema. Every built-in
// connector, the runtime's own operations and every plugin manifest are held
// to it, and a plugin repository can run it against its own manifests.
package conformance

import (
	"fmt"
	"sort"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Operation returns every way op's contract is incomplete or inconsistent.
// It checks the finalized operation, so a derived field is checked against
// what the contract derives, not against what someone wrote by hand.
func Operation(op contract.Operation) []string {
	op = op.Finalize()
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf("operation %q: ", op.Name)+fmt.Sprintf(format, args...))
	}

	if !op.Effect.Valid() {
		add("effect %q is missing or unknown", op.Effect)
	}
	// Acknowledgment: Decision 14, plus local_fs writes.
	wantAck := !op.Effect.ReadOnly() || op.LocalFS == contract.LocalFSWrites
	if op.RequiresAck != wantAck {
		add("requires_ack is %v, but effect %s with local_fs %q means %v", op.RequiresAck, op.Effect, op.LocalFS, wantAck)
	}
	if op.Destructive != (op.Effect == contract.EffectDestructive) {
		add("destructive is %v for effect %s", op.Destructive, op.Effect)
	}
	// Previews.
	wantDry := op.Preview != "" && op.Preview != contract.PreviewNone
	if op.SupportsDry != wantDry {
		add("supports_dry is %v, but preview %q means %v", op.SupportsDry, op.Preview, wantDry)
	}
	// Hints agree with the gate.
	h := contract.HintsFor(op)
	if h.ReadOnly == op.RequiresAck || h.Destructive != op.RequiresAck {
		add("hints read_only=%v destructive=%v disagree with requires_ack=%v", h.ReadOnly, h.Destructive, op.RequiresAck)
	}
	if h.Idempotent && !h.ReadOnly {
		add("an operation that is not read-only claims idempotence")
	}
	// The advertised schema is the key table's caller inputs, exactly.
	problems = append(problems, schemaProblems(op)...)
	return problems
}

func schemaProblems(op contract.Operation) []string {
	advertised, open := contract.InputsFromSchema(op.InputSchema)
	if open && !op.InputsOpen {
		return []string{fmt.Sprintf("operation %q: input_schema accepts undeclared keys, but its key table refuses them", op.Name)}
	}
	want := map[string]bool{}
	for _, in := range op.Inputs {
		if in.Scope == contract.InputLocal {
			continue
		}
		want[in.Name] = in.Required
	}
	got := map[string]bool{}
	for _, in := range advertised {
		got[in.Name] = in.Required
	}
	var problems []string
	for _, name := range sortedKeys(want) {
		required, ok := got[name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("operation %q: input %q is accepted but not in input_schema", op.Name, name))
		case required != want[name]:
			problems = append(problems, fmt.Sprintf("operation %q: input %q required=%v in the key table, %v in input_schema", op.Name, name, want[name], required))
		}
	}
	for _, name := range sortedKeys(got) {
		if _, ok := want[name]; !ok {
			problems = append(problems, fmt.Sprintf("operation %q: input_schema advertises %q, which the key table does not accept from callers", op.Name, name))
		}
	}
	return problems
}

// Definition checks every operation of a built-in definition, including that
// its contract is complete (ValidateDefinition).
func Definition(def contract.Definition) []string {
	var problems []string
	if err := contract.ValidateDefinition(def); err != nil {
		problems = append(problems, err.Error())
	}
	for _, op := range def.Operations {
		for _, p := range Operation(op) {
			problems = append(problems, def.ID+": "+p)
		}
	}
	return problems
}

// Manifest checks a plugin manifest: it validates, it has no contract gaps,
// and each operation's effective contract passes Operation.
func Manifest(m contract.Manifest) []string {
	var problems []string
	if err := m.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	for _, gap := range m.ContractGaps() {
		problems = append(problems, m.ID+": "+gap)
	}
	for _, op := range m.Operations {
		for _, p := range Operation(op.Operation()) {
			problems = append(problems, m.ID+": "+p)
		}
	}
	return problems
}

// Report joins problems for a test failure.
func Report(problems []string) string {
	return strings.Join(problems, "\n  ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
