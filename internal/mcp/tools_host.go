package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// HostTools are the host tools (ADR 0004): list, get, probe and capacity.
// All four are reads, and none ranks or picks a host: the result says what
// is known, how old it is, and which hosts could not be judged.
//
// Everything an agent must act on is written into the result's text, ahead of
// the JSON: a stale or never-probed host, an offline host, the reason a host
// was excluded. Claude Code shows the model the content blocks only, so
// nothing here depends on _meta.
func HostTools(client cerbapi.HostClient) []Tool {
	return []Tool{
		contractTool(Tool{
			Name:        "cerberus_host_list",
			Description: "List declared hosts with recorded state and probed facts (budgeted envelope). Each host reports state, probed_at and stale; stale or never-probed hosts are called out in the text. Facts only: hosts are in name order, not ranked.",
			InputSchema: objectSchema(map[string]interface{}{
				"role":     map[string]interface{}{"type": "string", "description": "Only hosts with this declared role."},
				"provider": map[string]interface{}{"type": "string", "description": "Only hosts with this provider."},
				"state":    map[string]interface{}{"type": "string", "enum": []string{"online", "offline", "unknown"}},
				"labels":   map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}, "description": "Only hosts with all of these labels."},
				"limit":    limitSchemaProp(),
				"offset":   offsetSchemaProp(),
			}),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				views, err := client.ListHosts(ctx, cerbapi.HostFilter{
					Role: stringArg(args, "role"), Provider: stringArg(args, "provider"), State: stringArg(args, "state"),
					Labels: stringMapArg(args, "labels"),
				})
				if err != nil {
					return "", err
				}
				var notes []string
				for _, v := range views {
					if note := hostNote(v); note != "" {
						notes = append(notes, note)
					}
				}
				var envelope any
				if err := json.Unmarshal([]byte(budgetedList("cerberus_host_list", views, args, "%d hosts total.")), &envelope); err != nil {
					return "", err
				}
				return withNotes(envelope, notes)
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_host_get",
			Description: "Show one declared host: declared specs and probed facts side by side, state, probed_at and stale.",
			InputSchema: objectSchema(map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Host name, as in cerberus_host_list."},
			}, "name"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				v, err := client.GetHost(ctx, stringArg(args, "name"))
				if err != nil {
					return "", err
				}
				return hostViewText(v)
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_host_probe",
			Description: "Measure one host over ssh with a fixed read-only script and record the facts. An unreachable host is reported as state offline, not as an error. Returns the host as cerberus_host_get does.",
			InputSchema: objectSchema(map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Host name, as in cerberus_host_list."},
			}, "name"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				v, err := client.ProbeHost(ctx, stringArg(args, "name"))
				if err != nil {
					return "", err
				}
				return hostViewText(v)
			},
		}),
		contractTool(Tool{
			Name: "cerberus_host_capacity",
			Description: "List the hosts that satisfy constraints, from recorded facts, in name order. Facts only: Cerberus does not rank, reserve or choose a host, and the caller decides placement. " +
				"A host with no probe data, or data older than max_stale_age, is reported under unknown and never guessed to fit; call cerberus_host_probe on it for fresh facts. Set explain=true to also list excluded hosts with the reason for each.",
			InputSchema: objectSchema(map[string]interface{}{
				"min_memory_available": map[string]interface{}{"type": []string{"string", "integer"}, "description": "Minimum available memory, in bytes or a size such as 4GiB."},
				"min_disk_free":        map[string]interface{}{"type": []string{"string", "integer"}, "description": "Minimum free disk on the data mount, in bytes or a size such as 50GB."},
				"min_cpu":              map[string]interface{}{"type": "integer", "description": "Minimum probed logical cores."},
				"role":                 map[string]interface{}{"type": "string"},
				"provider":             map[string]interface{}{"type": "string"},
				"labels":               map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}, "description": "Labels that must all match exactly."},
				"require_tools":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string", "enum": []string{"docker", "tmux", "tailscale", "caddy"}}, "description": "Probed tools that must be installed."},
				"max_stale_age":        map[string]interface{}{"type": "string", "description": "Oldest probe a measured constraint is judged on, such as 10m. Default 5m."},
				"include_offline":      map[string]interface{}{"type": "boolean", "description": "Judge offline hosts on their last measurement (shown with its age) instead of excluding them."},
				"explain":              map[string]interface{}{"type": "boolean", "description": "Also list excluded hosts with the reason for each."},
			}),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				data, err := json.Marshal(args)
				if err != nil {
					return "", err
				}
				var q cerbapi.CapacityQuery
				if err = json.Unmarshal(data, &q); err != nil {
					return "", err
				}
				res, err := client.HostCapacity(ctx, q)
				if err != nil {
					return "", err
				}
				return capacityText(res)
			},
		}),
	}
}

func init() {
	for _, op := range []string{cerbapi.OpHostList, cerbapi.OpHostGet, cerbapi.OpHostProbe, cerbapi.OpHostCapacity} {
		toolOperations["cerberus_host_"+op] = opRef{cerbapi.HostDefinition, op}
	}
}

func stringMapArg(args map[string]interface{}, key string) map[string]string {
	raw, _ := args[key].(map[string]interface{})
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// hostNote is the sentence an agent needs about one host that is not a
// current, online measurement; empty when there is nothing to act on.
func hostNote(v cerbapi.HostView) string {
	name := v.Host.Name
	switch {
	case v.Probed == nil && v.State == "offline":
		return fmt.Sprintf("%s: offline and never measured (%s). Its facts are unknown.", name, v.Reason)
	case v.Probed == nil:
		return fmt.Sprintf("%s: never probed, so its facts are unknown. Call cerberus_host_probe to measure it.", name)
	case v.State == "offline":
		return fmt.Sprintf("%s: offline (%s). The facts shown are the last good measurement%s.", name, v.Reason, probedAt(v))
	case v.Stale:
		return fmt.Sprintf("%s: the facts are stale%s. Call cerberus_host_probe for current ones.", name, probedAt(v))
	}
	return ""
}

func probedAt(v cerbapi.HostView) string {
	if v.ProbedAt == nil {
		return ""
	}
	return ", measured " + v.ProbedAt.UTC().Format("2006-01-02T15:04:05Z")
}

// withNotes adds a "notes" array to a JSON object and returns the result as
// text. The notes sit inside the result's first content block, which stays
// the result's own JSON, so the model reads them with the facts they qualify.
func withNotes(body any, notes []string) (string, error) {
	data, err := redact.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return string(data), nil
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&obj); err != nil {
		return "", err
	}
	obj["notes"] = notes
	out, err := redact.MarshalIndent(obj, "", "  ")
	return string(out), err
}

func hostViewText(v cerbapi.HostView) (any, error) {
	var notes []string
	if note := hostNote(v); note != "" {
		notes = append(notes, note)
	}
	return withNotes(v, notes)
}

// capacityText writes the capacity result for a model. The result already
// carries every unknown and excluded host with its reasons; the notes add the
// caveat on any matched host whose facts are old, and state in words that
// nothing was ranked or reserved.
func capacityText(res cerbapi.CapacityResult) (any, error) {
	notes := []string{fmt.Sprintf("%d matched, %d unknown, of %d declared hosts (probe age limit %s). Facts only: hosts are in name order, not ranked, and none is reserved.",
		len(res.Matched), len(res.Unknown), res.Total, res.MaxStaleAge)}
	for _, u := range res.Unknown {
		notes = append(notes, fmt.Sprintf("UNKNOWN %s (not guessed to fit): %s. %s", u.Name, strings.Join(u.Reasons, "; "), u.NextStep))
	}
	for _, x := range res.Excluded {
		notes = append(notes, fmt.Sprintf("EXCLUDED %s: %s", x.Name, strings.Join(x.Reasons, "; ")))
	}
	for _, v := range res.Matched {
		if note := hostNote(v); note != "" {
			notes = append(notes, "MATCHED, with a caveat: "+note)
		}
	}
	return withNotes(res, notes)
}
