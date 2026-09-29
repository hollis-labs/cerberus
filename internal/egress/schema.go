package egress

import (
	"sort"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// FieldsFromSchema reads a plugin operation's output schema labels
// (x-cerberus-label) as Fields. A nil schema is an unlabeled operation, and
// its whole result is Untrusted: a plugin's output is text Cerberus did not
// compose until the plugin says which parts are not.
func FieldsFromSchema(schema map[string]any) ([]Field, error) {
	if schema == nil {
		return []Field{{Pointer: "", Labels: []Label{Untrusted}}}, nil
	}
	pointers, err := contract.OutputLabelPointers(schema)
	if err != nil {
		return nil, err
	}
	out := make([]Field, 0, len(pointers))
	for p, labels := range pointers {
		f := Field{Pointer: p}
		for _, l := range labels {
			f.Labels = append(f.Labels, Label(l))
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pointer < out[j].Pointer })
	return out, nil
}
