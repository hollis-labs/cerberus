package namecheap

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func emailTestClient(t *testing.T, mode string, onWrite func(*http.Request)) *Client {
	t.Helper()
	c := NewClient("user", "test-key", "user", "127.0.0.1")
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Query().Get("Command") {
		case "namecheap.domains.dns.getHosts":
			body = fmt.Sprintf(`<ApiResponse Status="OK"><CommandResponse><DomainDNSGetHostsResult EmailType="%s"><host HostId="3" Name="@" Type="A" Address="192.0.2.1" TTL="300"/></DomainDNSGetHostsResult></CommandResponse></ApiResponse>`, mode)
		case "namecheap.domains.dns.setHosts":
			onWrite(req)
			body = `<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="true"/></CommandResponse></ApiResponse>`
		default:
			t.Fatalf("unexpected command")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return c
}

func TestCreatePreservesDomainEmailMode(t *testing.T) {
	for _, mode := range []string{"FWD", "MX", "MXE", "OX", "NONE"} {
		t.Run(mode, func(t *testing.T) {
			wrote := false
			client := emailTestClient(t, mode, func(req *http.Request) {
				wrote = true
				q := req.URL.Query()
				if q.Get("EmailType") != mode {
					t.Fatal("email mode reset")
				}
				if q.Get("HostName1") != "@" || q.Get("HostName2") != "www" {
					t.Fatal("host records not carried")
				}
			})
			connector := NewWithBackend(client)
			_, err := connector.CreateDNSRecord(context.Background(), "example.com", DNSRecord{Type: "A", Host: "www", Value: "192.0.2.2"})
			if err != nil || !wrote {
				t.Fatalf("create failed: %v", err)
			}
		})
	}
}

func TestMXUnderForwardingRefusedBeforeWrite(t *testing.T) {
	client := emailTestClient(t, "FWD", func(*http.Request) { t.Fatal("conflicting MX write reached API") })
	connector := NewWithBackend(client)
	_, err := connector.CreateDNSRecord(context.Background(), "example.com", DNSRecord{Type: "MX", Host: "send", Value: "mail.example.com"})
	if err == nil || !strings.Contains(err.Error(), "EmailType=FWD") {
		t.Fatalf("missing actionable conflict: %v", err)
	}
}

func TestEmptySetCarriesEmailModeAndUnknownModeRefuses(t *testing.T) {
	client := emailTestClient(t, "FWD", func(req *http.Request) {
		if req.URL.Query().Get("EmailType") != "FWD" {
			t.Fatal("empty set lost email mode")
		}
	})
	if err := client.SetDNSRecords(context.Background(), "example", "com", nil); err != nil {
		t.Fatal(err)
	}
	client = emailTestClient(t, "", func(*http.Request) { t.Fatal("unknown mode reached write") })
	if err := client.SetDNSRecords(context.Background(), "example", "com", nil); err == nil {
		t.Fatal("missing EmailType allowed reset")
	}
}

func TestExplicitModeChangeUsesCallerCompleteSet(t *testing.T) {
	client := emailTestClient(t, "FWD", func(req *http.Request) {
		q := req.URL.Query()
		if q.Get("EmailType") != "MX" || q.Get("Address1") != "p=AA/BB" || q.Get("RecordType2") != "MX" {
			t.Fatal("replacement changed caller intent")
		}
	})
	err := NewWithBackend(client).SetDNSRecordSet(context.Background(), "example.com", DNSRecordSet{EmailType: "MX", Records: []DNSRecord{{Type: "TXT", Host: "resend._domainkey", Value: "p=AA/BB"}, {Type: "MX", Host: "send", Value: "mail.example.com", MXPref: 10}}})
	if err != nil {
		t.Fatal(err)
	}
}
