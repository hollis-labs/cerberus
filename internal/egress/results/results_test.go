package results

import (
	"sort"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/egress"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func builtinDefinitions() []contract.Definition {
	defs := []contract.Definition{dockerconn.Definition(), sshconn.Definition()}
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
		"local.logs":         "/content",
		"local.deploy":       "/build_output /install_output",
		"docker.logs":        "(whole result)",
		"ssh.exec":           "/stderr /stdout",
		"ssh.status":         "/os",
		"pipeline.run":       "/error /stages/*/error",
		"host.get":           "/ceilings/cpu/socket/value /ceilings/memory/slot_ids/value/* /components/*/description /components/*/serial /components/*/speed /components/*/type /host/hardware/asset_tag /host/hardware/bios /host/hardware/board /host/hardware/cpu_model /host/hardware/cpu_socket /host/hardware/disks/*/model /host/hardware/disks/*/serial /host/hardware/memory/modules/* /host/hardware/memory/type /host/hardware/model /host/hardware/serial /host/hardware/vendor /host/hardware/version /probed/bios /probed/board /probed/cpu_model /probed/disks/*/model /probed/gpu_model /probed/hw_model /probed/hw_vendor /probed/hw_version /reason",
		"host.probe":         "/ceilings/cpu/socket/value /ceilings/memory/slot_ids/value/* /components/*/description /components/*/serial /components/*/speed /components/*/type /host/hardware/asset_tag /host/hardware/bios /host/hardware/board /host/hardware/cpu_model /host/hardware/cpu_socket /host/hardware/disks/*/model /host/hardware/disks/*/serial /host/hardware/memory/modules/* /host/hardware/memory/type /host/hardware/model /host/hardware/serial /host/hardware/vendor /host/hardware/version /probed/bios /probed/board /probed/cpu_model /probed/disks/*/model /probed/gpu_model /probed/hw_model /probed/hw_vendor /probed/hw_version /reason",
		"host.component_get": "/description /serial /speed /type",
		"host.capacity":      "/matched/*/ceilings/cpu/socket/value /matched/*/ceilings/memory/slot_ids/value/* /matched/*/components/*/description /matched/*/components/*/serial /matched/*/components/*/speed /matched/*/components/*/type /matched/*/host/hardware/asset_tag /matched/*/host/hardware/bios /matched/*/host/hardware/board /matched/*/host/hardware/cpu_model /matched/*/host/hardware/cpu_socket /matched/*/host/hardware/disks/*/model /matched/*/host/hardware/disks/*/serial /matched/*/host/hardware/memory/modules/* /matched/*/host/hardware/memory/type /matched/*/host/hardware/model /matched/*/host/hardware/serial /matched/*/host/hardware/vendor /matched/*/host/hardware/version /matched/*/probed/bios /matched/*/probed/board /matched/*/probed/cpu_model /matched/*/probed/disks/*/model /matched/*/probed/gpu_model /matched/*/probed/hw_model /matched/*/probed/hw_vendor /matched/*/probed/hw_version /matched/*/reason",
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

// The two readings of the same tags agree: the host's walker (egress.Fields)
// and the plugin author's (connector.OutputSchemaFor, read back through the
// manifest's pointers), for every registered result type.
func TestTagReadingsAgree(t *testing.T) {
	for key, r := range All() {
		fields, err := egress.Fields(r.Type)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		host := map[string]string{}
		for _, f := range fields {
			var ls []string
			for _, l := range f.Labels {
				ls = append(ls, string(l))
			}
			host[f.Pointer] = strings.Join(ls, ",")
		}
		schema, err := contract.OutputSchemaFor(r.Type)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		pointers, err := contract.OutputLabelPointers(schema)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		author := map[string]string{}
		for p, ls := range pointers {
			author[p] = strings.Join(ls, ",")
		}
		if len(host) != len(author) {
			t.Errorf("%s: host %v, author %v", key, host, author)
			continue
		}
		for p, ls := range host {
			if author[p] != ls {
				t.Errorf("%s: %s is %q to the host, %q to the author", key, p, ls, author[p])
			}
		}
	}
}
