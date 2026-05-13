package pluginhost

import (
	"testing"

	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
	ghconn "github.com/chrispian/cerberus/internal/connector/github"
	contract "github.com/chrispian/cerberus/pkg/connector"
)

func TestBuiltInConnectorDefinitionsGenerateValidManifests(t *testing.T) {
	for _, def := range []contract.Definition{
		dockerconn.Definition(),
		ghconn.Definition(),
	} {
		manifest := contract.ManifestFromDefinition(def)
		if err := manifest.Validate(); err != nil {
			t.Fatalf("%s manifest invalid: %v", def.ID, err)
		}
		if manifest.ID != def.ID {
			t.Fatalf("manifest ID = %q, want %q", manifest.ID, def.ID)
		}
	}
}

func TestDockerManifestMarksDestroyAsRequiringAck(t *testing.T) {
	manifest := contract.ManifestFromDefinition(dockerconn.Definition())
	for _, op := range manifest.Operations {
		if op.Name == "destroy" {
			if !op.Destructive || !op.RequiresAck {
				t.Fatalf("destroy operation = %+v, want destructive + requires_ack", op)
			}
			return
		}
	}
	t.Fatal("docker manifest missing destroy operation")
}
