package registry

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
)

// A host is administered, not supervised: declaring one as a resource is an
// error that points at hosts.yaml.
func TestHostIsRejectedAsAResource(t *testing.T) {
	for _, res := range []config.ResourceDef{
		{ID: "box", Type: "host", Connector: "ssh"},
		{ID: "box", Type: "server", Connector: "host"},
	} {
		pc := &ProjectConfig{Kind: ProjectConfigKind, Owner: "app", Project: config.ProjectDef{ID: "app"}, Resources: []config.ResourceDef{res}}
		vr := ValidateProjectConfig(pc)
		var found bool
		for _, e := range vr.Errors() {
			if strings.Contains(e.Message, "hosts.yaml") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%+v: errors = %v", res, vr.Errors())
		}
	}
}
