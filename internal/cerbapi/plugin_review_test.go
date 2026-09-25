package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

func inProcess() context.Context { return WithCallerSurface(context.Background(), SurfaceInProcess) }

// An accepted install copies the bundle that was reviewed into the store,
// writes a reviewed entry, and records the review before anything changed.
func TestInstallReviewStoresTheReviewedBundle(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	source := reviewablePluginDir(t, "docker", nil)
	sink := audit.NewMemory()
	r := NewPluginReviewer(sink, statePath)

	pending, err := r.PrepareInstall(inProcess(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	text := pending.Text()
	for _, want := range []string{"Plugin: docker 1.0.0", "read_sensitive", "free text, may carry secrets", "Secrets:  none", "MCP exposure requested: none", "Gaps", "no host range declared"} {
		if !strings.Contains(text, want) {
			t.Errorf("review is missing %q:\n%s", want, text)
		}
	}
	// The source changes after the review was prepared: what is stored is
	// still what was reviewed.
	if err = os.WriteFile(filepath.Join(source, "bin", "plugin"), []byte("#!/bin/sh\necho swapped\n"), 0o755); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}
	state, err := r.Accept(inProcess(), pending, "docker\n")
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(filepath.Dir(statePath), "plugins")
	if !strings.HasPrefix(state.Path, store) || state.BundleDigest != pending.Review.BundleDigest {
		t.Fatalf("state %+v", state)
	}
	if got, _ := pluginhost.BundleDigest(state.Path); got != pending.Review.BundleDigest {
		t.Fatalf("the stored bundle %s is not the reviewed one %s", got, pending.Review.BundleDigest)
	}
	st, _ := readPluginConnectorState(statePath)
	if len(st.Entries) != 1 || st.Entries[0].reviewPending() || st.Entries[0].Source != source || st.Version != pluginStateVersion {
		t.Fatalf("entry %+v", st)
	}
	recs := sink.Records()
	if len(recs) != 2 || recs[0].Operation != ReviewInstall || recs[0].Effect != "admin" || recs[1].OutcomeCode != audit.OutcomeOK {
		t.Fatalf("records %+v", recs)
	}
	if rv := recs[0].PluginReview; rv == nil || rv.SummarySHA256 != pending.Review.SummaryDigest() || rv.BundleDigest != pending.Review.BundleDigest || len(rv.Gaps) == 0 {
		t.Fatalf("the review record does not name what was accepted: %+v", recs[0].PluginReview)
	}
	if _, err := r.PrepareInstall(inProcess(), state.Path, false); !errors.Is(err, ErrNothingToReview) {
		t.Fatalf("reinstalling the same bundle: %v", err)
	}
}

func TestInstallReviewRefusals(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	store := filepath.Join(filepath.Dir(statePath), "plugins")

	t.Run("a confirmation that does not match installs nothing, and is recorded", func(t *testing.T) {
		sink := audit.NewMemory()
		r := NewPluginReviewer(sink, statePath)
		pending, err := r.PrepareInstall(inProcess(), reviewablePluginDir(t, "docker", nil), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Accept(inProcess(), pending, "yes"); connectorErrorCode(err) != ExternalConnectorAckRequired {
			t.Fatalf("err = %v", err)
		}
		if st, _ := readPluginConnectorState(statePath); len(st.Entries) != 0 {
			t.Fatal("a declined review wrote state")
		}
		if left, _ := filepath.Glob(filepath.Join(store, "*", "*")); len(left) != 0 {
			t.Fatalf("a declined review left bundles: %v", left)
		}
		if left, _ := filepath.Glob(filepath.Join(store, ".staging-*")); len(left) != 0 {
			t.Fatalf("a declined review left staging: %v", left)
		}
		if recs := sink.Records(); len(recs) != 2 || recs[1].Decision != audit.DecisionRefused {
			t.Fatalf("records %+v", recs)
		}
	})
	t.Run("only in-process", func(t *testing.T) {
		r := NewPluginReviewer(audit.NewMemory(), statePath)
		for _, surface := range []CallerSurface{SurfaceSocket, SurfaceWeb, SurfaceUnknown} {
			ctx := WithCallerSurface(context.Background(), surface)
			if _, err := r.PrepareInstall(ctx, reviewablePluginDir(t, "docker", nil), false); connectorErrorCode(err) != ExternalConnectorUnsupported {
				t.Errorf("%s: err = %v", surface, err)
			}
		}
	})
	t.Run("an unwritable log installs nothing", func(t *testing.T) {
		r := NewPluginReviewer(audit.Failing{}, statePath)
		pending, err := r.PrepareInstall(inProcess(), reviewablePluginDir(t, "docker", nil), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Accept(inProcess(), pending, "docker"); connectorErrorCode(err) != ExternalConnectorAuditUnavailable {
			t.Fatalf("err = %v", err)
		}
		if st, _ := readPluginConnectorState(statePath); len(st.Entries) != 0 {
			t.Fatal("an unrecorded review wrote state")
		}
	})
	t.Run("reserved id, host range, symlink and bad id", func(t *testing.T) {
		r := NewPluginReviewer(audit.NewMemory(), statePath, "ssh")
		var reserved *pluginhost.ReservedIDError
		if _, err := r.PrepareInstall(inProcess(), reviewablePluginDir(t, "ssh", nil), false); !errors.As(err, &reserved) {
			t.Errorf("reserved: %v", err)
		}
		future := reviewablePluginDir(t, "docker", func(p *pluginhost.PluginYAML) { p.Cerberus.Host.MinContract = pluginsdk.ContractVersion + 1 })
		if _, err := r.PrepareInstall(inProcess(), future, false); err == nil || !strings.Contains(err.Error(), "upgrade Cerberus") {
			t.Errorf("host range: %v", err)
		}
		linked := reviewablePluginDir(t, "docker", nil)
		if err := os.Symlink("/etc/hosts", filepath.Join(linked, "extra")); err != nil {
			t.Fatal(err)
		}
		if _, err := r.PrepareInstall(inProcess(), linked, false); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("symlink: %v", err)
		}
		escape := reviewablePluginDir(t, "Docker..", nil)
		if p, err := r.PrepareInstall(inProcess(), escape, false); err == nil {
			if _, err := r.Accept(inProcess(), p, p.Review.ID); err == nil {
				t.Error("an id that cannot name a store directory was stored")
			}
		}
	})
	t.Run("dev install needs a devmode build", func(t *testing.T) {
		if pluginhost.DevModeEnabled {
			t.Skip("devmode build")
		}
		r := NewPluginReviewer(audit.NewMemory(), statePath)
		if _, err := r.PrepareInstall(inProcess(), reviewablePluginDir(t, "docker", nil), true); err == nil || !strings.Contains(err.Error(), "devmode build") {
			t.Fatalf("err = %v", err)
		}
	})
}

// A plugin whose stored bundle changed is refused at load with
// plugin_changed; re-accepting it on a terminal shows the diff and moves it
// to a store directory named for the new digest.
func TestChangedBundleIsRefusedUntilReaccepted(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	sink := audit.NewMemory()
	state := reviewInstall(t, sink, statePath, reviewablePluginDir(t, "docker", nil))

	managed, err := NewManagedPluginConnectorService(sink, "test", nil, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = managed.Load(context.Background(), "docker"); err != nil {
		t.Fatalf("load of the reviewed bundle: %v", err)
	}
	if list, _ := managed.List(context.Background()); len(list) != 1 || list[0].ReviewPending || list[0].BundleDigest != state.BundleDigest {
		t.Fatalf("list %+v", list)
	}

	// The stored plugin.yaml is edited to widen an operation.
	spec, err := pluginhost.ReadPluginYAML(state.Path)
	if err != nil {
		t.Fatal(err)
	}
	spec.Cerberus.Connector.Operations[0].Effect = contract.EffectExec
	writePluginYAML(t, state.Path, spec)

	// Reload refuses before touching the running plugin.
	if _, err = managed.Reload(context.Background(), "docker"); connectorErrorCode(err) != ExternalConnectorPluginChanged {
		t.Fatalf("reload of a changed bundle: %v", err)
	}
	if !managed.Loaded("docker") {
		t.Fatal("a refused reload stopped the running plugin")
	}
	if _, err = managed.Unload(context.Background(), "docker"); err != nil {
		t.Fatal(err)
	}
	_, err = managed.Load(context.Background(), "docker")
	if connectorErrorCode(err) != ExternalConnectorPluginChanged {
		t.Fatalf("load of a changed bundle: %v", err)
	}
	if got := redact.Text(err.Error()); !strings.Contains(got, "cerberus connectors plugin managed load docker --accept-changes") {
		t.Fatalf("the recovery did not survive redaction: %q", got)
	}
	if managed.Loaded("docker") {
		t.Fatal("a changed bundle loaded")
	}

	r := NewPluginReviewer(sink, statePath)
	pending, err := r.PrepareReview(inProcess(), "docker")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Kind != ReviewAcceptChanges || !strings.Contains(strings.Join(pending.Changes, "\n"), "~ operation logs effect read_sensitive -> exec") {
		t.Fatalf("kind %s, changes %v", pending.Kind, pending.Changes)
	}
	if !strings.Contains(pending.Text(), "Changes since the review accepted") {
		t.Fatalf("text:\n%s", pending.Text())
	}
	accepted, err := r.Accept(inProcess(), pending, "docker")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Path == state.Path {
		t.Fatal("an accepted change kept the store directory named for the old digest")
	}
	if _, err := os.Stat(state.Path); !os.IsNotExist(err) {
		t.Fatal("the superseded store copy is still there")
	}
	if _, err := managed.Reload(context.Background(), "docker"); err != nil {
		t.Fatalf("reload after accepting: %v", err)
	}
	if _, err := managed.Load(context.Background(), "docker"); err != nil {
		t.Fatalf("load after accepting: %v", err)
	}
	_, _ = managed.Unload(context.Background(), "docker")
}

// An upgrade is a re-review: installing a new build of an installed plugin
// shows the diff, and nothing new reaches the daemon until it is accepted.
func TestUpgradeIsAReReview(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	sink := audit.NewMemory()
	first := reviewInstall(t, sink, statePath, reviewablePluginDir(t, "docker", nil))

	next := reviewablePluginDir(t, "docker", func(p *pluginhost.PluginYAML) {
		p.Version = "2.0.0"
		p.Cerberus.Connector.Version = "2.0.0"
		p.Cerberus.Connector.Config.Secrets = []contract.SecretRequirement{{Name: "token", Env: "DOCKER_TOKEN"}}
		p.Cerberus.Connector.Operations = append(p.Cerberus.Connector.Operations, contract.ManifestOperation{
			Name: "restart", Effect: contract.EffectLifecycle, Preview: contract.PreviewServer, Output: contract.OutputStructured,
			InputSchema: contract.ObjectSchema(map[string]any{}),
		})
		p.Cerberus.SuggestedPolicy = []pluginsdk.SuggestedRule{{Operation: "restart", Require: pluginsdk.RequireApprovalForAgents}}
	})
	r := NewPluginReviewer(sink, statePath)
	pending, err := r.PrepareInstall(inProcess(), next, false)
	if err != nil {
		t.Fatal(err)
	}
	changes := strings.Join(pending.Changes, "\n")
	for _, want := range []string{"~ version 1.0.0 -> 2.0.0", "+ operation restart (lifecycle, preview server)", "+ secret token", "+ suggested policy restart: approval_for_agents", "+ gap no telemetry declared for restart"} {
		if !strings.Contains(changes, want) {
			t.Errorf("diff is missing %q:\n%s", want, changes)
		}
	}
	if pending.Kind != ReviewUpgrade || !strings.Contains(pending.Review.Render(), "claimed by the plugin; Cerberus cannot verify") ||
		!strings.Contains(pending.Review.Render(), "shown only; Cerberus does not apply") {
		t.Fatalf("review:\n%s", pending.Text())
	}
	// Until accepted, the state still holds the first review.
	if st, _ := readPluginConnectorState(statePath); st.Entries[0].BundleDigest != first.BundleDigest {
		t.Fatal("an unaccepted upgrade changed state")
	}
	r.Discard(pending)
}

// Entries written before install review load as review_pending and keep
// loading; `review <id>` gives them the whole summary and moves them into
// the store.
func TestPendingEntriesKeepLoadingUntilReviewed(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	checkout := reviewablePluginDir(t, "docker", nil)
	v1 := `{"entries":[{"plugin_dir":` + jsonString(checkout) + `,"options":{},"loaded":true}]}`
	if err := os.WriteFile(statePath, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	managed, err := NewManagedPluginConnectorService(sink, "test", nil, statePath)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := managed.List(context.Background())
	if len(list) != 1 || !list[0].Loaded || !list[0].ReviewPending {
		t.Fatalf("a v1 entry did not keep loading as review_pending: %+v", list)
	}
	// The daemon's own write keeps the entry as it was, with its id.
	if err = managed.persist(); err != nil {
		t.Fatal(err)
	}
	st, _ := readPluginConnectorState(statePath)
	if len(st.Entries) != 1 || st.Entries[0].ID != "docker" || st.Entries[0].PluginDir != checkout || !st.Entries[0].reviewPending() {
		t.Fatalf("state after persist: %+v", st)
	}

	r := NewPluginReviewer(sink, statePath)
	pending, err := r.PrepareReview(inProcess(), "docker")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Kind != ReviewMigrate || pending.Previous != nil || !strings.Contains(pending.Text(), "Operations (1)") {
		t.Fatalf("the migration review is not the whole summary: %s\n%s", pending.Kind, pending.Text())
	}
	accepted, err := r.Accept(inProcess(), pending, "docker")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(accepted.Path, filepath.Join(filepath.Dir(statePath), "plugins")) {
		t.Fatalf("path %s", accepted.Path)
	}
	if _, err := os.Stat(checkout); err != nil {
		t.Fatal("reviewing a pending entry removed the operator's checkout")
	}
	if _, err := managed.Reload(context.Background(), "docker"); err != nil {
		t.Fatal(err)
	}
	list, _ = managed.List(context.Background())
	if len(list) != 1 || !list[0].Loaded || list[0].ReviewPending || list[0].Path != accepted.Path {
		t.Fatalf("after review: %+v", list)
	}
	if recs := sink.Records(); !hasRecord(recs, ReviewMigrate) {
		t.Fatal("the migration review is not recorded")
	}
	if _, err := r.PrepareReview(inProcess(), "docker"); !errors.Is(err, ErrNothingToReview) {
		t.Fatalf("second review: %v", err)
	}
	_, _ = managed.Unload(context.Background(), "docker")
}

// The daemon's writes keep entries it does not know, so an install review
// in a terminal is not overwritten by the daemon's next load or unload.
func TestDaemonPersistKeepsEntriesItDoesNotOwn(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	managed := mustManagedPluginService(t, statePath)
	reviewInstall(t, audit.NewMemory(), statePath, reviewablePluginDir(t, "docker", nil))
	if err := managed.persist(); err != nil {
		t.Fatal(err)
	}
	if st, _ := readPluginConnectorState(statePath); len(st.Entries) != 1 || st.Entries[0].reviewPending() {
		t.Fatalf("the daemon dropped or rewrote a reviewed entry: %+v", st)
	}
}

// A dry run whose preview was accepted at review runs without --ack, and is
// recorded plugin_claimed; the same dry run on a plugin still pending review
// needs --ack.
func TestAcceptedPreviewStandsInForAck(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	withRestart := func(p *pluginhost.PluginYAML) {
		p.Cerberus.Connector.Operations = append(p.Cerberus.Connector.Operations, contract.ManifestOperation{
			Name: "restart", Effect: contract.EffectLifecycle, Preview: contract.PreviewServer, Output: contract.OutputStructured,
			InputSchema: contract.ObjectSchema(map[string]any{}),
		})
	}
	for name, reviewed := range map[string]bool{"reviewed": true, "pending": false} {
		t.Run(name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
			sink := audit.NewMemory()
			dir := reviewablePluginDir(t, "docker", withRestart)
			var managed *ManagedPluginConnectorService
			if reviewed {
				reviewInstall(t, sink, statePath, dir)
				var err error
				if managed, err = NewManagedPluginConnectorService(sink, "test", nil, statePath); err != nil {
					t.Fatal(err)
				}
			} else {
				managed = mustManagedPluginService(t, statePath)
				managed.audit = sink
				if _, err := managed.installForTest(context.Background(), PluginConnectorHealthArgs{PluginDir: dir}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := managed.Load(context.Background(), "docker"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = managed.Unload(context.Background(), "docker") }()
			_, err := managed.Execute(context.Background(), "docker", PluginConnectorExecArgs{Operation: "restart", DryRun: true})
			switch {
			case reviewed && err != nil:
				t.Fatalf("an accepted preview still needed --ack: %v", err)
			case !reviewed && connectorErrorCode(err) != ExternalConnectorAckRequired:
				t.Fatalf("a pending plugin's dry run: %v", err)
			}
			last := sink.Records()[len(sink.Records())-1]
			if last.Preview != audit.PreviewPluginClaimed {
				t.Fatalf("the dry run is not recorded plugin_claimed: %+v", last)
			}
			// A real run still needs --ack.
			if _, err := managed.Execute(context.Background(), "docker", PluginConnectorExecArgs{Operation: "restart"}); connectorErrorCode(err) != ExternalConnectorAckRequired {
				t.Fatalf("the real run: %v", err)
			}
		})
	}
}

func TestSocketInstallRouteIsRetired(t *testing.T) {
	socket := startConnectorSocket(t, NewInProcessClient(WithManagedPluginConnectorService(mustManagedPluginService(t, ""))))
	var out map[string]any
	err := socket.doJSON(context.Background(), "POST", "/plugins/connectors/install", map[string]any{"plugin_dir": t.TempDir()}, &out)
	if err == nil || !strings.Contains(err.Error(), "installing a plugin over the socket is retired") {
		t.Fatalf("err = %v", err)
	}
	if got := redact.Text(PluginInstallRetired); got != PluginInstallRetired {
		t.Fatalf("redaction rewrote the refusal: %q", got)
	}
}

func hasRecord(recs []audit.Record, op string) bool {
	for _, rec := range recs {
		if rec.Operation == op && rec.Kind == audit.KindOutcome && rec.OutcomeCode == audit.OutcomeOK {
			return true
		}
	}
	return false
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
