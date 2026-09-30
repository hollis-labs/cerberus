package webui

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/target"
)

type infraResponse struct {
	StatePath   string               `json:"state_path,omitempty"`
	Deployments []infraDeploymentDTO `json:"deployments"`
	Error       string               `json:"error,omitempty"`
}

type infraDeploymentDTO struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	RepoPath         string `json:"repo_path"`
	Domain           string `json:"domain,omitempty"`
	DNSProvider      string `json:"dns_provider,omitempty"`
	ProductionBranch string `json:"production_branch,omitempty"`
	GitRemote        string `json:"git_remote,omitempty"`
	GitProvider      string `json:"git_provider,omitempty"`
	GitOwner         string `json:"git_owner,omitempty"`
	GitRepo          string `json:"git_repo,omitempty"`
	VercelProject    string `json:"vercel_project,omitempty"`
	VercelScope      string `json:"vercel_scope,omitempty"`
	CloudflareZoneID string `json:"cloudflare_zone_id,omitempty"`
	NamecheapDomain  string `json:"namecheap_domain,omitempty"`
	PreflightCommand string `json:"preflight_command,omitempty"`
	BuildCommand     string `json:"build_command,omitempty"`
	DeployCommand    string `json:"deploy_command,omitempty"`
	// The profile's target labels (CERB-GAP-886).
	Env   string   `json:"env,omitempty"`
	Owner string   `json:"owner,omitempty"`
	Admin string   `json:"admin,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

func (s *Server) handleInfra(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp, err := s.infraResponse(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, infraResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	_, route := consoleRoute(r.URL.Path)
	switch r.Method {
	case http.MethodGet:
		if route != "" {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		resp, err := s.infraResponse(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, infraResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deployments": resp.Deployments,
			"state_path":  resp.StatePath,
		})
	case http.MethodPost:
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		var dto struct {
			infraDeploymentDTO
			consoleConfirm
		}
		if err := decodeJSONBody(r, &dto); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// A profile carries shell commands and target labels: saving one is
		// an admin write (M9). The serving process checks it.
		profile := dto.toProfile()
		if _, ok := s.consoleWrite(w, r, route, dto.consoleConfirm, cerbapi.ConsoleWriteRequest{Operation: cerbapi.ConsoleProfileSave, Profile: &profile}); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleDeploymentByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/deployments/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "expected /api/deployments/{id}/{action}")
		return
	}
	id := parts[0]
	action := strings.Join(parts[1:], "/")
	route := ""
	if action == "delete/plan" || action == "delete/confirm" {
		action, route = consoleRoute(action)
	}

	switch action {
	case "delete":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		var confirm consoleConfirm
		if err := decodeJSONBody(r, &confirm); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, ok := s.consoleWrite(w, r, route, confirm, cerbapi.ConsoleWriteRequest{Operation: cerbapi.ConsoleProfileDelete, ID: id}); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	case "run":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		opts, err := decodeMutationBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// The daemon runs it, where the approval broker is (CERB-GAP-886).
		result, err := s.client.RunDeploymentProfile(r.Context(), id, opts...)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "run/plan", "run/confirm":
		route := strings.TrimPrefix(action, "run/")
		opts, ok := s.planOrConfirm(w, r, route)
		if !ok {
			return
		}
		if route == "plan" {
			p, perr := s.client.PlanDeploymentProfile(r.Context(), id, opts...)
			if perr != nil {
				writeClientError(w, perr)
				return
			}
			writePlan(w, p)
			return
		}
		result, err := s.client.RunDeploymentProfile(r.Context(), id, opts...)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "plan":
		// What run would execute, for the confirm step: the operator
		// confirms against these commands (Decision 3). Read-only.
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		state, err := infra.LoadState(s.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		profile, ok := state.Profile(id)
		if !ok {
			writeError(w, http.StatusNotFound, "deployment profile not found")
			return
		}
		writeJSON(w, http.StatusOK, infra.PlanDeployment(r.Context(), s.secrets, profile))
	default:
		writeError(w, http.StatusNotFound, "unknown deployment action")
	}
}

func (s *Server) infraResponse(r *http.Request) (infraResponse, error) {
	state, err := infra.LoadState(s.configPath)
	if err != nil {
		return infraResponse{}, err
	}
	statePath, err := infra.StatePathFor(s.configPath)
	if err != nil {
		return infraResponse{}, err
	}

	deployments := make([]infraDeploymentDTO, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		deployments = append(deployments, dtoFromProfile(profile))
	}

	resp := infraResponse{
		StatePath:   statePath,
		Deployments: deployments,
	}
	return resp, nil
}

func dtoFromProfile(profile infra.DeploymentProfile) infraDeploymentDTO {
	return infraDeploymentDTO{
		ID:               profile.ID,
		Name:             profile.Name,
		Provider:         profile.Provider,
		RepoPath:         profile.RepoPath,
		Domain:           profile.Domain,
		DNSProvider:      profile.DNSProvider,
		ProductionBranch: profile.ProductionBranch,
		GitRemote:        profile.GitRemote,
		GitProvider:      profile.GitProvider,
		GitOwner:         profile.GitOwner,
		GitRepo:          profile.GitRepo,
		VercelProject:    profile.VercelProject,
		VercelScope:      profile.VercelScope,
		CloudflareZoneID: profile.CloudflareZoneID,
		NamecheapDomain:  profile.NamecheapDomain,
		PreflightCommand: profile.PreflightCommand,
		BuildCommand:     profile.BuildCommand,
		DeployCommand:    profile.DeployCommand,
		Env:              string(profile.Env),
		Owner:            profile.Owner,
		Admin:            profile.Admin.Default,
		Tags:             profile.Tags,
	}
}

func (d infraDeploymentDTO) toProfile() infra.DeploymentProfile {
	return infra.DeploymentProfile{
		ID:               strings.TrimSpace(d.ID),
		Name:             strings.TrimSpace(d.Name),
		Provider:         strings.TrimSpace(d.Provider),
		RepoPath:         filepath.Clean(strings.TrimSpace(d.RepoPath)),
		Domain:           strings.TrimSpace(d.Domain),
		DNSProvider:      strings.TrimSpace(d.DNSProvider),
		ProductionBranch: strings.TrimSpace(d.ProductionBranch),
		GitRemote:        strings.TrimSpace(d.GitRemote),
		GitProvider:      strings.TrimSpace(d.GitProvider),
		GitOwner:         strings.TrimSpace(d.GitOwner),
		GitRepo:          strings.TrimSpace(d.GitRepo),
		VercelProject:    strings.TrimSpace(d.VercelProject),
		VercelScope:      strings.TrimSpace(d.VercelScope),
		CloudflareZoneID: strings.TrimSpace(d.CloudflareZoneID),
		NamecheapDomain:  strings.TrimSpace(d.NamecheapDomain),
		PreflightCommand: strings.TrimSpace(d.PreflightCommand),
		BuildCommand:     strings.TrimSpace(d.BuildCommand),
		DeployCommand:    strings.TrimSpace(d.DeployCommand),
		Env:              target.Env(strings.TrimSpace(d.Env)),
		Owner:            strings.TrimSpace(d.Owner),
		Admin:            target.Admin{Default: strings.TrimSpace(d.Admin)},
		Tags:             d.Tags,
	}
}
