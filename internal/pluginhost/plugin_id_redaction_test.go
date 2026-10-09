package pluginhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// onepasswordPlugin is a plugin whose id contains a credential word, the
// shape that made redact.Text eat the word after "onepassword": in the
// host's own messages.
func onepasswordPlugin() InstalledPlugin {
	p := validInstalledPlugin()
	p.ID = "onepassword"
	p.Manifest.ID = "onepassword"
	return p
}

// Every generic message the host composes about a plugin survives
// redaction intact when the plugin's id contains "password": through
// Error(), through redact.Text over that text, and through the scope's
// render.
func TestGenericPluginMessagesSurviveAnIDContainingPassword(t *testing.T) {
	ctx := context.Background()
	notLoaded := newTestManager(t, nil, fakeLauncher{}, "test")
	notLoaded.RegisterInstalled(onepasswordPlugin())

	process := &recordingProcess{callErr: errors.New("upstream refused the request")}
	loaded := newTestManager(t, nil, fakeLauncher{process: process}, "test")
	loaded.RegisterInstalled(onepasswordPlugin())
	if err := loaded.Load(ctx, "onepassword"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		m    *Manager
		args OperationArgs
		want string
	}{
		{"not loaded", notLoaded, OperationArgs{Connector: "onepassword", Operation: "logs"}, `plugin "onepassword" is not loaded`},
		{"undeclared", loaded, OperationArgs{Connector: "onepassword", Operation: "rotate"}, `plugin "onepassword" does not declare operation "rotate"`},
		{"invalid input", loaded, OperationArgs{Connector: "onepassword", Operation: "logs", Config: map[string]any{"bogus": "x"}}, `plugin "onepassword" operation "logs": `},
		{"no preview", loaded, OperationArgs{Connector: "onepassword", Operation: "logs", Config: map[string]any{"container": "web"}, DryRun: true}, `plugin "onepassword" operation "logs": preview_unsupported: the manifest does not declare supports_dry`},
		{"tool error", loaded, OperationArgs{Connector: "onepassword", Operation: "logs", Config: map[string]any{"container": "web"}}, `plugin "onepassword" tool "logs": upstream refused the request`},
	}
	for _, tc := range cases {
		_, err := tc.m.ExecuteOperation(ctx, tc.args)
		if err == nil {
			t.Fatalf("%s: no error", tc.name)
		}
		text := err.Error()
		if !strings.Contains(text, tc.want) {
			t.Errorf("%s: %q, want it to contain %q", tc.name, text, tc.want)
		}
		if got := redact.Text(text); got != text {
			t.Errorf("%s: redact.Text changed it:\n  %s\n  %s", tc.name, text, got)
		}
		if got := redact.Render(redact.NewScope(), err); got != text {
			t.Errorf("%s: the scope's render changed it:\n  %s\n  %s", tc.name, text, got)
		}
	}
	// Text that puts the id straight before a colon, as GuidanceWrap's
	// "plugin %q: <cause>" does and as log lines and wrapped errors do,
	// survives because the id is registered as a name once installed.
	for _, text := range []string{
		`plugin "onepassword": capability "ssh_agent" is not one this host grants`,
		`onepassword: the plugin stopped and Cerberus is restarting it`,
	} {
		if got := redact.Text(text); got != text {
			t.Errorf("an installed plugin's id is read as a credential key:\n  %s\n  %s", text, got)
		}
	}

	// The sentinels still classify.
	if _, err := notLoaded.ExecuteOperation(ctx, OperationArgs{Connector: "onepassword", Operation: "logs"}); !errors.Is(err, ErrNotLoaded) {
		t.Errorf("not loaded lost ErrNotLoaded: %v", err)
	}
	if _, err := loaded.ExecuteOperation(ctx, OperationArgs{Connector: "onepassword", Operation: "rotate"}); !errors.Is(err, ErrOperationUndeclared) {
		t.Errorf("undeclared lost ErrOperationUndeclared: %v", err)
	}
}
