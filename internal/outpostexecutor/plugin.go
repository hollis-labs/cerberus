// Package outpostexecutor implements fixed native operations, without a policy
// engine, credential store, arbitrary shell endpoint, or controller dependency.
package outpostexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/pkg/outpost"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

type Plugin struct {
	Version   string
	Protected []string
	Run       func(context.Context, string, []string) (outpost.Result, error)
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	p.Protected = strings.Split(params.Config["protected_units"], ",")
	if p.Run == nil {
		p.Run = run
	}
	return subprocess.InitResult{ID: "cerberus", Name: "Cerberus thin executor", Version: p.Version, Protocol: subprocess.ProtocolVersion, CapabilityContract: capability.ContractVersion}, nil
}
func (*Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (*Plugin) Unload(context.Context) error { return nil }
func (*Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	return subprocess.HealthStatus{OK: true}, nil
}

func (p *Plugin) HTTPHandle(ctx context.Context, req subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	if req.Method != "POST" || req.Path != "/api/plugins/cerberus/execute" {
		return subprocess.HTTPResponse{Status: 404}, nil
	}
	var request outpost.Request
	d := json.NewDecoder(bytes.NewReader(req.Body))
	d.DisallowUnknownFields()
	if err := d.Decode(&request); err != nil {
		return subprocess.HTTPResponse{Status: 400}, nil //nolint:nilerr // HTTP error response is the protocol result.
	}
	result, err := p.Execute(ctx, request)
	if err != nil {
		return subprocess.HTTPResponse{Status: 400, Body: []byte("operation refused")}, nil //nolint:nilerr // Refusal is represented by HTTP status.
	}
	b, err := json.Marshal(result)
	return subprocess.HTTPResponse{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: b}, err
}

func (p *Plugin) Execute(ctx context.Context, r outpost.Request) (outpost.Result, error) {
	if err := r.Validate(); err != nil {
		return outpost.Result{}, err
	}
	if r.Verb == "start" || r.Verb == "stop" || r.Verb == "restart" {
		for _, unit := range append([]string{"outpost.service", "tether-outpost.service", "dev.hollis-labs.outpost", "com.hollis-labs.cerberus", "com.hollis-labs.cerberus.service"}, p.Protected...) {
			if r.Unit == unit || (runtime.GOOS == "linux" && r.Unit+".service" == unit) {
				return outpost.Result{}, fmt.Errorf("cannot mutate the request's control plane")
			}
		}
	}
	if runtime.GOOS == "darwin" && r.Verb == "logs" {
		return launchdLogs(r)
	}
	command, args, err := Command(runtime.GOOS, os.Getuid(), r)
	if err != nil {
		return outpost.Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return p.Run(ctx, command, args)
}

func Command(platform string, uid int, r outpost.Request) (string, []string, error) {
	if err := r.Validate(); err != nil {
		return "", nil, err
	}
	if r.Verb == "probe" {
		return "/bin/sh", []string{"-c", hosts.PosixProbeCommand}, nil
	}
	if platform == "linux" {
		switch r.Verb {
		case "units":
			return "systemctl", []string{"--user", "list-units", "--type=service", "--all", "--no-pager", "--plain"}, nil
		case "logs":
			lines := r.Lines
			if lines == 0 {
				lines = 100
			}
			return "journalctl", []string{"--user", "--no-pager", "-n", strconv.Itoa(lines), "--unit", r.Unit}, nil
		default:
			return "systemctl", []string{"--user", r.Verb, "--", r.Unit}, nil
		}
	}
	if platform == "darwin" {
		target := "gui/" + strconv.Itoa(uid) + "/" + r.Unit
		switch r.Verb {
		case "units":
			return "launchctl", []string{"list"}, nil
		case "start":
			home, err := os.UserHomeDir()
			if err != nil {
				return "", nil, err
			}
			return "launchctl", []string{"bootstrap", "gui/" + strconv.Itoa(uid), filepath.Join(home, "Library", "LaunchAgents", r.Unit+".plist")}, nil
		case "restart":
			return "launchctl", []string{"kickstart", "-k", target}, nil
		case "stop":
			return "launchctl", []string{"bootout", target}, nil
		case "logs":
			return "", nil, fmt.Errorf("launchd unit log paths must be supplied by a future explicit log-source contract")
		}
	}
	return "", nil, fmt.Errorf("unsupported platform/verb")
}

type boundedOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	room := (256 << 10) - b.Len()
	if n > room {
		p = p[:room]
		b.truncated = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func run(ctx context.Context, command string, args []string) (outpost.Result, error) {
	cmd := exec.CommandContext(ctx, command, args...) // #nosec G204 -- fixed native commands and validated arguments from Command.
	cmd.WaitDelay = 2 * time.Second
	b := &boundedOutput{}
	cmd.Stdout = b
	cmd.Stderr = b
	err := cmd.Run()
	result := outpost.Result{Output: b.String(), Truncated: b.truncated}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		} else {
			return result, fmt.Errorf("native executor unavailable")
		}
	}
	return result, nil
}
