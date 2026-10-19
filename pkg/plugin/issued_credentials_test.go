package plugin

import (
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

func issuanceSpec() PluginYAML {
	m := validManifest()
	m.Operations = []contract.ManifestOperation{{Name: "provision", Effect: contract.EffectWrite, Preview: contract.PreviewPlugin, LocalFS: contract.LocalFSWrites, InputSchema: contract.ObjectSchema(map[string]any{"issuance_id": contract.StringSchema("Request ID")}, "issuance_id"), IssuedSecrets: []string{"runtime"}}}
	p := PluginYAMLFromManifest(m, Entrypoint{Command: "bin/plugin"})
	p.Capabilities = []subprocess.CapabilityRequest{{Name: CredentialIssuanceCapability}}
	return p
}
func TestIssuanceRequiresExplicitVersionedCapabilityAndClosedWriteContract(t *testing.T) {
	p := issuanceSpec()
	if err := p.Validate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*PluginYAML){
		func(p *PluginYAML) { p.Capabilities = nil },
		func(p *PluginYAML) { p.Cerberus.Connector.Operations[0].Effect = contract.EffectRead },
		func(p *PluginYAML) { p.Cerberus.Connector.Operations[0].Preview = contract.PreviewNone },
		func(p *PluginYAML) { p.Cerberus.Connector.Operations[0].IssuedSecrets = []string{"runtime", "runtime"} },
		func(p *PluginYAML) {
			p.Cerberus.Connector.Operations[0].InputSchema = contract.ObjectSchema(map[string]any{})
		},
		func(p *PluginYAML) {
			p.Cerberus.Connector.Config.Fields = []contract.ConfigField{{Name: IssuanceConfigKey, Type: "string"}}
		},
	} {
		p := issuanceSpec()
		mutate(&p)
		if p.Validate(t.TempDir()) == nil {
			t.Fatal("unsafe issuance declaration accepted")
		}
	}
}
func TestIssuedDeclarationsSurviveManifestRoundTrip(t *testing.T) {
	p := issuanceSpec()
	d := contract.DefinitionFromManifest(p.Cerberus.Connector)
	m := contract.ManifestFromDefinition(d)
	if strings.Join(m.Operations[0].IssuedSecrets, ",") != "runtime" {
		t.Fatal("issuance lost across discovery")
	}
}
