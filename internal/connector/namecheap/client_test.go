package namecheap

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestClientSetCustomNameservers(t *testing.T) {
	client := NewClient("apiuser", "apikey", "username", "127.0.0.1")
	client.http = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			query := req.URL.Query()
			if got := query.Get("Command"); got != "namecheap.domains.dns.setCustom" {
				t.Fatalf("Command = %q, want setCustom", got)
			}
			if got := query.Get("SLD"); got != "chrispian" {
				t.Fatalf("SLD = %q, want chrispian", got)
			}
			if got := query.Get("TLD"); got != "dev" {
				t.Fatalf("TLD = %q, want dev", got)
			}
			if got := query.Get("NameServers"); got != "aldo.ns.cloudflare.com,betty.ns.cloudflare.com" {
				t.Fatalf("NameServers = %q", got)
			}
			body := `<?xml version="1.0" encoding="UTF-8"?>
<ApiResponse xmlns="http://api.namecheap.com/xml.response" Status="OK">
  <Errors />
  <CommandResponse Type="namecheap.domains.dns.setCustom">
    <DomainDNSSetCustomResult Domain="chrispian.dev" Updated="true" />
  </CommandResponse>
</ApiResponse>`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	update, err := client.SetCustomNameservers(context.Background(), "chrispian.dev", []string{"aldo.ns.cloudflare.com", "betty.ns.cloudflare.com"})
	if err != nil {
		t.Fatalf("SetCustomNameservers: %v", err)
	}
	if !update.Updated || update.Domain != "chrispian.dev" || len(update.NameServers) != 2 {
		t.Fatalf("update = %#v", update)
	}
}
