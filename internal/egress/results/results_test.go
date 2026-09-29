package results

import (
	"sort"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/egress"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func builtinDefinitions() []contract.Definition {
	defs := []contract.Definition{dockerconn.Definition(), ghconn.Definition(), sshconn.Definition()}
	return append(defs, cerbapi.RuntimeDefinitions()...)
}

// Every built-in operation that returns free text has a registered result
// with something labeled untrusted, so a new text-returning operation
// cannot arrive unlabeled (WP-S7's acceptance, P4-1).
func TestFreeTextOperationsAreLabeled(t *testing.T) {
	declared := map[string]bool{}
	for _, def := range builtinDefinitions() {
		for _, op := range def.Operations {
			key := def.ID + "." + op.Name
			declared[key] = true
			if op.Output != contract.OutputFreeText {
				continue
			}
			r, ok := For(def.ID, op.Name)
			if !ok {
				t.Errorf("%s returns free text and has no registered result", key)
				continue
			}
			fields, err := r.Fields()
			if err != nil {
				t.Errorf("%s: %v", key, err)
				continue
			}
			if !egress.Has(fields, egress.Untrusted) {
				t.Errorf("%s returns free text and labels nothing untrusted", key)
			}
		}
	}
	for key, r := range All() {
		if !declared[key] {
			t.Errorf("%s is registered but no built-in declares it", key)
		}
		if _, err := r.Fields(); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
}

// The labeled places, pinned: a label removed from a DTO shows here.
func TestUntrustedFieldsArePinned(t *testing.T) {
	want := map[string]string{
		"local.logs":                "/content",
		"local.deploy":              "/build_output /install_output",
		"docker.logs":               "(whole result)",
		"ssh.exec":                  "/stderr /stdout",
		"ssh.status":                "/os",
		"pipeline.run":              "/error /stages/*/error",
		"infra.run_profile":         "/error /steps/*/error /steps/*/output",
		"github.status":             "/description",
		"github.list_releases":      "/*/name",
		"github.list_workflow_runs": "/*/branch /*/name",
	}
	for key, pointers := range want {
		connector, op, _ := strings.Cut(key, ".")
		r, ok := For(connector, op)
		if !ok {
			t.Errorf("%s is not registered", key)
			continue
		}
		fields, _ := r.Fields()
		var got []string
		for _, f := range fields {
			for _, l := range f.Labels {
				if l == egress.Untrusted {
					p := f.Pointer
					if p == "" {
						p = "(whole result)"
					}
					got = append(got, p)
				}
			}
		}
		sort.Strings(got)
		if strings.Join(got, " ") != pointers {
			t.Errorf("%s: untrusted at %q, want %q", key, strings.Join(got, " "), pointers)
		}
	}
}
