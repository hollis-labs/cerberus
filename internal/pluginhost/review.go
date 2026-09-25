package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// Review is the install review of one plugin bundle: what it declares it can
// do, in the host's reading, and where the declaration has gaps
// (docs/plans/live-systems-security-target.md, section 10). The operator
// accepts a Review, not a plugin: the accepted Review is stored, and a later
// bundle is shown as a diff against it.
//
// Everything here is the plugin's claim about itself, read strictly. It is
// built without starting the plugin.
type Review struct {
	ID               string `json:"id"`
	Version          string `json:"version"`
	Source           string `json:"source"`
	Origin           string `json:"origin"`
	Entrypoint       string `json:"entrypoint"`
	EntrypointSHA256 string `json:"entrypoint_sha256"`
	BundleDigest     string `json:"bundle_digest"`

	Operations      []ReviewOperation      `json:"operations"`
	Secrets         []ReviewSecret         `json:"secrets"`
	Capabilities    []string               `json:"capabilities"`
	SuggestedPolicy []plugin.SuggestedRule `json:"suggested_policy"`
	MCPRequested    []string               `json:"mcp_requested"`
	CLIOnly         []string               `json:"cli_only"`
	Telemetry       map[string][]string    `json:"telemetry"`
	Host            plugin.HostRange       `json:"host"`
	HostContract    int                    `json:"host_contract"`
	Gaps            []string               `json:"gaps"`
}

// ReviewOperation is one operation as the host reads it: a missing effect is
// exec, a missing preview none.
type ReviewOperation struct {
	Name           string               `json:"name"`
	Effect         contract.Effect      `json:"effect"`
	EffectDeclared bool                 `json:"effect_declared"`
	Preview        contract.PreviewKind `json:"preview"`
	Output         contract.OutputKind  `json:"output,omitempty"`
	LocalFS        contract.LocalFS     `json:"local_fs,omitempty"`
}

// ReviewSecret is a declared credential, by name.
type ReviewSecret struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// BuildReview reads a staged or in-place bundle into its review.
func BuildReview(staged *Staged, source string, origin InstallOrigin) Review {
	spec := staged.Spec
	block := spec.Cerberus
	r := Review{
		ID:               spec.ID,
		Version:          spec.Version,
		Source:           source,
		Origin:           string(origin),
		Entrypoint:       spec.Entrypoint.Command,
		EntrypointSHA256: staged.EntrypointSHA256,
		BundleDigest:     staged.Digest,
		Operations:       []ReviewOperation{},
		Secrets:          []ReviewSecret{},
		Capabilities:     []string{},
		SuggestedPolicy:  append([]plugin.SuggestedRule{}, block.SuggestedPolicy...),
		MCPRequested:     sortedCopy(block.Surfaces.MCP),
		CLIOnly:          sortedCopy(block.Surfaces.CLIOnly),
		Telemetry:        map[string][]string{},
		Host:             block.Host,
		HostContract:     plugin.ContractVersion,
		Gaps:             []string{},
	}
	for _, op := range block.Connector.Operations {
		r.Operations = append(r.Operations, ReviewOperation{
			Name:           op.Name,
			Effect:         op.EffectiveEffect(),
			EffectDeclared: op.Effect != "",
			Preview:        op.EffectivePreview(),
			Output:         op.Output,
			LocalFS:        op.LocalFS,
		})
	}
	sort.Slice(r.Operations, func(i, j int) bool { return r.Operations[i].Name < r.Operations[j].Name })
	for _, secret := range block.Connector.Config.Secrets {
		r.Secrets = append(r.Secrets, ReviewSecret{Name: secret.Name, Required: secret.Required})
	}
	sort.Slice(r.Secrets, func(i, j int) bool { return r.Secrets[i].Name < r.Secrets[j].Name })
	for _, c := range spec.Capabilities {
		r.Capabilities = append(r.Capabilities, c.Name)
	}
	sort.Strings(r.Capabilities)
	sort.Slice(r.SuggestedPolicy, func(i, j int) bool { return r.SuggestedPolicy[i].Operation < r.SuggestedPolicy[j].Operation })
	for _, t := range block.Telemetry {
		r.Telemetry[t.Operation] = sortedCopy(t.Events)
	}
	r.Gaps = reviewGaps(r, block)
	return r
}

// reviewGaps names what the declaration leaves out and how the host reads
// each gap. A gap is shown, never refused, and always reads strictly.
func reviewGaps(r Review, block plugin.CerberusPluginBlock) []string {
	gaps := append([]string{}, block.Connector.ContractGaps()...)
	for _, op := range r.Operations {
		if op.Effect.ReadOnly() {
			continue
		}
		if _, ok := r.Telemetry[op.Name]; !ok {
			gaps = append(gaps, fmt.Sprintf("no telemetry declared for %s: its audit record will carry the host's view only", op.Name))
		}
	}
	for _, op := range r.Operations {
		if op.Output == "" {
			gaps = append(gaps, fmt.Sprintf("operation %q declares no output kind: read as free text", op.Name))
		}
	}
	if !r.Host.Declared() {
		gaps = append(gaps, fmt.Sprintf("no host range declared: the plugin does not say which Cerberus contract it was built for (this host implements %d)", r.HostContract))
	}
	return gaps
}

// SummaryDigest is the digest of the review as accepted, for the audit
// record: it names exactly what the operator was shown.
func (r Review) SummaryDigest() string {
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Render is the review as the operator reads it.
func (r Review) Render() string {
	var b strings.Builder
	origin := ""
	if r.Origin == string(OriginDev) {
		origin = "  [development install: runs from its source directory; destructive operations are refused]"
	}
	fmt.Fprintf(&b, "Plugin: %s %s (from %s)%s\n", r.ID, r.Version, r.Source, origin)
	fmt.Fprintf(&b, "Entrypoint: %s  %s\n", r.Entrypoint, shortDigest(r.EntrypointSHA256))
	fmt.Fprintf(&b, "Bundle:     %s\n\n", r.BundleDigest)

	fmt.Fprintf(&b, "Operations (%d)\n", len(r.Operations))
	byEffect := map[contract.Effect][]string{}
	for _, op := range r.Operations {
		name := op.Name
		if !op.EffectDeclared {
			name += "*"
		}
		byEffect[op.Effect] = append(byEffect[op.Effect], name)
	}
	for _, effect := range contract.Effects {
		names := byEffect[effect]
		line := fmt.Sprintf("  %-15s %3d   %s", effect, len(names), strings.Join(names, ", "))
		switch {
		case effect == contract.EffectReadSensitive && len(names) > 0:
			line += "   -> free text, may carry secrets or personal data"
		case effect == contract.EffectAdmin && len(names) > 0:
			line += "   -> changes Cerberus itself"
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	if byEffect[contract.EffectExec] != nil && strings.Contains(strings.Join(byEffect[contract.EffectExec], ","), "*") {
		b.WriteString("  (* declares no effect, read as exec: every call needs acknowledgment)\n")
	}

	previews := map[contract.PreviewKind][]string{}
	for _, op := range r.Operations {
		if op.Preview != contract.PreviewNone && !op.Effect.ReadOnly() {
			previews[op.Preview] = append(previews[op.Preview], op.Name)
		}
	}
	if len(previews) == 0 {
		b.WriteString("Previews: none declared\n")
	} else {
		var parts []string
		for _, kind := range []contract.PreviewKind{contract.PreviewServer, contract.PreviewHost, contract.PreviewPlugin} {
			if names := previews[kind]; len(names) > 0 {
				parts = append(parts, fmt.Sprintf("%s on %d (%s)", kind, len(names), strings.Join(names, ", ")))
			}
		}
		fmt.Fprintf(&b, "Previews: %s\n", strings.Join(parts, "; "))
		b.WriteString("          -> claimed by the plugin; Cerberus cannot verify them. Accepting this review lets\n")
		b.WriteString("             a dry run of these operations run without --ack.\n")
	}

	if len(r.Secrets) == 0 {
		b.WriteString("Secrets:  none\n")
	} else {
		var parts []string
		for _, s := range r.Secrets {
			if s.Required {
				parts = append(parts, s.Name+" (required)")
			} else {
				parts = append(parts, s.Name+" (optional)")
			}
		}
		fmt.Fprintf(&b, "Secrets:  %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "Capabilities requested: %s\n", joinOrNone(r.Capabilities))
	if len(r.SuggestedPolicy) == 0 {
		b.WriteString("Suggested policy: none\n")
	} else {
		var parts []string
		for _, rule := range r.SuggestedPolicy {
			parts = append(parts, fmt.Sprintf("%s: %s", rule.Operation, rule.Require))
		}
		fmt.Fprintf(&b, "Suggested policy: %d rule(s) (%s)\n", len(r.SuggestedPolicy), strings.Join(parts, "; "))
		b.WriteString("                  -> shown only; Cerberus does not apply a plugin's policy\n")
	}
	fmt.Fprintf(&b, "MCP exposure requested: %s\n", describeNames(r.MCPRequested))
	b.WriteString("                        -> nothing reaches MCP until you list it under mcp.expose in connector-config.yaml\n")
	if len(r.CLIOnly) > 0 {
		fmt.Fprintf(&b, "CLI only: %s\n", strings.Join(r.CLIOnly, ", "))
	}
	if r.Host.Declared() {
		fmt.Fprintf(&b, "Host contract: %s (this host: %d)\n", describeRange(r.Host), r.HostContract)
	}

	if len(r.Gaps) > 0 {
		b.WriteString("\nGaps\n")
		for _, gap := range r.Gaps {
			fmt.Fprintf(&b, "  ! %s\n", gap)
		}
	}
	return b.String()
}

// Diff lists what changed between the accepted review and this one, one
// line per change, "+" added, "-" removed, "~" changed. An empty diff with a
// different bundle digest is a change to files the declarations do not
// describe, such as the binary.
func Diff(accepted, current Review) []string {
	var out []string
	change := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if accepted.Version != current.Version {
		change("~ version %s -> %s", accepted.Version, current.Version)
	}
	if accepted.EntrypointSHA256 != current.EntrypointSHA256 {
		change("~ entrypoint %s -> %s", shortDigest(accepted.EntrypointSHA256), shortDigest(current.EntrypointSHA256))
	}
	if accepted.Origin != current.Origin {
		change("~ origin %s -> %s", accepted.Origin, current.Origin)
	}
	oldOps, newOps := opsByName(accepted.Operations), opsByName(current.Operations)
	for _, name := range unionKeys(oldOps, newOps) {
		o, had := oldOps[name]
		n, has := newOps[name]
		switch {
		case !had:
			change("+ operation %s (%s, preview %s)", name, n.Effect, n.Preview)
		case !has:
			change("- operation %s", name)
		default:
			if o.Effect != n.Effect {
				change("~ operation %s effect %s -> %s", name, o.Effect, n.Effect)
			}
			if o.Preview != n.Preview {
				change("~ operation %s preview %s -> %s", name, o.Preview, n.Preview)
			}
			if o.Output != n.Output {
				change("~ operation %s output %s -> %s", name, orNone(string(o.Output)), orNone(string(n.Output)))
			}
			if o.LocalFS != n.LocalFS {
				change("~ operation %s local_fs %s -> %s", name, orNone(string(o.LocalFS)), orNone(string(n.LocalFS)))
			}
		}
	}
	oldSecrets, newSecrets := map[string]bool{}, map[string]bool{}
	for _, s := range accepted.Secrets {
		oldSecrets[s.Name] = s.Required
	}
	for _, s := range current.Secrets {
		newSecrets[s.Name] = s.Required
	}
	for _, name := range unionKeys(oldSecrets, newSecrets) {
		o, had := oldSecrets[name]
		n, has := newSecrets[name]
		switch {
		case !had:
			change("+ secret %s", name)
		case !has:
			change("- secret %s", name)
		case o != n:
			change("~ secret %s required %t -> %t", name, o, n)
		}
	}
	setDiff(&out, "capability", accepted.Capabilities, current.Capabilities)
	setDiff(&out, "suggested policy", rules(accepted.SuggestedPolicy), rules(current.SuggestedPolicy))
	setDiff(&out, "MCP exposure requested", accepted.MCPRequested, current.MCPRequested)
	setDiff(&out, "CLI only", accepted.CLIOnly, current.CLIOnly)
	setDiff(&out, "telemetry", telemetry(accepted.Telemetry), telemetry(current.Telemetry))
	if accepted.Host != current.Host {
		change("~ host contract %s -> %s", describeRange(accepted.Host), describeRange(current.Host))
	}
	setDiff(&out, "gap", accepted.Gaps, current.Gaps)
	if len(out) == 0 && accepted.BundleDigest != current.BundleDigest {
		change("~ bundle %s -> %s: files changed that the declarations do not describe", shortDigest(accepted.BundleDigest), shortDigest(current.BundleDigest))
	}
	return out
}

func opsByName(ops []ReviewOperation) map[string]ReviewOperation {
	out := make(map[string]ReviewOperation, len(ops))
	for _, op := range ops {
		out[op.Name] = op
	}
	return out
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setDiff(out *[]string, label string, old, current []string) {
	o, n := map[string]bool{}, map[string]bool{}
	for _, s := range old {
		o[s] = true
	}
	for _, s := range current {
		n[s] = true
	}
	for _, k := range unionKeys(o, n) {
		switch {
		case !o[k]:
			*out = append(*out, fmt.Sprintf("+ %s %s", label, k))
		case !n[k]:
			*out = append(*out, fmt.Sprintf("- %s %s", label, k))
		}
	}
}

func rules(rs []plugin.SuggestedRule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = fmt.Sprintf("%s: %s", r.Operation, r.Require)
	}
	return out
}

func telemetry(t map[string][]string) []string {
	out := make([]string, 0, len(t))
	for op, events := range t {
		out = append(out, fmt.Sprintf("%s: %s", op, strings.Join(events, ",")))
	}
	return out
}

func describeRange(r plugin.HostRange) string {
	switch {
	case !r.Declared():
		return "undeclared"
	case r.MinContract != 0 && r.MaxContract != 0:
		return fmt.Sprintf("%d..%d", r.MinContract, r.MaxContract)
	case r.MinContract != 0:
		return fmt.Sprintf(">= %d", r.MinContract)
	default:
		return fmt.Sprintf("<= %d", r.MaxContract)
	}
}

func describeNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d operation(s): %s", len(names), strings.Join(names, ", "))
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
