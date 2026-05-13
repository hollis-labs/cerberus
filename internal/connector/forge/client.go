package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultBaseURL = "https://forge.laravel.com/api/v1"

// Client wraps net/http for the Laravel Forge REST API.
type Client struct {
	httpClient *http.Client
	apiToken   string
	baseURL    string
}

// NewClient creates a Forge API client with the given token.
func NewClient(apiToken string) *Client {
	return &Client{
		httpClient: &http.Client{},
		apiToken:   apiToken,
		baseURL:    defaultBaseURL,
	}
}

// ListServers returns all servers on the account.
func (c *Client) ListServers(ctx context.Context) ([]Server, error) {
	body, err := c.get(ctx, "/servers")
	if err != nil {
		return nil, fmt.Errorf("forge list servers: %w", err)
	}

	var resp struct {
		Servers []Server `json:"servers"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("forge list servers: parse response: %w", err)
	}
	return resp.Servers, nil
}

// GetServer returns a single server by ID.
func (c *Client) GetServer(ctx context.Context, serverID int) (*Server, error) {
	body, err := c.get(ctx, fmt.Sprintf("/servers/%d", serverID))
	if err != nil {
		return nil, fmt.Errorf("forge get server: %w", err)
	}

	var resp struct {
		Server Server `json:"server"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("forge get server: parse response: %w", err)
	}
	return &resp.Server, nil
}

// ListSites returns all sites on a server.
func (c *Client) ListSites(ctx context.Context, serverID int) ([]Site, error) {
	body, err := c.get(ctx, fmt.Sprintf("/servers/%d/sites", serverID))
	if err != nil {
		return nil, fmt.Errorf("forge list sites: %w", err)
	}

	var resp struct {
		Sites []Site `json:"sites"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("forge list sites: parse response: %w", err)
	}
	return resp.Sites, nil
}

// GetSite returns a single site on a server.
func (c *Client) GetSite(ctx context.Context, serverID, siteID int) (*Site, error) {
	body, err := c.get(ctx, fmt.Sprintf("/servers/%d/sites/%d", serverID, siteID))
	if err != nil {
		return nil, fmt.Errorf("forge get site: %w", err)
	}

	var resp struct {
		Site Site `json:"site"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("forge get site: parse response: %w", err)
	}
	return &resp.Site, nil
}

// GetDeploymentScript returns the current deployment script for a site.
func (c *Client) GetDeploymentScript(ctx context.Context, serverID, siteID int) (string, error) {
	body, err := c.get(ctx, fmt.Sprintf("/servers/%d/sites/%d/deployment/script", serverID, siteID))
	if err != nil {
		return "", fmt.Errorf("forge get deployment script: %w", err)
	}
	return string(body), nil
}

// UpdateDeploymentScript updates the deployment script for a site.
func (c *Client) UpdateDeploymentScript(ctx context.Context, serverID, siteID int, content string, autoSource bool) error {
	payload := map[string]any{
		"content":     content,
		"auto_source": autoSource,
	}
	if _, err := c.requestJSON(ctx, http.MethodPut, fmt.Sprintf("/servers/%d/sites/%d/deployment/script", serverID, siteID), payload); err != nil {
		return fmt.Errorf("forge update deployment script: %w", err)
	}
	return nil
}

// DeploySite triggers a deployment for a site.
func (c *Client) DeploySite(ctx context.Context, serverID, siteID int) error {
	if _, err := c.requestJSON(ctx, http.MethodPost, fmt.Sprintf("/servers/%d/sites/%d/deployment/deploy", serverID, siteID), nil); err != nil {
		return fmt.Errorf("forge deploy site: %w", err)
	}
	return nil
}

// ExecuteSiteCommand runs a command from within a site's root directory.
func (c *Client) ExecuteSiteCommand(ctx context.Context, serverID, siteID int, command string) (*SiteCommand, error) {
	body, err := c.requestJSON(ctx, http.MethodPost, fmt.Sprintf("/servers/%d/sites/%d/commands", serverID, siteID), map[string]any{
		"command": command,
	})
	if err != nil {
		return nil, fmt.Errorf("forge execute site command: %w", err)
	}

	var resp struct {
		Command SiteCommand `json:"command"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("forge execute site command: parse response: %w", err)
	}
	return &resp.Command, nil
}

// get performs an authenticated GET request and returns the response body.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	return c.requestJSON(ctx, http.MethodGet, path, nil)
}

func (c *Client) requestJSON(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
