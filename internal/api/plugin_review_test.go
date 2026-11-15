package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// reviewAPI is the API with an admin lane over the adopted root root (a fresh one when empty).
func reviewAPI(t *testing.T, root string) (*gatedAPI, string) {
	t.Helper()
	return reviewAPIWith(t, root, gatetest.Options{})
}

// reviewAPIWith is reviewAPI over a gate built with o.
func reviewAPIWith(t *testing.T, root string, o gatetest.Options) (*gatedAPI, string) {
	t.Helper()
	w := gatetest.New(t, o)
	if root == "" {
		root = filepath.Join(t.TempDir(), "adopted")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := admin.New(admin.Options{Gate: w.Gate, Paths: admin.Paths{AdoptRoot: root, ConfigDir: t.TempDir()}, Keyring: memKeyring{},
		HostVersion: "test", Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Plugins().Stop(t.Context()) })
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Gate: w.Gate, Admin: svc})
	if err != nil {
		t.Fatal(err)
	}
	return &gatedAPI{h: a.Mux(), w: w}, root
}

// echoSource writes the echo plugin's source as plugin id and returns the directory.
func echoSource(t *testing.T, id string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := admintest.WriteSource(src, echoBinary, id); err != nil {
		t.Fatal(err)
	}
	return src
}

// installedEntry is id's entry in root's plugin-connectors.json, or false.
func installedEntry(t *testing.T, root, id string) (map[string]any, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "plugin-connectors.json")) //nolint:gosec // the test's own file
	if err != nil {
		return nil, false
	}
	var st struct {
		Entries []map[string]any `json:"entries"`
	}
	if err = json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	for _, e := range st.Entries {
		if e["id"] == id {
			return e, true
		}
	}
	return nil, false
}

// accepted decodes a 200 accepted review.
func accepted(t *testing.T, call func() (int, string)) admin.ReviewResult {
	t.Helper()
	code, body := call()
	var res admin.ReviewResult
	if code != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil || res.State.ID == "" {
		t.Fatalf("want 200 with the accepted review, got %d %s", code, body)
	}
	return res
}

// wantInstalled checks the entry the accepted review wrote: the store copy at
// plugins/<id>/<digest-hex> in the adopted root, with its review, accepted_at and bundle_digest.
func wantInstalled(t *testing.T, root string, res admin.ReviewResult) {
	t.Helper()
	digest := res.Review.BundleDigest
	dir := filepath.Join(root, "plugins", res.State.ID, strings.TrimPrefix(digest, "sha256:"))
	entry, ok := installedEntry(t, root, res.State.ID)
	review, _ := entry["review"].(map[string]any)
	if !ok || entry["plugin_dir"] != dir || entry["bundle_digest"] != digest || entry["accepted_at"] == nil || review == nil || review["bundle_digest"] != digest {
		t.Fatalf("plugin-connectors.json entry %v, want the reviewed bundle %s at %s", entry, digest, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.yaml")); err != nil {
		t.Fatalf("the store copy: %v", err)
	}
}

// The owner installs on the console: the plan form shows the review (preview kind "review") and
// installs nothing, a confirmation that is not the plugin id is refused, and the typed id
// installs it, recorded with the review.
func TestTheOwnerInstallsAPluginOnTheConsoleWithTheTypedID(t *testing.T) {
	g, root := reviewAPI(t, "")
	src := echoSource(t, "fresh")
	body := `{"plugin_dir":"` + src + `"}`
	code, out := g.call(t, ownerSession, "POST", "/api/plugins/connectors/install/plan", body)
	var shown gate.ConnectorPlan
	var preview admin.ReviewPreview
	if code != http.StatusOK || json.Unmarshal([]byte(out), &shown) != nil || shown.Plan.PreviewKind != "review" ||
		json.Unmarshal(shown.Plan.Preview, &preview) != nil || preview.ID != "fresh" || !strings.Contains(preview.Text, "fresh") {
		t.Fatalf("the plan form: %d %s", code, out)
	}
	if _, ok := installedEntry(t, root, "fresh"); ok {
		t.Fatal("the plan form installed the plugin")
	}
	code, out = g.call(t, ownerSession, "POST", "/api/plugins/connectors/install", `{"plugin_dir":"`+src+`","typed":"echo"}`)
	wantCode(t, "the wrong plugin id typed", code, out, http.StatusConflict, "acknowledgment_required")
	code, out = g.call(t, ownerSession, "POST", "/api/plugins/connectors/install", `{"plugin_dir":"relative/dir","typed":"fresh"}`)
	wantCode(t, "a relative plugin_dir", code, out, http.StatusBadRequest, "invalid_args")
	if _, ok := installedEntry(t, root, "fresh"); ok {
		t.Fatal("a refused install installed the plugin")
	}
	res := accepted(t, func() (int, string) {
		return g.call(t, ownerSession, "POST", "/api/plugins/connectors/install", `{"plugin_dir":"`+src+`","typed":"fresh"}`)
	})
	if res.Kind != "install" || res.Proof != "typed" {
		t.Fatalf("result %+v", res)
	}
	wantInstalled(t, root, res)
	var reviewed bool
	for _, r := range g.w.Records(audit.KindIntent) {
		reviewed = reviewed || (r.Connector == "plugin" && r.Operation == "install" && r.PluginReview != nil && r.PluginReview.BundleDigest == res.Review.BundleDigest)
	}
	if !reviewed {
		t.Fatal("no intent record carries the review")
	}
	code, out = g.call(t, ownerSession, "POST", "/api/plugins/connectors/install", `{"plugin_dir":"`+src+`","typed":"fresh"}`)
	wantCode(t, "the same bundle again", code, out, http.StatusConflict, "nothing_to_review")
	if code, out = g.call(t, apiKey, "GET", "/api/plugins/connectors", ""); code != http.StatusOK || !strings.Contains(out, `"id":"fresh"`) {
		t.Fatalf("the installed plugin is not listed: %d %s", code, out)
	}
}

// A key installs only with the owner's approval (the lead's ruling (a)): its call opens an
// out-of-band approval bound to the review (409 approval_pending under details), a pending
// approval installs nothing, the key cannot decide it itself (403), an MCP client is refused
// outright (428, naming both owner paths, asking for nothing), another key cannot use it, a
// source changed since the approval is plan_stale, and once the owner approves on the console
// the key's retry naming it installs, recorded with the approval as its proof; the approval is
// spent.
func TestAKeyInstallsAPluginOnlyWithTheOwnersApproval(t *testing.T) {
	g, root := reviewAPI(t, "")
	src := echoSource(t, "fresh")
	body := `{"plugin_dir":"` + src + `","typed":"fresh"}`

	code, out := g.call(t, localTrust, "POST", "/api/plugins/connectors/install", body)
	wantCode(t, "an MCP client installing", code, out, http.StatusPreconditionRequired, "approval_required")
	if !strings.Contains(out, "on the console") || !strings.Contains(out, "--approval") {
		t.Fatalf("the refusal does not name both owner paths: %s", out)
	}
	if len(g.w.Broker.List()) != 0 {
		t.Fatal("an MCP client's install asked the owner for an approval")
	}

	id := g.pendingID(t, apiKey, "POST", "/api/plugins/connectors/install", body)
	if a, ok := g.w.Broker.Get(id); !ok || a.Channel != "out_of_band" || a.Connector != "plugin" || a.Operation != "install" || a.PlanHash == "" {
		t.Fatalf("the install approval: %+v", a)
	}
	withApproval := `{"plugin_dir":"` + src + `","approval_id":"` + id + `"}`
	code, out = g.call(t, apiKey, "POST", "/api/plugins/connectors/install", withApproval)
	wantCode(t, "a key installing on a pending approval", code, out, http.StatusConflict, "approval_pending")
	code, out = g.call(t, apiKey, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`)
	wantCode(t, "a key approving its own install", code, out, http.StatusForbidden, "user_required")
	if _, ok := installedEntry(t, root, "fresh"); ok {
		t.Fatal("installed without the owner's approval")
	}
	if code, out = g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true}`); code != http.StatusOK {
		t.Fatalf("the owner's approve: %d %s", code, out)
	}
	code, out = g.call(t, localTrust, "POST", "/api/plugins/connectors/install", withApproval)
	wantCode(t, "an MCP client naming the owner's approval", code, out, http.StatusPreconditionRequired, "approval_required")
	code, out = g.call(t, otherKey, "POST", "/api/plugins/connectors/install", withApproval)
	wantCode(t, "another key naming the approval", code, out, http.StatusPreconditionRequired, "approval_required")

	extra := filepath.Join(src, "EXTRA")
	if err := os.WriteFile(extra, []byte("not what was approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = g.call(t, apiKey, "POST", "/api/plugins/connectors/install", withApproval)
	wantCode(t, "a source changed since the approval", code, out, http.StatusConflict, "plan_stale")
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedEntry(t, root, "fresh"); ok {
		t.Fatal("a refused install installed the plugin")
	}

	res := accepted(t, func() (int, string) {
		return g.call(t, apiKey, "POST", "/api/plugins/connectors/install", withApproval)
	})
	if res.Proof != "approval:"+id || res.Kind != "install" {
		t.Fatalf("result %+v", res)
	}
	wantInstalled(t, root, res)
	if a, _ := g.w.Broker.Get(id); a.Status != "consumed" {
		t.Fatalf("the approval was not spent: %s", a.Status)
	}
	if err := os.WriteFile(filepath.Join(src, "NOTES"), []byte("1.0.1"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = g.call(t, apiKey, "POST", "/api/plugins/connectors/fresh/upgrade", withApproval)
	wantCode(t, "an upgrade under the spent install approval", code, out, http.StatusPreconditionRequired, "approval_required")
}

// A plugin installed before install review is reviewed once by id: a key asks for the owner's
// approval, the owner accepts on the console with the typed id; a review takes no plugin_dir.
func TestTheOwnerReviewsAPendingPlugin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adopted")
	if _, err := admintest.Install(root, echoBinary, admintest.Plugin{Pending: true}); err != nil {
		t.Fatal(err)
	}
	g, _ := reviewAPI(t, root)
	g.pendingID(t, apiKey, "POST", "/api/plugins/connectors/echo/review", `{}`)
	code, out := g.call(t, ownerSession, "POST", "/api/plugins/connectors/echo/review", `{"plugin_dir":"/elsewhere","typed":"echo"}`)
	wantCode(t, "a review naming a directory", code, out, http.StatusBadRequest, "invalid_args")
	res := accepted(t, func() (int, string) {
		return g.call(t, ownerSession, "POST", "/api/plugins/connectors/echo/review", `{"typed":"echo"}`)
	})
	if res.Kind != "review" || res.State.ReviewPending {
		t.Fatalf("result %+v", res)
	}
	wantInstalled(t, root, res)
	code, out = g.call(t, ownerSession, "POST", "/api/plugins/connectors/echo/review", `{"typed":"echo"}`)
	wantCode(t, "a second review", code, out, http.StatusConflict, "nothing_to_review")
}

// Under a policy rule enforcing approve on plugin.install, the typed id on the console is not an
// approval (the lead's ruling (a)): the owner's console install asks for the owner's approval
// (409 approval_pending) and installs nothing; once approved, the retry naming it installs with
// that one approval as its proof.
func TestThePolicyApproveOnAPluginInstallNeedsTheOwnersApproval(t *testing.T) {
	f := policy.File{Version: policy.FileVersion,
		Baseline:  &policy.BaselineOverride{ByEffect: map[contract.Effect]map[string]policy.Decision{contract.EffectAdmin: {"agent": policy.Allow, "human": policy.Allow}}},
		Providers: map[string]policy.Provider{"plugin": {Rules: []policy.Rule{{ID: "approve-plugin-install", Ops: []string{"install"}, Decision: policy.Approve}}}}}
	if problems := f.Validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	g, root := reviewAPIWith(t, "", gatetest.Options{Policy: policy.NewEvaluator(f, "test"), Enforcement: enforceAll{}})
	src := echoSource(t, "fresh")
	id := g.pendingID(t, ownerSession, "POST", "/api/plugins/connectors/install", `{"plugin_dir":"`+src+`","typed":"fresh"}`)
	if _, ok := installedEntry(t, root, "fresh"); ok {
		t.Fatal("the typed id installed the plugin under a policy approve")
	}
	if code, out := g.call(t, ownerSession, "POST", "/api/approvals/"+id+"/decide", `{"approve":true,"typed":"fresh"}`); code != http.StatusOK {
		t.Fatalf("the owner's approval: %d %s", code, out)
	}
	res := accepted(t, func() (int, string) {
		return g.call(t, ownerSession, "POST", "/api/plugins/connectors/install", `{"plugin_dir":"`+src+`","typed":"fresh","approval_id":"`+id+`"}`)
	})
	if res.Proof != "approval:"+id || len(g.w.Broker.List()) != 1 {
		t.Fatalf("result %+v; %d approvals", res, len(g.w.Broker.List()))
	}
	wantInstalled(t, root, res)
}
