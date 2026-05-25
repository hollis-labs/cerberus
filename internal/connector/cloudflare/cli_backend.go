package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// CLIBackend implements Backend by shelling out to the wrangler CLI.
type CLIBackend struct {
	wranglerPath string
}

// DetectWrangler returns the path to the wrangler binary and whether it was found.
func DetectWrangler() (string, bool) {
	path, err := exec.LookPath("wrangler")
	if err != nil {
		return "", false
	}
	return path, true
}

// NewCLIBackend creates a CLIBackend after verifying that wrangler is available in PATH.
func NewCLIBackend() (*CLIBackend, error) {
	path, found := DetectWrangler()
	if !found {
		return nil, fmt.Errorf("wrangler CLI not found in PATH")
	}
	return &CLIBackend{wranglerPath: path}, nil
}

// newCLIBackendWithPath creates a CLIBackend using an already-resolved wrangler path.
func newCLIBackendWithPath(path string) *CLIBackend {
	return &CLIBackend{wranglerPath: path}
}

func (c *CLIBackend) ListZones(_ context.Context) ([]Zone, error) {
	return nil, fmt.Errorf("wrangler CLI does not support listing zones — use API backend (set CERBERUS_CLOUDFLARE_API_TOKEN)")
}

func (c *CLIBackend) CreateZone(_ context.Context, _, _, _ string) (*Zone, error) {
	return nil, fmt.Errorf("wrangler CLI does not support creating zones — use API backend (set CERBERUS_CLOUDFLARE_API_TOKEN)")
}

// --- intermediate JSON structs for wrangler DNS output ---

type wranglerDNSRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
}

func (c *CLIBackend) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	out, err := c.run(ctx, "dns", "records", "list", zoneID, "--json")
	if err != nil {
		return nil, fmt.Errorf("wrangler dns records list %s: %w", zoneID, err)
	}

	var records []wranglerDNSRecord
	if err := json.Unmarshal(out, &records); err != nil {
		return nil, fmt.Errorf("parsing wrangler dns records output: %w", err)
	}

	result := make([]DNSRecord, len(records))
	for i, r := range records {
		result[i] = DNSRecord(r)
	}
	return result, nil
}

func (c *CLIBackend) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error) {
	args := []string{
		"dns", "records", "create", zoneID,
		"--type", rec.Type,
		"--name", rec.Name,
		"--content", rec.Content,
		"--json",
	}
	if rec.TTL > 0 {
		args = append(args, "--ttl", fmt.Sprintf("%d", rec.TTL))
	}
	if rec.Proxied {
		args = append(args, "--proxied", "true")
	}

	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("wrangler dns records create in zone %s: %w", zoneID, err)
	}

	var created wranglerDNSRecord
	if err := json.Unmarshal(out, &created); err != nil {
		return nil, fmt.Errorf("parsing wrangler dns create output: %w", err)
	}

	return &DNSRecord{
		ID:       created.ID,
		Type:     created.Type,
		Name:     created.Name,
		Content:  created.Content,
		TTL:      created.TTL,
		Proxied:  created.Proxied,
		Priority: created.Priority,
	}, nil
}

func (c *CLIBackend) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := c.run(ctx, "dns", "records", "delete", zoneID, recordID)
	if err != nil {
		return fmt.Errorf("wrangler dns records delete %s in zone %s: %w", recordID, zoneID, err)
	}
	return nil
}

func (c *CLIBackend) ListTunnels(_ context.Context, _ string) ([]Tunnel, error) {
	return nil, fmt.Errorf("wrangler CLI does not support listing tunnels — use API backend (set CERBERUS_CLOUDFLARE_API_TOKEN)")
}

// run executes a wrangler subcommand and returns its stdout bytes.
func (c *CLIBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.wranglerPath, args...) //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok { //nolint:errorlint
			return nil, fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
		return nil, err
	}
	return out, nil
}
