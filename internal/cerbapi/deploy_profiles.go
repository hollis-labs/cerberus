package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// Deploy-profile runs belong to the serving process (CERB-GAP-886): the
// daemon holds the approval broker, so a run that policy wants approved can
// be asked for, decided and consumed where it runs. The console is a client
// of these, as it is of resource verbs and pipelines.

// ErrDeploymentProfileNotFound is a run or plan of a profile the saved
// state does not have.
var ErrDeploymentProfileNotFound = errors.New("deployment profile not found")

// WithDeploySecrets is the credential store deploy-profile runs read the
// Vercel token and scope from.
func WithDeploySecrets(p secret.Provider) InProcessOption {
	return func(c *InProcessClient) { c.deploySecrets = p }
}

// deploymentProfile is the saved profile id. A profile that is not there
// is still an attempted run: it is recorded, refused, like a resource verb
// on an unknown id.
func (c *InProcessClient) deploymentProfile(ctx context.Context, id string, options []MutationOption) (infra.DeploymentProfile, error) {
	state, err := infra.LoadState(c.cfgPath)
	if err != nil {
		return infra.DeploymentProfile{}, err
	}
	profile, ok := state.Profile(id)
	if ok {
		return profile, nil
	}
	spec := deployProfileSpec(infra.DeploymentProfile{ID: id}, ApplyMutationOptions(options))
	missing := externalConnectorError(ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}, ExternalConnectorInvalidArgs,
		fmt.Errorf("%w: %q; `cerberus infra` or the console's Deployments page lists the saved ones", ErrDeploymentProfileNotFound, id))
	call, err := beginAudit(ctx, c.runtime.audit, c.logger, spec)
	if err != nil {
		return infra.DeploymentProfile{}, externalConnectorError(ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}, ExternalConnectorAuditUnavailable, err)
	}
	call.finish(missing)
	return infra.DeploymentProfile{}, missing
}

// RunDeploymentProfile runs the saved profile id through the gate.
func (c *InProcessClient) RunDeploymentProfile(ctx context.Context, id string, opts ...MutationOption) (*infra.DeploymentRunResult, error) {
	profile, err := c.deploymentProfile(ctx, id, opts)
	if err != nil {
		return nil, err
	}
	return RunDeploymentProfile(ctx, c.runtime.audit, c.deploySecrets, profile, opts...)
}

// PlanDeploymentProfile is the saved profile id's run plan.
func (c *InProcessClient) PlanDeploymentProfile(ctx context.Context, id string, opts ...MutationOption) (*ConnectorPlan, error) {
	profile, err := c.deploymentProfile(ctx, id, opts)
	if err != nil {
		return nil, err
	}
	return PlanDeploymentProfile(ctx, c.runtime.audit, c.deploySecrets, profile, opts...)
}

// RunDeploymentProfile asks the daemon to run a profile; a confirmed run
// goes to its confirm route.
func (c *SocketClient) RunDeploymentProfile(ctx context.Context, id string, options ...MutationOption) (*infra.DeploymentRunResult, error) {
	var out infra.DeploymentRunResult
	if err := c.deploymentProfileCall(ctx, id, options, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PlanDeploymentProfile asks the daemon for a profile run's plan.
func (c *SocketClient) PlanDeploymentProfile(ctx context.Context, id string, options ...MutationOption) (*ConnectorPlan, error) {
	var out ConnectorPlan
	if err := c.deploymentProfileCall(ctx, id, append(append([]MutationOption(nil), options...), WithPlan()), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SocketClient) deploymentProfileCall(ctx context.Context, id string, options []MutationOption, out any) error {
	if id == "" {
		return errors.New("deployment profile id required")
	}
	body := ApplyMutationOptions(options)
	path := "/deployments/" + url.PathEscape(id) + "/run"
	var send any = body
	switch {
	case body.ConfirmedPlanHash != "":
		path += "/confirm"
		send = confirmedBody{body, body.ConfirmedPlanHash}
	case body.Plan:
		path += "/plan"
	}
	err := c.doJSONStream(ctx, http.MethodPost, path, send, out)
	if err != nil && strings.Contains(err.Error(), "404 page not found") {
		// A daemon that predates running deploy profiles has no such route.
		return redact.GuidanceWrap(err, "the running daemon predates running deploy profiles itself, so it refused and nothing ran; restart the daemon on this build, then retry")
	}
	return err
}

// handleDeploymentsID is POST /deployments/{id}/run, /run/plan and
// /run/confirm.
func (s *SocketServer) handleDeploymentsID(w http.ResponseWriter, r *http.Request) {
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/deployments/"), "/")
	route := routeRun
	switch action {
	case "run":
	case "run/plan":
		route = routePlan
	case "run/confirm":
		route = routeConfirm
	default:
		writeJSONError(w, http.StatusNotFound, "expected /deployments/{id}/run, /run/plan or /run/confirm")
		return
	}
	if id == "" {
		writeJSONError(w, http.StatusNotFound, "deployment profile id required")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	opts, err := decodeMutationOptions(r.Body, route)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	call := func(ctx context.Context) (interface{}, error) {
		if route == routePlan {
			return s.client.PlanDeploymentProfile(ctx, id, opts...)
		}
		return s.client.RunDeploymentProfile(ctx, id, opts...)
	}
	if s.handleStream(w, r, call) {
		return
	}
	out, err := call(r.Context())
	if err != nil {
		if errors.Is(err, ErrDeploymentProfileNotFound) {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeServiceError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
