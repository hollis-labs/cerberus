package cerbapi

import (
	"fmt"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
)

// UnsupervisedStatus is what the runtime reports for a resource kind the
// supervision lane does not own.
//
// It is deliberately a word and not an empty string. A blank STATUS cell is
// indistinguishable from a probe that failed, so the one honest answer — this
// resource is administered, not watched — used to read as a defect.
const UnsupervisedStatus = "unsupervised"

// SupervisedLocally reports whether the supervision lane owns a resource kind.
//
// The lane is hardcoded to local/process in roughly ten places, deliberately —
// see docs/adr/0002 and docs/plans/infra-admin-control-plane.md. Everything else
// is a named handle for connector operations: `muctlvaig` is server/ssh and has
// always worked that way, and a container/docker resource is the same shape.
func SupervisedLocally(resourceType, connector string) bool {
	return resourceType == string(domain.ResourceProcess) && connector == "local"
}

// UnsupervisedReason explains, in one line, why a resource has no runtime state.
func UnsupervisedReason(resourceType, connector string) string {
	return fmt.Sprintf(
		"%s/%s resources are administered through the %s connector, not supervised by the local runtime lane",
		resourceType, connector, connector)
}

// UnsupervisedNextStep names the commands that do administer the resource.
//
// Naming them is the whole point: "status currently supports local process
// resources only" told an operator what did not work and left them to guess
// what did.
func UnsupervisedNextStep(id, connector string) string {
	switch connector {
	case "docker":
		return fmt.Sprintf("cerberus docker up %s | cerberus docker down %s | cerberus docker logs %s", id, id, id)
	case "ssh":
		return fmt.Sprintf("cerberus ssh status %s | cerberus ssh exec %s -- <command>", id, id)
	default:
		return fmt.Sprintf("cerberus connectors describe %s", connector)
	}
}

// NewUnsupervisedRuntimeStatus reports what is true about an unsupervised
// resource rather than refusing to answer.
//
// `resource status` on a server or container resource is a reasonable question
// with a real answer — the kind, the connector, and the commands that operate
// it — so it returns that answer instead of an error.
func NewUnsupervisedRuntimeStatus(res *config.ResourceDef) *ResourceRuntimeStatus {
	return &ResourceRuntimeStatus{
		ID:                  res.ID,
		Name:                res.Name,
		Type:                res.Type,
		Project:             res.Project,
		Connector:           res.Connector,
		URL:                 configString(res.Config, "url"),
		Port:                configInt(res.Config, "port"),
		Status:              UnsupervisedStatus,
		RecommendedReason:   UnsupervisedReason(res.Type, res.Connector),
		RecommendedNextStep: UnsupervisedNextStep(res.ID, res.Connector),
	}
}

// UnsupervisedOperationError refuses a supervision-lane verb on a resource the
// lane does not own, and names what to run instead.
func UnsupervisedOperationError(verb string, res *config.ResourceDef) error {
	return fmt.Errorf("%s does not apply to %q: %s; try: %s",
		verb, res.ID,
		UnsupervisedReason(res.Type, res.Connector),
		UnsupervisedNextStep(res.ID, res.Connector))
}
