package cerbapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/egress"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Egress policy on a result (P4-4, section 7). After an operation runs, its
// labeled parts (P4-1, P4-3) are matched against the policy's egress rules
// for this principal and target. Every decision other than pass is recorded
// on the outcome; a rule in shadow changes nothing, and an enforced one caps,
// masks or refuses, and says what it withheld. The default is pass.

// egressFields are the labeled places in value: its Go type's tags, a plugin
// operation's output schema, or the whole value when the operation returns
// free text and the value is a bare string.
func egressFields(spec auditSpec, value any) []egress.Field {
	if value == nil {
		return nil
	}
	if _, isString := value.(string); isString && spec.known && spec.op.Output == contract.OutputFreeText {
		return []egress.Field{{Pointer: "", Labels: []egress.Label{egress.Untrusted}}}
	}
	if spec.known && spec.op.OutputSchema != nil {
		if fields, err := egress.FieldsFromSchema(spec.op.OutputSchema); err == nil {
			return fields
		}
	}
	// A plugin result with no output schema arrives as decoded JSON: it is
	// unlabeled, and wholly untrusted (P4-3).
	switch value.(type) {
	case map[string]any, []any:
		fields, _ := egress.FieldsFromSchema(nil)
		return fields
	}
	fields, err := egress.Fields(reflect.TypeOf(value))
	if err != nil {
		return nil
	}
	return fields
}

// applyEgress is egress policy on value, the result of c's operation.
func (c *auditCall) applyEgress(value any) (any, error) {
	if c == nil {
		return value, nil
	}
	ev, ok := PolicyDecisionPoint().(*policy.Evaluator)
	if !ok || len(ev.File().Egress) == 0 {
		return value, nil
	}
	fields := egressFields(c.spec, value)
	if len(fields) == 0 {
		return value, nil
	}
	byLabel := map[string][]string{}
	var order []string
	for _, f := range fields {
		for _, l := range f.Labels {
			if _, seen := byLabel[string(l)]; !seen {
				order = append(order, string(l))
			}
			byLabel[string(l)] = append(byLabel[string(l)], f.Pointer)
		}
	}
	var doc any
	transformed := false
	for _, label := range order {
		d := ev.File().EgressFor(c.spec.connector, c.target, c.intent.Principal.Kind, label)
		if d.Action == policy.EgressPass {
			continue
		}
		action := audit.EgressAction{Rule: d.Rule, Label: label, Action: d.Action, Mode: d.Mode, Pointers: byLabel[label], Applied: d.Enforced()}
		if doc == nil {
			doc = toJSONValue(value)
		}
		if d.Action == policy.EgressRefuse {
			if d.Enforced() {
				c.egress = append(c.egress, action)
				return nil, egressRefusedError(c.spec, d)
			}
			c.egress = append(c.egress, action)
			continue
		}
		// Measure what would be withheld on a copy; apply it only when
		// enforced.
		target := doc
		if !d.Enforced() {
			target = toJSONValue(doc)
		}
		out, withheld := shapeAll(target, byLabel[label], d)
		action.Withheld = withheld
		c.egress = append(c.egress, action)
		if d.Enforced() {
			doc, transformed = out, true
		}
	}
	if transformed {
		return doc, nil
	}
	return value, nil
}

func egressRefusedError(spec auditSpec, d policy.EgressDecision) error {
	args := ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}
	return externalConnectorError(args, ExternalConnectorEgressRefused,
		redact.Guidance("%s %s ran, but egress rule %s withholds its %s output from you; ask your operator to read it, or to change the rule", spec.connector, spec.operation, d.Rule, d.Label))
}

// toJSONValue is value as decoded JSON, the shape results travel in.
func toJSONValue(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if json.Unmarshal(data, &out) != nil {
		return value
	}
	return out
}

// shapeAll applies a cap or mask at every pointer, returning the shaped
// document and how much it withheld.
func shapeAll(doc any, pointers []string, d policy.EgressDecision) (any, int) {
	total := 0
	for _, p := range pointers {
		var n int
		doc, n = shapeAt(doc, splitPointer(p), d)
		total += n
	}
	return doc, total
}

func splitPointer(p string) []string {
	if p == "" {
		return nil
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		segs[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
	}
	return segs
}

// shapeAt walks to the pointer and shapes what is there. A final "*" over a
// list of strings is the list itself: a cap keeps its first lines, a mask
// masks each.
func shapeAt(v any, segs []string, d policy.EgressDecision) (any, int) {
	if len(segs) == 0 {
		return shapeText(v, d)
	}
	seg, rest := segs[0], segs[1:]
	switch x := v.(type) {
	case map[string]any:
		if seg == "*" {
			total := 0
			for k, e := range x {
				var n int
				x[k], n = shapeAt(e, rest, d)
				total += n
			}
			return x, total
		}
		e, ok := x[seg]
		if !ok {
			return x, 0
		}
		var n int
		x[seg], n = shapeAt(e, rest, d)
		return x, n
	case []any:
		if seg != "*" {
			return x, 0
		}
		if len(rest) == 0 && d.Action == policy.EgressCap {
			return capList(x, d)
		}
		total := 0
		for i, e := range x {
			var n int
			x[i], n = shapeAt(e, rest, d)
			total += n
		}
		return x, total
	}
	return v, 0
}

// shapeText caps or masks one text value.
func shapeText(v any, d policy.EgressDecision) (any, int) {
	s, ok := v.(string)
	if !ok {
		if list, isList := v.([]any); isList && d.Action == policy.EgressCap {
			return capList(list, d)
		}
		return v, 0
	}
	switch d.Action {
	case policy.EgressMask:
		return fmt.Sprintf("[cerberus: %d characters of %s text masked by egress rule %s]", len(s), d.Label, d.Rule), len(s)
	case policy.EgressCap:
		lines := strings.Split(s, "\n")
		if len(lines) <= d.Lines {
			return s, 0
		}
		withheld := len(lines) - d.Lines
		return strings.Join(lines[:d.Lines], "\n") + fmt.Sprintf("\n[cerberus: %d more lines withheld by egress rule %s]", withheld, d.Rule), withheld
	}
	return s, 0
}

// capList keeps a list's first lines and says how many it held back.
func capList(list []any, d policy.EgressDecision) (any, int) {
	if len(list) <= d.Lines {
		return list, 0
	}
	withheld := len(list) - d.Lines
	out := append(append([]any{}, list[:d.Lines]...), fmt.Sprintf("[cerberus: %d more entries withheld by egress rule %s]", withheld, d.Rule))
	return out, withheld
}
