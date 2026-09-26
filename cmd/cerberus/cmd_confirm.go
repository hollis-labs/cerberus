package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/plan"
)

// tty_confirm on the call (P3-3): when policy wants a person to confirm an
// operation on their own terminal, the CLI shows the plan, asks for the
// target to be typed, and sends the call again on its confirm route with
// the hash of the plan it showed. Anything but an interactive terminal gets
// the refusal as it came, with the command that decides it.

// confirmIsTerminal is whether stdin and stdout are a terminal. Tests swap
// it.
var confirmIsTerminal = policyIsTerminal

// confirmable is the approval ref of a refusal the caller can meet by
// confirming on this terminal.
func confirmable(err error) (*cerbapi.ApprovalRef, bool) {
	var coded *cerbapi.ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil || coded.Approval.Channel != approval.ChannelTTYConfirm {
		return nil, false
	}
	if coded.Code != cerbapi.ExternalConnectorApprovalPending && coded.Code != cerbapi.ExternalConnectorApprovalRequired {
		return nil, false
	}
	return coded.Approval, confirmIsTerminal()
}

// confirmOnTerminal shows the plan and asks for the target. It returns the
// hash to confirm, or an error when the person did not confirm.
func confirmOnTerminal(in io.Reader, out io.Writer, shown *cerbapi.ConnectorPlan) (string, error) {
	if shown == nil || shown.PlanHash == "" {
		return "", errors.New("the serving Cerberus returned no plan to confirm; nothing ran")
	}
	writeConfirmPlan(out, shown)
	name := confirmTargetName(shown.Plan)
	fmt.Fprintf(out, "\nType the target (%s) to confirm, or anything else to cancel: ", name)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) != name {
		return "", errors.New("the confirmation did not match the target; nothing ran")
	}
	return shown.PlanHash, nil
}

// planFrom decodes a plan result: in process it is the value, over the
// socket a decoded JSON object.
func planFrom(data any) (*cerbapi.ConnectorPlan, error) {
	switch v := data.(type) {
	case cerbapi.ConnectorPlan:
		return &v, nil
	case *cerbapi.ConnectorPlan:
		return v, nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var p cerbapi.ConnectorPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode the plan to confirm: %w", err)
	}
	return &p, nil
}

func bold(s string) string { return "\x1b[1m" + s + "\x1b[0m" }

// writeConfirmPlan prints what is about to be confirmed. The target is what
// is typed; the hash is what is bound, printed whole.
func writeConfirmPlan(out io.Writer, shown *cerbapi.ConnectorPlan) {
	p := shown.Plan
	fmt.Fprintf(out, "Cerberus needs you to confirm %s %s on this terminal.\n\n", p.Connector, p.Operation)
	fmt.Fprintln(out, bold("Effect:      "+p.Effect))
	fmt.Fprintln(out, bold("Target:      "+targetLine(p.Target)))
	fmt.Fprintln(out, bold("Computed by: "+string(shown.ComputedBy)))
	writePlanBody(out, p, "  ")
	fmt.Fprintf(out, "\nPlan hash: %s\n", shown.PlanHash)
}

func writePlanBody(out io.Writer, p plan.Plan, indent string) {
	if p.State != "" {
		fmt.Fprintf(out, "%sState:     %s\n", indent, p.State)
	}
	if p.Source != nil {
		dirty := ""
		if p.Source.Dirty {
			dirty = " (uncommitted changes)"
		}
		fmt.Fprintf(out, "%sSource:    %s at %s%s\n", indent, p.Source.Path, p.Source.HEAD, dirty)
	}
	if p.Artifact != "" {
		fmt.Fprintf(out, "%sArtifact:  %s\n", indent, p.Artifact)
	}
	if len(p.Preview) > 0 {
		fmt.Fprintf(out, "%sPreview (%s): %s\n", indent, p.PreviewKind, string(p.Preview))
	}
	if p.PluginEntrypointSHA256 != "" {
		fmt.Fprintf(out, "%sPlugin:    %s\n", indent, p.PluginEntrypointSHA256)
	}
	for i, step := range p.Steps {
		fmt.Fprintf(out, "%sStep %d:    %s: %s\n", indent, i+1, step.Name, step.Command)
		if step.Dir != "" {
			fmt.Fprintf(out, "%s           in %s\n", indent, step.Dir)
		}
		if len(step.Env) > 0 {
			fmt.Fprintf(out, "%s           env: %s\n", indent, strings.Join(step.Env, " "))
		}
	}
	for _, action := range p.Actions {
		fmt.Fprintf(out, "%s%s %s\n", indent, bold(action.Operation), targetLine(action.Target))
		writePlanBody(out, action, indent+"  ")
	}
	if len(p.Digests) > 0 {
		keys := make([]string, 0, len(p.Digests))
		for k := range p.Digests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(out, "%sBinds:     %s\n", indent, strings.Join(keys, ", "))
	}
}

// targetLine is a target with its labels.
func targetLine(t audit.Target) string {
	name := t.Resource
	if name == "" {
		name = firstField(t.Fields)
	}
	labels := []string{"env " + labelOrUnknown(t.Env), "owner " + labelOrUnknown(t.Owner), "admin " + labelOrUnknown(t.Admin)}
	if len(t.Tags) > 0 {
		labels = append(labels, "tags "+strings.Join(t.Tags, ","))
	}
	return strings.TrimSpace(t.Kind+" "+name) + " (" + strings.Join(labels, ", ") + ")"
}

// confirmTargetName is what the person types: the resource, else the
// target's first field, else its kind.
func confirmTargetName(p plan.Plan) string {
	switch {
	case p.Target.Resource != "":
		return p.Target.Resource
	case len(p.Target.Fields) > 0:
		return firstField(p.Target.Fields)
	case p.Target.Kind != "":
		return p.Target.Kind
	}
	return p.Connector + "." + p.Operation
}

func firstField(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return fields[keys[0]]
}

func labelOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
