package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// planSnapshot is what an approval request binds to and shows: the plan's
// hash, and the approver's view of the plan and the arguments.
type planSnapshot struct {
	hash  string
	shown *approval.Shown
}

// specPlanSnapshot computes the lane's plan for the call once, hashes it, and
// renders what the approver is shown. A lane without a plan function binds
// to the arguments alone, and still shows them.
func specPlanSnapshot(ctx context.Context, spec auditSpec) (planSnapshot, error) {
	var p *plan.Plan
	var snap planSnapshot
	if spec.plan != nil {
		computed, err := spec.plan(ctx)
		if err != nil {
			return planSnapshot{}, err
		}
		if snap.hash, err = computed.Hash(); err != nil {
			return planSnapshot{}, err
		}
		p = &computed
	}
	snap.shown = renderShown(redact.ScopeFrom(ctx), p, spec.config)
	return snap, nil
}

// renderShown renders the approver's view: the plan and the arguments as
// they will run. A credential value the request resolved is removed (the
// value boundary), so it never lands in the approvals store, and so is a
// field named for a credential. Nothing else is rewritten: the text rules are for text Cerberus did not mean to show,
// and here they hid the requester's command behind "[REDACTED]" (B4). A
// string they would have changed is shown verbatim and flagged instead.
// The arguments, and a preview that echoes them, are marked untrusted: an
// agent wrote them.
func renderShown(scope *redact.Scope, p *plan.Plan, args map[string]any) *approval.Shown {
	shown := &approval.Shown{}
	values := scope.Redactor()
	if p != nil {
		if data, flagged, err := verbatim(values, p, "/plan"); err == nil {
			shown.Plan = data
			shown.Flagged = append(shown.Flagged, flagged...)
		}
		if len(p.Preview) > 0 {
			shown.Untrusted = append(shown.Untrusted, "/plan/preview")
		}
	}
	if len(args) > 0 {
		if data, flagged, err := verbatim(values, args, "/arguments"); err == nil {
			shown.Arguments = data
			shown.Flagged = append(shown.Flagged, flagged...)
			shown.Untrusted = append(shown.Untrusted, "/arguments")
		}
	}
	if len(shown.Plan) == 0 && len(shown.Arguments) == 0 {
		return nil
	}
	if len(shown.Plan)+len(shown.Arguments) > approval.ShownMaxBytes {
		// Keep the plan whole where it fits, and say what was cut. A cut
		// JSON document is not JSON, so what does not fit is replaced by a
		// note, never truncated mid-value.
		shown.Truncated = true
		note, _ := json.Marshal("[cerberus: too large to store with the approval; the approval still binds the whole plan by its hash. Deny it and ask for a smaller call if you cannot see what it does]")
		if len(shown.Plan) > approval.ShownMaxBytes {
			shown.Plan = note
		}
		if len(shown.Plan)+len(shown.Arguments) > approval.ShownMaxBytes {
			shown.Arguments = note
		}
	}
	return shown
}

// verbatim is v as JSON with only the request's resolved credential values
// removed, and a field named for a credential hidden, and the JSON pointers
// (under root) of the strings flagged: hidden that way, ones the text rules
// would have rewritten (shown as they are), or ones carrying an invisible or
// direction-changing character (shown escaped).
func verbatim(values redact.Redactor, v any, root string) (json.RawMessage, []string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var tree any
	if err = decoder.Decode(&tree); err != nil {
		return nil, nil, err
	}
	var flagged []string
	var walk func(node any, ptr string) any
	walk = func(node any, ptr string) any {
		switch n := node.(type) {
		case string:
			out := values.ReplaceValues(n)
			escaped := escapeInvisible(out)
			if escaped != out || redact.Text(out) != out {
				flagged = append(flagged, ptr)
			}
			return escaped
		case map[string]any:
			for k, child := range n {
				p := ptr + "/" + pointerToken(k)
				// A field known by its name to hold a credential is hidden,
				// and flagged, so the approver knows something is.
				if _, isString := child.(string); isString && redact.SensitiveKey(k) && !redact.NamesOnlyKey(k) {
					n[k] = redact.Marker
					flagged = append(flagged, p)
					continue
				}
				n[k] = walk(child, p)
			}
		case []any:
			for i, child := range n {
				n[i] = walk(child, ptr+"/"+strconv.Itoa(i))
			}
		}
		return node
	}
	tree = walk(tree, root)
	sort.Strings(flagged)
	out, err := json.Marshal(tree)
	return out, flagged, err
}

// pointerToken escapes a key for a JSON pointer (RFC 6901).
func pointerToken(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

// escapeInvisible shows each format character (zero-width, bidi override
// and isolate, BOM) as a visible \uXXXX, so the text reads as it runs.
func escapeInvisible(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&b, "\\u%04X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
