package cerbapi

import (
	"strings"
	"testing"

	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/policy"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// An SFTP download into a local path writes the operator's files: a
// read_sensitive token cannot run it, and policy decides it as a write,
// so a rule or baseline that allows reads does not cover it (H2).
func TestLocalFilesystemReadsAreAuthorizedAsWrites(t *testing.T) {
	op, ok := sshconn.Definition().Operation("get")
	if !ok || op.Effect != contract.EffectReadSensitive || op.LocalFS != contract.LocalFSWrites {
		t.Fatalf("ssh get's contract changed: %+v", op)
	}
	spec := auditSpec{connector: "ssh", operation: "get", op: op, known: true}
	if req := policyRequest(as(Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}), spec, policyTargetFor("box")); req.Effect != contract.EffectWrite {
		t.Fatalf("policy sees %s", req.Effect)
	}
	ctx := WithPrincipal(as(Principal{}), Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP, AuthMethod: AuthOAuth, Subject: "client:t", Scopes: []string{"cerberus:read_sensitive"}})
	err := scopeRefusal(ctx, spec)
	if connectorErrorCode(err) != ExternalConnectorInsufficientScope || !strings.Contains(err.Error(), "needs cerberus:operate") {
		t.Fatalf("a read_sensitive token on ssh get: %v", err)
	}
	// An allow for read_sensitive no longer covers it.
	f := policy.File{Version: policy.FileVersion, Principals: []policy.PrincipalBlock{{Match: policy.PrincipalMatch{Kind: "agent"},
		Rules: []policy.Rule{{ID: "logs", Effect: []contract.Effect{contract.EffectReadSensitive}, Decision: policy.Allow}}}}}
	res := policy.NewEvaluator(f, "t").Authorize(policy.Request{Connector: "ssh", Operation: "get", Effect: op.PolicyEffect(), Principal: policy.Principal{Kind: "agent"}})
	for _, m := range res.Matched {
		if m.Rule == "logs" {
			t.Fatalf("a read_sensitive allow matched a local write: %+v", res)
		}
	}
}
