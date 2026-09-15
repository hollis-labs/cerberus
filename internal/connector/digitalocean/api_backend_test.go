package digitalocean

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/digitalocean/godo"
)

func TestGodoReadBoundaryPreservesDropletFields(t *testing.T) {
	droplet := `{"id":42,"name":"api","status":"active","region":{"slug":"nyc3"},"size":{"slug":"s-1vcpu-1gb"},"image":{"slug":"ubuntu-24-04-x64"},"networks":{"v4":[{"type":"private","ip_address":"10.0.0.1"},{"type":"public","ip_address":"192.0.2.8"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/droplets":
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("SDK dropped pagination request")
			}
			_, _ = fmt.Fprintf(w, `{"droplets":[%s]}`, droplet)
		case "/v2/droplets/42":
			_, _ = fmt.Fprintf(w, `{"droplet":%s}`, droplet)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	c := NewWithClient(client)
	list, err := c.ListDroplets(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list decode failed: %+v %v", list, err)
	}
	got, err := c.GetDroplet(context.Background(), list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.IPv4 != "192.0.2.8" || got.Region != "nyc3" || got.Size != "s-1vcpu-1gb" || got.Image != "ubuntu-24-04-x64" || got.Status != "active" {
		t.Fatalf("SDK read mapping changed: %+v", got)
	}
}
