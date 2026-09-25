package pluginhost

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// valueSentinel contains characters URL escaping rewrites, so a test can tell
// the raw form from the escaped one a transport error echoes back.
const valueSentinel = "SENTINEL/plugin+secret=7f3a"

// leakyProcess is a plugin that puts the credential it was handed into every
// piece of text it returns: the case the host's value redactor exists for.
type leakyProcess struct {
	token       string
	initErr     bool
	loadErr     bool
	callErr     bool
	callIsError bool
	healthErr   bool
}

func (p *leakyProcess) leak() string {
	return "upstream said " + p.token + " at https://api.example.test/" + url.PathEscape(p.token) + "?key=" + url.QueryEscape(p.token)
}

func (p *leakyProcess) Init(_ context.Context, params SDKInitParams) (SDKInitResult, error) {
	p.token = params.Config["token"]
	if p.initErr {
		return SDKInitResult{}, errors.New(p.leak())
	}
	return SDKInitResult{ID: "contextforge", Version: "dev", Protocol: SDKProtocolVersion}, nil
}
func (p *leakyProcess) Load(context.Context) (SDKLoadResult, error) {
	if p.loadErr {
		return SDKLoadResult{}, errors.New(p.leak())
	}
	return SDKLoadResult{}, nil
}
func (p *leakyProcess) Unload(context.Context) error { return nil }
func (p *leakyProcess) Health(context.Context) (SDKHealthResult, error) {
	if p.healthErr {
		return SDKHealthResult{}, errors.New(p.leak())
	}
	return SDKHealthResult{OK: false, Message: p.leak()}, nil
}
func (p *leakyProcess) CallTool(context.Context, SDKMCPCallRequest) (SDKMCPCallResult, error) {
	if p.callErr {
		return SDKMCPCallResult{}, errors.New(p.leak())
	}
	if p.callIsError {
		return SDKMCPCallResult{IsError: true, Content: []byte(p.leak())}, nil
	}
	return SDKMCPCallResult{Content: []byte(`"ok"`)}, nil
}
func (p *leakyProcess) Close() error { return nil }

func assertNoSentinel(t *testing.T, where, text string) {
	t.Helper()
	for _, form := range []string{valueSentinel, url.QueryEscape(valueSentinel), url.PathEscape(valueSentinel)} {
		if strings.Contains(text, form) {
			t.Fatalf("%s leaked the resolved credential (%q):\n%s", where, form, text)
		}
	}
	if !strings.Contains(text, "upstream said") {
		t.Fatalf("%s lost the plugin's message instead of redacting inside it:\n%s", where, text)
	}
}

func leakyManager(t *testing.T, process *leakyProcess) *Manager {
	t.Helper()
	resolver := &fakeResolver{values: map[string]string{"contextforge/token": valueSentinel}}
	manager := NewManager(nil, fakeLauncher{process: process}, "test", WithSecretResolver(resolver))
	manager.RegisterInstalled(secretDeclaringPlugin())
	return manager
}

func TestManagerRedactsResolvedValuesFromPluginText(t *testing.T) {
	ctx := context.Background()

	t.Run("operation error", func(t *testing.T) {
		manager := leakyManager(t, &leakyProcess{callErr: true})
		if err := manager.Load(ctx, "contextforge"); err != nil {
			t.Fatalf("Load: %v", err)
		}
		_, err := manager.ExecuteOperation(ctx, OperationArgs{Connector: "contextforge", Operation: "list_gateways"})
		if err == nil {
			t.Fatal("ExecuteOperation succeeded, want the plugin's error")
		}
		assertNoSentinel(t, "operation error", err.Error())
	})

	t.Run("tool error result", func(t *testing.T) {
		manager := leakyManager(t, &leakyProcess{callIsError: true})
		if err := manager.Load(ctx, "contextforge"); err != nil {
			t.Fatalf("Load: %v", err)
		}
		_, err := manager.ExecuteOperation(ctx, OperationArgs{Connector: "contextforge", Operation: "list_gateways"})
		if err == nil {
			t.Fatal("ExecuteOperation succeeded, want the tool error")
		}
		assertNoSentinel(t, "tool error result", err.Error())
	})

	t.Run("health message", func(t *testing.T) {
		manager := leakyManager(t, &leakyProcess{})
		if err := manager.Load(ctx, "contextforge"); err != nil {
			t.Fatalf("Load: %v", err)
		}
		health, err := manager.Health(ctx, "contextforge")
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		assertNoSentinel(t, "health message", health.Message)
	})

	t.Run("health error", func(t *testing.T) {
		manager := leakyManager(t, &leakyProcess{healthErr: true})
		if err := manager.Load(ctx, "contextforge"); err != nil {
			t.Fatalf("Load: %v", err)
		}
		_, err := manager.Health(ctx, "contextforge")
		if err == nil {
			t.Fatal("Health succeeded, want the plugin's error")
		}
		assertNoSentinel(t, "health error", err.Error())
	})

	t.Run("init error", func(t *testing.T) {
		err := leakyManager(t, &leakyProcess{initErr: true}).Load(ctx, "contextforge")
		if err == nil {
			t.Fatal("Load succeeded, want the init error")
		}
		assertNoSentinel(t, "init error", err.Error())
	})

	t.Run("load error", func(t *testing.T) {
		err := leakyManager(t, &leakyProcess{loadErr: true}).Load(ctx, "contextforge")
		if err == nil {
			t.Fatal("Load succeeded, want the load error")
		}
		assertNoSentinel(t, "load error", err.Error())
	})
}

// Redaction must not cost the caller the error's identity: the admin lane
// classifies a plugin failure with errors.Is/As through these wrappers.
func TestManagerValueRedactionKeepsErrorChain(t *testing.T) {
	sentinelErr := errors.New("upstream said " + valueSentinel)
	resolver := &fakeResolver{values: map[string]string{"contextforge/token": valueSentinel}}
	manager := NewManager(nil, fakeLauncher{process: &recordingProcess{callErr: sentinelErr}}, "test", WithSecretResolver(resolver))
	manager.RegisterInstalled(secretDeclaringPlugin())
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "contextforge", Operation: "list_gateways"})
	if !errors.Is(err, sentinelErr) {
		t.Fatalf("errors.Is lost the plugin's error through redaction: %v", err)
	}
	assertNoSentinel(t, "wrapped error", err.Error())
}

// scriptedProcess fails every operation with a fixed message, so a test can
// hold the exact text the plugin sent against what the caller received.
type scriptedProcess struct {
	recordingProcess
	message string
}

func (p *scriptedProcess) CallTool(context.Context, SDKMCPCallRequest) (SDKMCPCallResult, error) {
	return SDKMCPCallResult{}, errors.New(p.message)
}

func scriptedFailure(t *testing.T, token, message string) (string, []string) {
	t.Helper()
	resolver := &fakeResolver{values: map[string]string{"contextforge/token": token}}
	var warnings []string
	manager := NewManager(nil, fakeLauncher{process: &scriptedProcess{message: message}}, "test",
		WithSecretResolver(resolver),
		WithLoadWarning(func(line string) { warnings = append(warnings, line) }))
	manager.RegisterInstalled(secretDeclaringPlugin())
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "contextforge", Operation: "list_gateways"})
	if err == nil {
		t.Fatal("ExecuteOperation succeeded, want the scripted failure")
	}
	return err.Error(), warnings
}

const recoveryInstruction = "reload it with `cerberus connectors plugin managed load contextforge` once the region is back"

// A short resolved value is common text — a region, an octet, "true". Redacting
// it everywhere would shred the message around it, so it is left alone, and
// the operator is told, by name, that it is not covered.
func TestPluginRedactorSkipsShortValues(t *testing.T) {
	const short = "nyc3"
	message := "region nyc3 is full; " + recoveryInstruction
	got, warnings := scriptedFailure(t, short, message)
	if !strings.Contains(got, message) {
		t.Fatalf("a short resolved value mangled the plugin's message:\n got: %s\nwant it to contain: %s", got, message)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, `plugin "contextforge" credential token is shorter than`) {
		t.Fatalf("warnings = %q, want one naming the unprotected secret", warnings)
	}
	if strings.Contains(joined, short) {
		t.Fatalf("the warning carries the value it is about: %q", warnings)
	}
}

// A redacted credential takes only itself out of the message: the recovery
// instruction next to it arrives intact.
func TestPluginRedactorKeepsRecoveryInstruction(t *testing.T) {
	message := "upstream said " + valueSentinel + " as " + url.QueryEscape(valueSentinel) + "; " + recoveryInstruction
	got, warnings := scriptedFailure(t, valueSentinel, message)
	assertNoSentinel(t, "scripted failure", got)
	if !strings.Contains(got, recoveryInstruction) {
		t.Fatalf("redaction ate the recovery instruction:\n%s", got)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %q, want none for a full-length credential", warnings)
	}
}

// The escaped forms are only searched for when they differ from the raw one.
func TestPluginRedactorDeduplicatesEscapedForms(t *testing.T) {
	plain := resolvedSecrets{Config: map[string]string{"token": "plainvalue123"}}
	escaped := resolvedSecrets{Config: map[string]string{"token": valueSentinel}}
	if _, unprotected := plain.redactor(); len(unprotected) != 0 {
		t.Fatalf("unprotected = %v, want none", unprotected)
	}
	r, _ := escaped.redactor()
	text := strings.Join([]string{valueSentinel, url.QueryEscape(valueSentinel), url.PathEscape(valueSentinel)}, " | ")
	if got := r.Text(text); strings.Contains(got, "SENTINEL") {
		t.Fatalf("an escaped form survived: %s", got)
	}
}
