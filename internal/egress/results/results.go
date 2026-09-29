// Package results says what each built-in operation returns, so its labels
// can be read: conformance holds every free_text operation to a labeled
// result (P4-1), and the MCP marker points at the labeled fields (P4-2).
package results

import (
	"reflect"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/egress"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/pipeline"
)

// Result is an operation's result: its Go type, and labels on the whole
// value when it is bare text (docker logs is a string, all of it untrusted).
type Result struct {
	Type reflect.Type
	Root []egress.Label
}

// Fields are the result's labeled places.
func (r Result) Fields() ([]egress.Field, error) {
	fields, err := egress.Fields(r.Type)
	if err != nil {
		return nil, err
	}
	if len(r.Root) > 0 {
		fields = append([]egress.Field{{Pointer: "", Labels: r.Root}}, fields...)
	}
	return fields, nil
}

func of[T any]() reflect.Type { return reflect.TypeFor[T]() }

var untrustedText = Result{Type: of[string](), Root: []egress.Label{egress.Untrusted}}

// builtins are keyed "connector.operation".
var builtins = map[string]Result{
	"local." + localconn.OpLogs:        {Type: of[cerbapi.LogLines]()},
	"local." + localconn.OpDeploy:      {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpApply:       {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpEnsureFresh: {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpReload:      {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpStop:        {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpSync:        {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpRemove:      {Type: of[cerbapi.OpResult]()},
	"local." + localconn.OpStatus:      {Type: of[cerbapi.ResourceRuntimeStatus]()},
	"local." + localconn.OpInspect:     {Type: of[cerbapi.ResourceInspect]()},
	"local." + localconn.OpDoctor:      {Type: of[cerbapi.ResourceDoctor]()},

	"pipeline." + pipeline.OpRun:  {Type: of[pipeline.RunResult]()},
	"infra." + infra.OpRunProfile: {Type: of[infra.DeploymentRunResult]()},
	"docker.logs":                 untrustedText,
	"docker.list_containers":      {Type: of[[]dockerconn.Container]()},
	"ssh.exec":                    {Type: of[sshconn.ExecResult]()},
	"ssh.status":                  {Type: of[sshconn.HostStatus]()},
	"github.status":               {Type: of[ghconn.RepoStatus]()},
	"github.list_releases":        {Type: of[[]ghconn.Release]()},
	"github.list_workflow_runs":   {Type: of[[]ghconn.WorkflowRun]()},
}

// For is what connector.operation returns, when it is a built-in whose
// result is known.
func For(connector, operation string) (Result, bool) {
	r, ok := builtins[connector+"."+operation]
	return r, ok
}

// All is every registered result, keyed "connector.operation".
func All() map[string]Result {
	out := make(map[string]Result, len(builtins))
	for k, v := range builtins {
		out[k] = v
	}
	return out
}
