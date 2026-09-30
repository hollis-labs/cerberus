package connector

import (
	"fmt"
	"sort"
	"strings"
)

// OutputLabelKey is the JSON Schema keyword a plugin puts on a property of
// an operation's output_schema to say what its value is, for egress (P4-3):
//
//	output_schema:
//	  type: object
//	  properties:
//	    message: { type: string, x-cerberus-label: untrusted }
//	    email:   { type: string, x-cerberus-label: [personal, untrusted] }
//
// untrusted is text Cerberus did not compose, which a client should present
// as data and never as instructions; personal is personal data. An operation
// with an output_schema has been labeled, even if it labels nothing (a
// wholly structured result); one without is unlabeled, and the host marks
// its whole result untrusted.
const OutputLabelKey = "x-cerberus-label"

// Output labels.
const (
	LabelUntrusted = "untrusted"
	LabelPersonal  = "personal"
)

// OutputLabels is the vocabulary.
var OutputLabels = []string{LabelUntrusted, LabelPersonal}

// OutputLabelPointers reads an output schema's labels as JSON pointers into
// the result, a "*" segment standing for every element (items, or
// additionalProperties). The empty pointer is the whole result.
func OutputLabelPointers(schema map[string]any) (map[string][]string, error) {
	out := map[string][]string{}
	var problems []string
	walkOutputSchema(schema, "", out, &problems)
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return out, nil
}

func walkOutputSchema(node map[string]any, at string, out map[string][]string, problems *[]string) {
	if node == nil {
		return
	}
	if raw, ok := node[OutputLabelKey]; ok {
		labels, err := outputLabels(raw)
		if err != nil {
			where := at
			if where == "" {
				where = "(the whole result)"
			}
			*problems = append(*problems, fmt.Sprintf("%s at %s: %v", OutputLabelKey, where, err))
		} else {
			out[at] = labels
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			if child, ok := sub.(map[string]any); ok {
				walkOutputSchema(child, at+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(name), out, problems)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		walkOutputSchema(items, at+"/*", out, problems)
	}
	if extra, ok := node["additionalProperties"].(map[string]any); ok {
		walkOutputSchema(extra, at+"/*", out, problems)
	}
}

func outputLabels(raw any) ([]string, error) {
	var values []string
	switch v := raw.(type) {
	case string:
		values = []string{v}
	case []any:
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%v is not a label", e)
			}
			values = append(values, s)
		}
	case []string:
		values = v
	default:
		return nil, fmt.Errorf("must be a label or a list of labels, not %T", raw)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("an empty list labels nothing")
	}
	for _, s := range values {
		if !contains(OutputLabels, s) {
			return nil, fmt.Errorf("%q is not one of %s", s, strings.Join(OutputLabels, ", "))
		}
	}
	return values, nil
}
