package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// pluginSource writes the echo plugin's source directory as plugin id and returns it.
func pluginSource(t *testing.T, id string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src-"+id)
	if err := admintest.WriteSource(src, echoBinary, id); err != nil {
		t.Fatal(err)
	}
	return src
}

// stateEntry is id's entry in the adopted root's plugin-connectors.json, as written.
type stateEntry struct {
	ID           string `json:"id"`
	PluginDir    string `json:"plugin_dir"`
	Source       string `json:"source"`
	Loaded       bool   `json:"loaded"`
	BundleDigest string `json:"bundle_digest"`
	Review       *struct {
		ID           string `json:"id"`
		BundleDigest string `json:"bundle_digest"`
	} `json:"review"`
	AcceptedAt time.Time `json:"accepted_at"`
}

func readEntry(t *testing.T, root, id string) (stateEntry, bool) {
	t.Helper()
	path := filepath.Join(root, "plugin-connectors.json")
	data, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if errors.Is(err, os.ErrNotExist) {
		return stateEntry{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if fi, statErr := os.Stat(path); statErr != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("plugin-connectors.json mode: %v %v", fi, statErr)
	}
	var st struct {
		Version int          `json:"version"`
		Entries []stateEntry `json:"entries"`
	}
	if err = json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != 2 {
		t.Fatalf("state version %d", st.Version)
	}
	for _, e := range st.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return stateEntry{}, false
}

// staging is what is left in the plugin store's staging area.
func staging(t *testing.T, root string) []string {
	t.Helper()
	found, _ := filepath.Glob(filepath.Join(root, "plugins", ".staging-*"))
	return found
}

// The owner installs a plugin on the console by typing its id: the reviewed bundle lands in
// plugins/<id>/<digest-hex>/ in the adopted root, plugin-connectors.json gets the entry with
// its review, accepted_at and bundle_digest (0600, format 2), the intent record carries the
// review record, and the lane registers the plugin.
func TestTheOwnerInstallsAPluginByTypingItsID(t *testing.T) {
	l := newLane(t, "", nil)
	src := pluginSource(t, "fresh")
	res, shown, err := l.svc.Plugins().Install(owner(), admin.ReviewRequest{PluginDir: src, Typed: "fresh"}, admin.GateOpts{})
	if err != nil || shown != nil {
		t.Fatalf("install: %v (plan %v)", err, shown)
	}
	digest := res.Review.BundleDigest
	if res.Kind != admin.ReviewInstall || res.Proof != "typed" || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("result %+v", res)
	}
	want := filepath.Join(l.root, "plugins", "fresh", strings.TrimPrefix(digest, "sha256:"))
	if res.State.Path != want || res.State.BundleDigest != digest || res.State.ReviewPending {
		t.Fatalf("state %+v, want it running from %s", res.State, want)
	}
	if _, err = os.Stat(filepath.Join(want, "plugin.yaml")); err != nil {
		t.Fatalf("the store copy: %v", err)
	}
	entry, ok := readEntry(t, l.root, "fresh")
	if !ok || entry.PluginDir != want || entry.Source != src || entry.BundleDigest != digest || entry.AcceptedAt.IsZero() ||
		entry.Review == nil || entry.Review.BundleDigest != digest || entry.Review.ID != "fresh" {
		t.Fatalf("plugin-connectors.json entry %+v", entry)
	}
	if left := staging(t, l.root); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
	var intent *audit.Record
	for _, r := range l.world.Records(audit.KindIntent) {
		if r.Connector == "plugin" && r.Operation == "install" {
			intent = &r
		}
	}
	if intent == nil || intent.PluginReview == nil || intent.PluginReview.BundleDigest != digest || intent.PluginReview.Kind != "install" ||
		intent.PluginReview.SummarySHA256 != res.Review.SummaryDigest() || intent.PluginReview.Source != src {
		t.Fatalf("the intent's review record: %+v", intent)
	}
	if !l.svc.Plugins().Installed("fresh") {
		t.Fatal("the lane did not register the installed plugin")
	}

	// The same bundle again is nothing to review.
	if _, _, err = l.svc.Plugins().Install(owner(), admin.ReviewRequest{PluginDir: src, Typed: "fresh"}, admin.GateOpts{}); !errors.Is(err, admin.ErrNothingToReview) {
		t.Fatalf("reinstalling the same bundle: %v", err)
	}
}

// A confirmation that does not match the plugin id is refused (acknowledgment_required),
// recorded as the outcome, and installs nothing.
func TestAWrongTypedIDInstallsNothing(t *testing.T) {
	l := newLane(t, "", nil)
	src := pluginSource(t, "fresh")
	for _, typed := range []string{"", "echo", "fresh "} {
		_, _, err := l.svc.Plugins().Install(owner(), admin.ReviewRequest{PluginDir: src, Typed: typed}, admin.GateOpts{})
		if typed == "fresh " {
			// Surrounding space is trimmed, as the old terminal read did.
			if err != nil {
				t.Fatalf("typed %q: %v", typed, err)
			}
			continue
		}
		if gate.CodeOf(err) != gate.CodeAckRequired {
			t.Fatalf("typed %q: %v, want acknowledgment_required", typed, err)
		}
		if _, ok := readEntry(t, l.root, "fresh"); ok {
			t.Fatalf("typed %q installed the plugin", typed)
		}
		if left := staging(t, l.root); len(left) != 0 {
			t.Fatalf("staging left behind: %v", left)
		}
	}
	var refused int
	for _, r := range l.world.Records(audit.KindOutcome) {
		if r.Connector == "plugin" && r.Operation == "install" && r.OutcomeCode == string(gate.CodeAckRequired) {
			refused++
		}
	}
	if refused != 2 {
		t.Fatalf("%d refusals recorded, want 2", refused)
	}
}

// A key cannot install by typing the id: its call asks for the owner's out-of-band approval
// (approval_pending, bound to the review's plan), and installs nothing.
func TestAKeysInstallAsksForTheOwnersApproval(t *testing.T) {
	l := newLane(t, "", nil)
	src := pluginSource(t, "fresh")
	key := gate.WithPrincipal(gate.BeginRequest(context.Background(), gate.SurfaceSocket),
		gate.Principal{Kind: gate.PrincipalAgent, Via: "api", UID: -1, Client: "key:k1", Session: "key:k1"})
	_, _, err := l.svc.Plugins().Install(key, admin.ReviewRequest{PluginDir: src, Typed: "fresh"}, admin.GateOpts{})
	status, code, _, details, ok := gate.HTTPError(err)
	if !ok || status != 409 || code != gate.CodeApprovalPending || details.Approval == nil {
		t.Fatalf("a key's install: %v", err)
	}
	a, found := l.world.Broker.Get(details.Approval.ID)
	if !found || a.Channel != "out_of_band" || a.Connector != "plugin" || a.Operation != "install" || a.PlanHash == "" || a.ArgsDigest == "" {
		t.Fatalf("the approval: %+v", a)
	}
	if _, ok := readEntry(t, l.root, "fresh"); ok {
		t.Fatal("a key's call installed the plugin")
	}
}

// Installing an installed plugin from a changed source is an upgrade, shown as a diff against
// its accepted review; accepted, the new bundle is stored under its digest, the entry keeps
// its loaded flag, the running plugin is reloaded, and the superseded store copy is removed.
func TestAnUpgradeReplacesTheReviewedBundle(t *testing.T) {
	root, in := install(t, admintest.Plugin{Loaded: true})
	l := newLane(t, root, nil)
	if err := l.svc.Plugins().RestorePlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	src := pluginSource(t, "echo")
	if err := os.WriteFile(filepath.Join(src, "NOTES"), []byte("1.0.1"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, shown, err := l.svc.Plugins().Upgrade(owner(), "echo", admin.ReviewRequest{PluginDir: src}, admin.GateOpts{Plan: true})
	if err != nil || shown == nil || shown.Plan.PreviewKind != "review" || shown.Plan.Digests["accepted"] != in.Digest {
		t.Fatalf("the upgrade's plan: %+v %v", shown, err)
	}
	var preview admin.ReviewPreview
	if err = json.Unmarshal(shown.Plan.Preview, &preview); err != nil || preview.Kind != admin.ReviewUpgrade || !strings.Contains(preview.Text, "Changes since the review accepted") {
		t.Fatalf("the review shown: %+v %v", preview, err)
	}
	if left := staging(t, root); len(left) != 0 {
		t.Fatalf("a plan request left staging behind: %v", left)
	}
	res, _, err := l.svc.Plugins().Upgrade(owner(), "echo", admin.ReviewRequest{PluginDir: src, Typed: "echo"}, admin.GateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != admin.ReviewUpgrade || res.Review.BundleDigest == in.Digest || !res.State.Loaded {
		t.Fatalf("upgrade %+v", res)
	}
	entry, _ := readEntry(t, root, "echo")
	if entry.BundleDigest != res.Review.BundleDigest || !entry.Loaded || entry.PluginDir == in.Path {
		t.Fatalf("entry %+v", entry)
	}
	if _, err = os.Stat(in.Path); !os.IsNotExist(err) {
		t.Fatalf("the superseded store copy is still there: %v", err)
	}
	if _, _, err = l.svc.Plugins().Upgrade(owner(), "other", admin.ReviewRequest{PluginDir: src, Typed: "other"}, admin.GateOpts{}); err == nil {
		t.Fatal("an upgrade naming another plugin than the source declares")
	}
}

// A plugin installed before install review (review pending) is reviewed once, by id: its
// directory is copied into the store, and its entry gets the review.
func TestReviewingAPendingPluginRecordsItsReview(t *testing.T) {
	root, _ := install(t, admintest.Plugin{Pending: true})
	l := newLane(t, root, nil)
	res, _, err := l.svc.Plugins().Review(owner(), "echo", admin.ReviewRequest{Typed: "echo"}, admin.GateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != admin.ReviewMigrate || res.State.ReviewPending {
		t.Fatalf("review %+v", res)
	}
	entry, _ := readEntry(t, root, "echo")
	if entry.BundleDigest != res.Review.BundleDigest || entry.Review == nil || entry.Review.BundleDigest != entry.BundleDigest || entry.AcceptedAt.IsZero() {
		t.Fatalf("entry %+v", entry)
	}
	if _, _, err = l.svc.Plugins().Review(owner(), "echo", admin.ReviewRequest{Typed: "echo"}, admin.GateOpts{}); !errors.Is(err, admin.ErrNothingToReview) {
		t.Fatalf("a second review: %v", err)
	}
	if _, _, err = l.svc.Plugins().Review(owner(), "nope", admin.ReviewRequest{Typed: "nope"}, admin.GateOpts{}); err == nil {
		t.Fatal("a review of a plugin that is not installed")
	}
}
