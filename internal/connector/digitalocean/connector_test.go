package digitalocean

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

type fakeBackend struct {
	created *godo.DropletCreateRequest
	id      int
}

func (b *fakeBackend) CreateDroplet(_ context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error) {
	b.created = req
	return &godo.Droplet{ID: 17, Name: req.Name, Status: "new", Region: &godo.Region{Slug: req.Region}, Size: &godo.Size{Slug: req.Size}, Image: &godo.Image{Slug: req.Image.Slug}}, nil
}

func (b *fakeBackend) PowerOnDroplet(_ context.Context, id int) error  { b.id = id; return nil }
func (b *fakeBackend) PowerOffDroplet(_ context.Context, id int) error { b.id = id; return nil }
func (b *fakeBackend) DeleteDroplet(_ context.Context, id int) error   { b.id = id; return nil }

func (b *fakeBackend) GetDroplet(_ context.Context, id int) (*godo.Droplet, error) {
	b.id = id
	return &godo.Droplet{ID: id, Name: "web", Status: "active"}, nil
}

func (b *fakeBackend) ListDroplets(_ context.Context) ([]godo.Droplet, error) {
	return []godo.Droplet{{ID: 17, Name: "web", Status: "active"}}, nil
}

func TestDigitalOceanConnectorLifecycleUsesDropletID(t *testing.T) {
	backend := &fakeBackend{}
	conn := NewWithBackend(backend)
	res := &resource.Resource{ID: "srv", Name: "web", Config: map[string]any{"droplet_id": 17}}

	if err := conn.Start(context.Background(), res); err != nil {
		t.Fatalf("start: %v", err)
	}
	if backend.id != 17 {
		t.Fatalf("start id = %d, want 17", backend.id)
	}

	state, err := conn.Status(context.Background(), res)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if state != resource.StateRunning {
		t.Fatalf("state = %q, want %q", state, resource.StateRunning)
	}

	if err := conn.Destroy(context.Background(), res); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if backend.id != 17 {
		t.Fatalf("destroy id = %d, want 17", backend.id)
	}
}

func TestDigitalOceanConnectorCreateStoresDropletID(t *testing.T) {
	backend := &fakeBackend{}
	conn := NewWithBackend(backend)
	res := &resource.Resource{
		Name: "web-1",
		Config: map[string]any{
			"name":   "web-1",
			"region": "nyc3",
			"size":   "s-1vcpu-1gb",
			"image":  "ubuntu-24-04-x64",
		},
	}

	if err := conn.Create(context.Background(), res); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := backend.created.Name; got != "web-1" {
		t.Fatalf("created name = %q, want web-1", got)
	}
	if got, ok := res.Config["droplet_id"].(int); !ok || got != 17 {
		t.Fatalf("droplet_id = %#v, want 17", res.Config["droplet_id"])
	}
}

func TestDigitalOceanConnectorDefinition(t *testing.T) {
	conn := NewWithBackend(&fakeBackend{})
	def := conn.Definition()

	if def.ID != "digitalocean" {
		t.Fatalf("ID = %q, want digitalocean", def.ID)
	}
	if len(def.ResourceTypes) != 1 || def.ResourceTypes[0] != "server" {
		t.Fatalf("ResourceTypes = %#v, want [server]", def.ResourceTypes)
	}
	if !def.Capabilities.CanCreate || !def.Capabilities.CanDestroy || !def.Capabilities.CanHealth {
		t.Fatalf("Capabilities = %#v", def.Capabilities)
	}
	if len(def.Operations) == 0 {
		t.Fatal("expected operations")
	}

	var _ contract.Describer = conn
}
