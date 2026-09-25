package cerbapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

// Accepting a review copies the plugin's suggested policy into a working
// file, shown in the review, and applies nothing: the applied snapshot is
// untouched until `cerberus policy apply` (D7, Decision 10, I10).
func TestReviewCopiesSuggestedPolicyIntoAWorkingFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	policyDir := filepath.Join(filepath.Dir(statePath), "policy")
	withRules := func(rules ...pluginsdk.SuggestedRule) func(*pluginhost.PluginYAML) {
		return func(p *pluginhost.PluginYAML) { p.Cerberus.SuggestedPolicy = rules }
	}
	dir := reviewablePluginDir(t, "docker", withRules(pluginsdk.SuggestedRule{Operation: "logs", Require: pluginsdk.RequireApprovalForAgents}))
	r := NewPluginReviewer(audit.NewMemory(), statePath)
	pending, err := r.PrepareInstall(inProcess(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pending.Text(), "+ docker: approve [logs] for agent") || !strings.Contains(pending.Text(), "nothing is enforced until you review it and run `cerberus policy apply`") {
		t.Fatalf("review:\n%s", pending.Text())
	}
	if _, err = r.Accept(inProcess(), pending, "docker"); err != nil {
		t.Fatal(err)
	}
	store := policy.Store{Dir: policyDir}
	f, ok, err := store.ReadWorking(filepath.Join("providers", "docker.yaml"))
	if err != nil || !ok || len(f.Providers["docker"].Rules) != 1 {
		t.Fatalf("working file: %+v %v %v", f, ok, err)
	}
	if _, err = os.Stat(filepath.Join(policyDir, "applied.yaml")); !os.IsNotExist(err) {
		t.Fatal("accepting a review applied policy")
	}

	// An upgrade that changes the suggestion shows the diff.
	next := reviewablePluginDir(t, "docker", func(p *pluginhost.PluginYAML) {
		p.Version = "2.0.0"
		p.Cerberus.SuggestedPolicy = []pluginsdk.SuggestedRule{{Operation: "logs", Require: pluginsdk.RequireDeny}}
	})
	pending, err = r.PrepareInstall(inProcess(), next, false)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(pending.PolicyChange, "\n")
	if !strings.Contains(text, "- docker: approve [logs] for agent") || !strings.Contains(text, "+ docker: deny [logs]") {
		t.Fatalf("policy diff:\n%s", text)
	}
	r.Discard(pending)
}
