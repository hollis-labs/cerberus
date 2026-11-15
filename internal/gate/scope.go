package gate

import (
	"context"
	"strings"

	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// scopeRefusal refuses a verified caller's call its scopes do not cover.
// It runs before the brakes and policy, in every mode: a scope is what the
// operator granted the token, not a decision to shadow. A data session
// (dataaccess) reaches only its own connector's query.
func scopeRefusal(ctx context.Context, spec Spec) error {
	if proof, ok := dataaccess.FromContext(ctx); ok {
		if spec.Connector == proof.Connector && spec.Operation == "query" && spec.Known && spec.Op.Effect == contract.EffectReadSensitive {
			return nil
		}
		return redact.Guidance("a data session cannot invoke administrative operations")
	}
	p, ok := PrincipalFrom(ctx)
	if !ok || !p.Verified() || spec.Automation {
		return nil
	}
	// A read that touches local files needs operate, like a write (H2).
	effect := spec.Op.PolicyEffect()
	if !spec.Known || effect == "" {
		effect = contract.EffectExec
	}
	if oauth.AllowsOp(p.Scopes, spec.Connector, spec.Operation, effect) {
		return nil
	}
	return insufficientScope(spec.Connector, spec.Operation, effect, p.Scopes, p.Client)
}

// insufficientScope is the refusal an agent reads: the text says what the
// token lacks and what to ask for, because a client may show the model
// only the text of a result.
func insufficientScope(connector, operation string, effect contract.Effect, scopes []string, client string) error {
	need := oauth.RequiredScopeOp(connector, operation, effect)
	if client == "" {
		client = "<client>"
	}
	issue := need
	if i := strings.Index(need, " "); i > 0 {
		issue = need[:i] // the narrow scope to ask for, without the alternative
	}
	return codedError(opArgs{Connector: connector, Operation: operation}, CodeInsufficientScope,
		redact.Guidance("this token's scopes (%s) do not cover %s %s, a %s operation, which needs %s. You cannot widen them yourself: ask your operator for a token with %s (`cerberus mcp-http token issue --client %s --scope %s` on their terminal)",
			strings.Join(scopes, " "), connector, operation, effect, need, issue, client, issue))
}
