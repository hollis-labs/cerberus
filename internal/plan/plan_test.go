package plan

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
)

func base() Plan {
	return Plan{Lane: LaneAdmin, Connector: "docker", Operation: "stop", Effect: "lifecycle",
		Target:     audit.Target{Kind: "docker.container", Resource: "dev-box", Env: "dev", Owner: "self", Admin: "self"},
		ArgsDigest: "hmac-sha256:aa"}
}

func TestHashIsStableAndSensitive(t *testing.T) {
	a, err := base().Hash()
	if err != nil || !strings.HasPrefix(a, "sha256:") {
		t.Fatal(a, err)
	}
	b, _ := base().Hash()
	if a != b {
		t.Fatal("the same plan hashed differently")
	}
	// Preview field order does not matter; content does.
	p1, p2 := base(), base()
	_ = p1.WithPreview("host", map[string]any{"b": 1, "a": 2})
	_ = p2.WithPreview("host", struct {
		A int `json:"a"`
		B int `json:"b"`
	}{2, 1})
	h1, _ := p1.Hash()
	h2, _ := p2.Hash()
	if h1 != h2 {
		t.Fatal("key order changed the hash")
	}
	for name, mutate := range map[string]func(*Plan){
		"args":        func(p *Plan) { p.ArgsDigest = "hmac-sha256:bb" },
		"label":       func(p *Plan) { p.Target.Env = "prod" },
		"preview":     func(p *Plan) { _ = p.WithPreview("server", map[string]any{"replicas": 3}) },
		"plugin":      func(p *Plan) { p.PluginEntrypointSHA256 = "x" },
		"plugin conf": func(p *Plan) { p.PluginConfigSHA256 = "y" },
		"step":        func(p *Plan) { p.Steps = []Step{{Name: "deploy", Command: "vercel deploy"}} },
		"source":      func(p *Plan) { p.Source = &Source{Path: "/r", HEAD: "abc", Dirty: true} },
		"state":       func(p *Plan) { p.State = "running" },
	} {
		p := base()
		mutate(&p)
		if h, _ := p.Hash(); h == a {
			t.Errorf("%s did not change the hash", name)
		}
	}
}
