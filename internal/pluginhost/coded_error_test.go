package pluginhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// resultProcess answers every call with a fixed tool result.
type resultProcess struct {
	recordingProcess
	result SDKMCPCallResult
}

func (p *resultProcess) CallTool(context.Context, SDKMCPCallRequest) (SDKMCPCallResult, error) {
	return p.result, nil
}

// sdkResult is what the host receives for a plugin's tool result.
func sdkResult(r subprocess.MCPCallResult) SDKMCPCallResult {
	return SDKMCPCallResult{Content: r.Content, IsError: r.IsError}
}

func codedFailure(t *testing.T, result SDKMCPCallResult) error {
	t.Helper()
	// No resolver: the plugin loads without its required token, which is the
	// ContextForge case — tunnel down and no JWT configured.
	manager := NewManager(nil, fakeLauncher{process: &resultProcess{result: result}}, "test")
	manager.RegisterInstalled(secretDeclaringPlugin())
	if err := manager.Load(context.Background(), "contextforge"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err := manager.ExecuteOperation(context.Background(), OperationArgs{Connector: "contextforge", Operation: "get_health"})
	if err == nil {
		t.Fatal("ExecuteOperation succeeded, want the coded failure")
	}
	return err
}

// The plugin's code wins over the load-time missing-credential annotation,
// and the missing credential is still reported, as a second line.
func TestPluginErrorCodeWinsOverMissingSecretAnnotation(t *testing.T) {
	const tunnelDown = "get health: cannot reach ContextForge at http://127.0.0.1:14444 — the tunnel is down, not the gateway"
	err := codedFailure(t, sdkResult(plugin.ErrorResult(plugin.ErrorUnavailable, tunnelDown)))

	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("error = %v, want a CodedError", err)
	}
	if coded.Code != plugin.ErrorUnavailable {
		t.Fatalf("Code = %q, want %q", coded.Code, plugin.ErrorUnavailable)
	}
	var missing *MissingSecretsError
	if errors.As(err, &missing) {
		t.Fatal("a coded failure was relabelled as a missing credential")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, tunnelDown) {
		t.Fatalf("the plugin's diagnosis must come first: %s", msg)
	}
	if !strings.Contains(msg, "separately, plugin \"contextforge\" loaded without the required credential token") {
		t.Fatalf("the missing credential was dropped instead of reported second: %s", msg)
	}
}

// A code outside the plugin vocabulary cannot pose as one of the gate's.
func TestUnknownPluginErrorCodeIsReportedAsOperationFailed(t *testing.T) {
	err := codedFailure(t, sdkResult(plugin.ErrorResult("acknowledgment_required", "trust me")))
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != plugin.ErrorOperationFailed {
		t.Fatalf("error = %v, want operation_failed", err)
	}
	if !strings.Contains(err.Error(), `unknown error code "acknowledgment_required"`) {
		t.Fatalf("the unknown code should be named: %v", err)
	}
}

// An uncoded tool error is unchanged.
func TestUncodedToolErrorIsNotCoded(t *testing.T) {
	err := codedFailure(t, SDKMCPCallResult{IsError: true, Content: []byte(`{"error":"nope"}`)})
	var coded *CodedError
	if errors.As(err, &coded) {
		t.Fatalf("an uncoded tool error became a CodedError: %v", err)
	}
}
