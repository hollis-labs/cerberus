package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// credentialsResponse is the credential editor: every connector that
// declares a secret, built-in or installed plugin, each secret with whether a
// value is stored. It lists names and presence, never a value.
type credentialsResponse struct {
	Providers []credentialProviderDTO `json:"providers"`
	Error     string                  `json:"error,omitempty"`
}

type credentialProviderDTO struct {
	ID      string                `json:"id"`
	Version string                `json:"version,omitempty"`
	Secrets []credentialSecretDTO `json:"secrets"`
}

// credentialSecretDTO names a declared secret and says whether a value is
// stored. Under "secrets", the response walk keeps its name, kind and env as
// names by schema (walkSecretRequirement in internal/redact).
type credentialSecretDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Env         string `json:"env,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Kind        string `json:"kind"`
	Present     bool   `json:"present"`
}

type credentialSaveRequest struct {
	Secrets      map[string]string `json:"secrets"`
	ClearSecrets []string          `json:"clear_secrets"`
}

func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	defs, err := s.client.ListConnectors(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, credentialsResponse{Providers: []credentialProviderDTO{}, Error: redact.Text(err.Error())})
		return
	}
	resp := credentialsResponse{Providers: []credentialProviderDTO{}}
	for _, provider := range cerbapi.CredentialCatalog(defs) {
		dto := credentialProviderDTO{ID: provider.ID, Version: provider.Version, Secrets: make([]credentialSecretDTO, 0, len(provider.Secrets))}
		for _, secret := range provider.Secrets {
			present := false
			if s.secrets != nil {
				value, _ := s.secrets.Get(r.Context(), provider.ID, secret.Name)
				present = strings.TrimSpace(value) != ""
			}
			dto.Secrets = append(dto.Secrets, credentialSecretDTO{
				Name: secret.Name, Description: secret.Description, Env: secret.Env,
				Required: secret.Required, Kind: string(secret.Kind), Present: present,
			})
		}
		resp.Providers = append(resp.Providers, dto)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCredentialByID saves a connector's credentials: POST
// /api/credentials/{id}, with /plan and /confirm for the console's confirm
// step. The daemon checks the id and every key against what the connector
// declares, so an undeclared one is refused there, not here.
func (s *Server) handleCredentialByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	id, route := consoleRoute(strings.TrimPrefix(r.URL.Path, "/api/credentials/"))
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "expected POST /api/credentials/{id}")
		return
	}
	var req struct {
		credentialSaveRequest
		consoleConfirm
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Credentials are an admin write (M9), recorded by name: which ones
	// changed, never their values.
	result, ok := s.consoleWrite(w, r, route, req.consoleConfirm, cerbapi.ConsoleWriteRequest{Operation: cerbapi.ConsoleProviderSave, ID: id,
		Secrets: req.Secrets, ClearSecrets: req.ClearSecrets})
	if !ok {
		return
	}
	resp := map[string]any{"success": true}
	if result.SecretsChanged {
		reloaded, reloadErr := s.reloadPluginForSecrets(r.Context(), id)
		resp["plugin_reloaded"] = reloaded
		if reloadErr != nil {
			// The secret is saved either way; say the plugin still holds the
			// old value rather than failing the save.
			resp["plugin_reload_error"] = redact.Text(reloadErr.Error())
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// reloadPluginForSecrets restarts the loaded managed plugin whose id matches a
// connector whose secrets were just saved. A plugin receives its credentials
// at load, unlike a built-in, which resolves them on every call, so without
// this a console save would not take effect until someone ran `managed load`.
// It reports false when no loaded plugin has the id.
func (s *Server) reloadPluginForSecrets(ctx context.Context, id string) (bool, error) {
	plugins, err := s.client.ListManagedPlugins(ctx)
	if err != nil {
		return false, fmt.Errorf("list managed plugins: %w", err)
	}
	for _, plugin := range plugins {
		if plugin.ID != id || !plugin.Loaded {
			continue
		}
		if _, err := s.client.UnloadManagedPlugin(ctx, id); err != nil {
			return false, fmt.Errorf("unload plugin %q to pick up the new credential: %w", id, err)
		}
		if _, err := s.client.LoadManagedPlugin(ctx, id); err != nil {
			return false, fmt.Errorf("reload plugin %q after the credential change: %w; run `cerberus connectors plugin managed load %s`", id, err, id)
		}
		return true, nil
	}
	return false, nil
}
