package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/hollis-labs/substrate/module"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// startAdmin builds the admin lane in supervise mode (nil in observe mode, which starts no
// plugin and writes nothing), over the slice 6 gate: the plugins and their state in the adopted
// root, connector-secrets.yaml in the config dir, cerberus.connectors for the plugins' settings,
// and the credential broker's receipts in the class A store. It registers the plugins and loads
// none; RestorePlugins loads them once the supervision lane has started.
func startAdmin(app *module.App, g *gate.Gate, home string) (*admin.Service, error) {
	st, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, err
	}
	if st.Mode != settings.ModeSupervise || g == nil {
		return nil, nil
	}
	reg := registry.ForRoot(st.AdoptRoot)
	// Plugins start through this binary's __plugin-exec, which sets their rlimits; the path is
	// the running executable, never a PATH lookup.
	shim, _ := pluginhost.ExecutablePath()
	return admin.New(admin.Options{
		Gate:  g,
		Paths: admin.Paths{AdoptRoot: st.AdoptRoot, ConfigDir: configDir(home)},
		Resources: func(id string) (*config.ResourceDef, bool) {
			resolved, rerr := reg.Resolve()
			if rerr != nil {
				return nil, false
			}
			return gate.ConfigResourceLookup(resolved.Config)(id)
		},
		ConnectorConfig: st.Connectors,
		Receipts:        admin.StoreRecorder{Store: storage.FromApp(app).Local},
		HostVersion:     buildVersion(),
		Shim:            shim,
		Stderr:          os.Stderr,
		Logger:          slog.Default(),
	})
}

// buildVersion is this binary's module version, for plugin host ranges.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}

// configDir is the operator's config dir, beside the override file: <XDG_CONFIG_HOME>/cerberus
// (policy/, connector-secrets.yaml).
func configDir(home string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cerberus")
}

// hostProbe is the contract a host probe's credentials resolve under: a read of the host.
var hostProbe = contract.Operation{Name: "probe", Effect: contract.EffectRead,
	Target: contract.TargetDescriptor{Kind: "host", From: []string{"name"}}}

// hostSSH is the admin lane's ssh connector for the host monitor, resolved per probe (the old
// NewHostService): each probe's credentials resolve as a read of its ssh resource, as the old
// probe's did (I9).
func hostSSH(adm *admin.Service, lookup gate.ResourceLookup) func(context.Context) (hostmon.SSH, error) {
	if adm == nil {
		return nil
	}
	return func(ctx context.Context) (hostmon.SSH, error) {
		c, err := adm.SSH(ctx)
		if err != nil {
			return nil, err
		}
		return scopedSSH{c: c, lookup: lookup}, nil
	}
}

// scopedSSH runs a probe's command with its credential scope.
type scopedSSH struct {
	c      *sshconn.Connector
	lookup gate.ResourceLookup
}

func (s scopedSSH) Exec(ctx context.Context, res *resource.Resource, command string) (*sshconn.ExecResult, error) {
	_, resolved := gate.AuditTarget(gate.Spec{Connector: "ssh", Operation: "exec", Op: hostProbe, Known: true,
		Config: map[string]any{"id": res.ID}, Resources: s.lookup})
	ctx = secrets.WithCredentialScope(ctx, secrets.CredentialScope{Access: secrets.AccessFor(hostProbe.Effect, false), Target: resolved})
	return s.c.Exec(ctx, res, command)
}
