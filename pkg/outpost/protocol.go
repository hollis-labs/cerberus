// Package outpost defines Cerberus's fixed thin-executor protocol. Outpost
// provides authenticated per-plugin routing; Cerberus owns policy and approval.
package outpost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type Request struct {
	Verb  string `json:"verb"`
	Unit  string `json:"unit,omitempty"`
	Lines int    `json:"lines,omitempty"`
}

type Result struct {
	Output    string `json:"output" cerb:"untrusted"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated,omitempty"`
}

var unitRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,254}$`)

func (r Request) Validate() error {
	switch r.Verb {
	case "probe", "units":
		if r.Unit != "" || r.Lines != 0 {
			return fmt.Errorf("%s accepts no unit or lines", r.Verb)
		}
	case "logs", "start", "stop", "restart":
		if !unitRE.MatchString(r.Unit) {
			return fmt.Errorf("invalid unit identifier")
		}
		if r.Verb != "logs" && r.Lines != 0 {
			return fmt.Errorf("lines is only valid for logs")
		}
		if r.Lines < 0 || r.Lines > 1000 {
			return fmt.Errorf("lines must be between 0 and 1000")
		}
	default:
		return fmt.Errorf("unsupported outpost verb %q", r.Verb)
	}
	return nil
}

func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("outpost URL must be an origin without credentials, query or path")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip, err := netip.ParseAddr(u.Hostname())
	private := ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)
	if u.Scheme != "http" || err != nil || !private {
		return fmt.Errorf("outpost URL requires HTTPS or a literal loopback/Tailscale HTTP address")
	}
	return nil
}

type Client struct {
	URL, TokenFile string
	Credential     func(string)
}

func (c Client) Execute(ctx context.Context, request Request) (Result, error) {
	var result Result
	if err := request.Validate(); err != nil {
		return result, err
	}
	if err := ValidateEndpoint(c.URL); err != nil {
		return result, err
	}
	info, err := os.Stat(c.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return result, fmt.Errorf("outpost credential must be a readable private regular file")
	}
	b, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return result, fmt.Errorf("read outpost credential")
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return result, fmt.Errorf("empty outpost credential")
	}
	if c.Credential != nil {
		c.Credential(token)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/api/plugins/cerberus/execute", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return result, fmt.Errorf("outpost request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return result, fmt.Errorf("outpost request refused (HTTP %d)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return result, fmt.Errorf("invalid or oversized outpost response")
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("invalid outpost response")
	}
	return result, nil
}
