package webui

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/chrispian/cerberus/internal/infra"
)

type infraResponse struct {
	StatePath   string               `json:"state_path,omitempty"`
	Providers   []infraProviderDTO   `json:"providers"`
	Deployments []infraDeploymentDTO `json:"deployments"`
	Suggestions []infraDeploymentDTO `json:"suggestions,omitempty"`
	Error       string               `json:"error,omitempty"`
}

type infraProviderDTO struct {
	ID      string                   `json:"id"`
	Label   string                   `json:"label"`
	Fields  []infraProviderFieldDTO  `json:"fields"`
	Secrets []infraProviderSecretDTO `json:"secrets"`
	Values  map[string]string        `json:"values,omitempty"`
}

type infraProviderFieldDTO struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type infraProviderSecretDTO struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Present     bool   `json:"present"`
}

type infraProviderSaveRequest struct {
	Values       map[string]string `json:"values"`
	Secrets      map[string]string `json:"secrets"`
	ClearSecrets []string          `json:"clear_secrets"`
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
	Suggested        bool   `json:"suggested,omitempty"`
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

func (s *Server) handleInfraProviderByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/infra/providers/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "expected POST /api/infra/providers/{id}")
		return
	}
	if _, ok := providerCatalog()[id]; !ok {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}

	var req infraProviderSaveRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := infra.LoadState(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg := state.Providers[id]
	if cfg.Values == nil {
		cfg.Values = map[string]string{}
	}
	for key, value := range req.Values {
		cfg.Values[key] = strings.TrimSpace(value)
	}
	state.Providers[id] = cfg
	if err := infra.SaveState(s.configPath, state); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for key, value := range req.Secrets {
		if s.secrets != nil && strings.TrimSpace(value) != "" {
			if err := s.secrets.Set(r.Context(), id, key, value); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	for _, key := range req.ClearSecrets {
		if s.secrets != nil {
			if err := s.secrets.Delete(r.Context(), id, key); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		resp, err := s.infraResponse(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, infraResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deployments": resp.Deployments,
			"suggestions": resp.Suggestions,
			"state_path":  resp.StatePath,
		})
	case http.MethodPost:
		if !s.allowStateChangingRequest(r) {
			writeError(w, http.StatusForbidden, "state-changing request rejected")
			return
		}
		var dto infraDeploymentDTO
		if err := decodeJSONBody(r, &dto); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		profile := dto.toProfile()
		if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.RepoPath) == "" {
			writeError(w, http.StatusBadRequest, "id, name, provider, and repo_path are required")
			return
		}
		state, err := infra.LoadState(s.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		state.UpsertProfile(profile)
		if err := infra.SaveState(s.configPath, state); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
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
	action := parts[1]

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
		state, err := infra.LoadState(s.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !state.DeleteProfile(id) {
			writeError(w, http.StatusNotFound, "deployment profile not found")
			return
		}
		if err := infra.SaveState(s.configPath, state); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
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
		result, err := infra.RunDeployment(r.Context(), s.secrets, profile)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
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

	savedIDs := map[string]bool{}
	deployments := make([]infraDeploymentDTO, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		savedIDs[profile.ID] = true
		deployments = append(deployments, dtoFromProfile(profile, false))
	}
	suggestions := []infraDeploymentDTO{}
	for _, profile := range infra.SuggestedProfiles() {
		if savedIDs[profile.ID] {
			continue
		}
		suggestions = append(suggestions, dtoFromProfile(profile, true))
	}

	resp := infraResponse{
		StatePath:   statePath,
		Providers:   s.providerDTOs(r, state),
		Deployments: deployments,
		Suggestions: suggestions,
	}
	return resp, nil
}

func (s *Server) providerDTOs(r *http.Request, state *infra.State) []infraProviderDTO {
	catalog := providerCatalog()
	order := []string{"vercel", "github", "cloudflare", "namecheap", "git"}
	out := make([]infraProviderDTO, 0, len(order))
	for _, id := range order {
		spec := catalog[id]
		dto := infraProviderDTO{
			ID:      id,
			Label:   spec.Label,
			Fields:  append([]infraProviderFieldDTO(nil), spec.Fields...),
			Secrets: make([]infraProviderSecretDTO, 0, len(spec.Secrets)),
			Values:  map[string]string{},
		}
		if cfg, ok := state.Providers[id]; ok {
			for key, value := range cfg.Values {
				dto.Values[key] = value
			}
		}
		for _, secret := range spec.Secrets {
			present := false
			if s.secrets != nil {
				value, _ := s.secrets.Get(r.Context(), id, secret.Name)
				present = strings.TrimSpace(value) != ""
			}
			dto.Secrets = append(dto.Secrets, infraProviderSecretDTO{
				Name:        secret.Name,
				Label:       secret.Label,
				Description: secret.Description,
				Present:     present,
			})
		}
		out = append(out, dto)
	}
	return out
}

type providerSpec struct {
	Label   string
	Fields  []infraProviderFieldDTO
	Secrets []infraProviderSecretDTO
}

func providerCatalog() map[string]providerSpec {
	return map[string]providerSpec{
		"vercel": {
			Label: "Vercel",
			Fields: []infraProviderFieldDTO{
				{Name: "scope", Label: "Scope", Description: "Optional Vercel scope/team slug used for link and deploy commands."},
			},
			Secrets: []infraProviderSecretDTO{
				{Name: "token", Label: "Token", Description: "Vercel token used for non-interactive CLI operations."},
			},
		},
		"github": {
			Label: "GitHub",
			Fields: []infraProviderFieldDTO{
				{Name: "default_owner", Label: "Default owner", Description: "Default GitHub owner or organization for deployment profiles."},
			},
			Secrets: []infraProviderSecretDTO{
				{Name: "token", Label: "Token", Description: "GitHub API token used by the GitHub connector."},
			},
		},
		"cloudflare": {
			Label: "Cloudflare",
			Fields: []infraProviderFieldDTO{
				{Name: "account_id", Label: "Account ID", Description: "Cloudflare account ID used when creating new zones."},
				{Name: "default_zone_id", Label: "Default zone ID", Description: "Default Cloudflare zone ID for deployment and DNS flows."},
			},
			Secrets: []infraProviderSecretDTO{
				{Name: "api_token", Label: "API token", Description: "Cloudflare API token used by the Cloudflare connector."},
			},
		},
		"namecheap": {
			Label: "Namecheap",
			Fields: []infraProviderFieldDTO{
				{Name: "client_ip", Label: "Client IP", Description: "Client IP allowed for Namecheap API access."},
			},
			Secrets: []infraProviderSecretDTO{
				{Name: "api_user", Label: "API user", Description: "Namecheap API user."},
				{Name: "api_key", Label: "API key", Description: "Namecheap API key."},
				{Name: "username", Label: "Username", Description: "Namecheap account username."},
			},
		},
		"git": {
			Label: "Git",
			Fields: []infraProviderFieldDTO{
				{Name: "default_remote", Label: "Default remote", Description: "Default Git remote name for deployment profiles."},
				{Name: "default_branch", Label: "Default branch", Description: "Default production branch for deployment profiles."},
			},
		},
	}
}

func dtoFromProfile(profile infra.DeploymentProfile, suggested bool) infraDeploymentDTO {
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
		Suggested:        suggested,
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
	}
}
