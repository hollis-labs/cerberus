package policy

import (
	"sort"

	contract "github.com/hollis-labs/cerberus/pkg/connector"

	"github.com/hollis-labs/cerberus/internal/target"
)

// Kinds are the principal kinds a sample covers.
var Kinds = []string{"human", "agent", "automation"}

// SampleTarget is a registered resource, for sampling the operations of
// its connector against it.
type SampleTarget struct {
	Connector string
	Labels    target.ResourceLabels
}

// Case is one sampled decision: an operation, a target and a principal.
type Case struct {
	Connector string          `json:"connector"`
	Operation string          `json:"operation"`
	Effect    contract.Effect `json:"effect"`
	Target    target.Target   `json:"target"`
	Kind      string          `json:"kind"`
}

// Request is the case as the decision point sees it.
func (c Case) Request() Request {
	return Request{Connector: c.Connector, Operation: c.Operation, Effect: c.Effect, Target: c.Target, Principal: Principal{Kind: c.Kind}}
}

// Sample is a representative set of decisions (D10): every declared
// operation, against every registered resource of its connector, an
// unregistered target and an ad hoc one, for every principal kind.
func Sample(defs []contract.Definition, resources []SampleTarget) []Case {
	var out []Case
	for _, def := range defs {
		for _, op := range def.Operations {
			op = op.Finalize()
			var targets []target.Target
			for _, r := range resources {
				if r.Connector == def.ID {
					labels := r.Labels
					targets = append(targets, target.Resolve(def.ID, op.Target.Kind, "", &labels, false))
				}
			}
			targets = append(targets,
				target.Resolve(def.ID, op.Target.Kind, "(unregistered)", nil, false),
				target.Resolve(def.ID, op.Target.Kind, "(ad hoc)", nil, true))
			for _, t := range targets {
				for _, kind := range Kinds {
					out = append(out, Case{Connector: def.ID, Operation: op.Name, Effect: op.Effect, Target: t, Kind: kind})
				}
			}
		}
	}
	return out
}

// Flip is a sampled decision that changes.
type Flip struct {
	Case Case   `json:"case"`
	From Result `json:"from"`
	To   Result `json:"to"`
}

// Flips are the sampled decisions that differ between two decision points.
func Flips(from, to PDP, cases []Case) []Flip {
	var out []Flip
	for _, c := range cases {
		a, b := from.Authorize(c.Request()), to.Authorize(c.Request())
		if a.Decision != b.Decision || a.WouldBlock != b.WouldBlock {
			out = append(out, Flip{Case: c, From: a, To: b})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i].Case, out[j].Case
		if x.Connector != y.Connector {
			return x.Connector < y.Connector
		}
		return x.Operation < y.Operation
	})
	return out
}
