package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

const launchdProcessPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
{{- range .ProgramArguments }}
        <string>{{ . }}</string>
{{- end }}
    </array>
    <key>WorkingDirectory</key>
    <string>{{.WorkingDirectory}}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.StandardOutPath}}</string>
    <key>StandardErrorPath</key>
    <string>{{.StandardErrorPath}}</string>
{{- if .Environment }}
    <key>EnvironmentVariables</key>
    <dict>
{{- range .EnvironmentEntries }}
        <key>{{ .Key }}</key>
        <string>{{ .Value }}</string>
{{- end }}
    </dict>
{{- end }}
</dict>
</plist>
`

type commandRunner interface {
	CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec
}

type launchdBackend struct {
	runner  commandRunner
	homeDir func() (string, error)
	uid     func() int
	install artifactInstaller
}

type plistTemplateData struct {
	Label              string
	ProgramArguments   []string
	WorkingDirectory   string
	StandardOutPath    string
	StandardErrorPath  string
	Environment        map[string]string
	EnvironmentEntries []plistEnvEntry
}

type plistEnvEntry struct {
	Key   string
	Value string
}

func (b launchdBackend) Start(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	_, err := b.Apply(ctx, res, spec)
	return err
}

func (b launchdBackend) Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec) (ApplyResult, error) {
	plistPath, label, artifactChanged, plistChanged, err := b.writePlist(res, spec)
	if err != nil {
		return ApplyResult{}, err
	}

	domainTarget := b.domainTarget()
	serviceTarget := b.serviceTarget(label)
	loaded, state, err := b.loadedState(ctx, label)
	if err != nil {
		return ApplyResult{}, err
	}

	needsReload := !loaded || artifactChanged || plistChanged
	if needsReload && loaded {
		_, _ = b.runner.CombinedOutput(ctx, "launchctl", "bootout", serviceTarget)
	}
	if needsReload {
		if _, err := b.runner.CombinedOutput(ctx, "launchctl", "bootstrap", domainTarget, plistPath); err != nil {
			return ApplyResult{}, fmt.Errorf("launchctl bootstrap %s: %w", label, err)
		}
	}
	if !needsReload && (state == domain.StateRunning || state == domain.StateStarting) {
		return ApplyResult{
			Action:          ApplyActionNoop,
			ArtifactChanged: artifactChanged,
			PlistChanged:    plistChanged,
		}, nil
	}
	if _, err := b.runner.CombinedOutput(ctx, "launchctl", "kickstart", "-k", serviceTarget); err != nil {
		return ApplyResult{}, fmt.Errorf("launchctl kickstart %s: %w", label, err)
	}
	action := ApplyActionRestarted
	if !loaded {
		action = ApplyActionStarted
	} else if artifactChanged || plistChanged {
		action = ApplyActionReloaded
	}
	return ApplyResult{
		Action:          action,
		ArtifactChanged: artifactChanged,
		PlistChanged:    plistChanged,
	}, nil
}

func (b launchdBackend) Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	label, err := b.serviceName(res, spec)
	if err != nil {
		return err
	}
	target := b.serviceTarget(label)
	out, err := b.runner.CombinedOutput(ctx, "launchctl", "bootout", target)
	if err != nil && !isLaunchdNotFound(string(out), err) {
		return fmt.Errorf("launchctl bootout %s: %w", label, err)
	}
	return nil
}

func (b launchdBackend) Status(ctx context.Context, res *domain.Resource, spec ProcessSpec) (domain.State, error) {
	label, err := b.serviceName(res, spec)
	if err != nil {
		return domain.StateUnknown, err
	}
	target := b.serviceTarget(label)
	out, err := b.runner.CombinedOutput(ctx, "launchctl", "print", target)
	if err != nil {
		if isLaunchdNotFound(string(out), err) {
			return domain.StateStopped, nil
		}
		return domain.StateUnknown, fmt.Errorf("launchctl print %s: %w", label, err)
	}
	return parseLaunchdState(string(out)), nil
}

func (b launchdBackend) Remove(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	layout, err := defaultInstallLayoutFromBackend(b, res, spec)
	if err != nil {
		return err
	}
	label := layout.ServiceName
	target := b.serviceTarget(label)
	out, bootErr := b.runner.CombinedOutput(ctx, "launchctl", "bootout", target)
	if bootErr != nil && !isLaunchdNotFound(string(out), bootErr) {
		return fmt.Errorf("launchctl bootout %s: %w", label, bootErr)
	}
	if rmErr := os.Remove(layout.PlistPath); rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("remove plist: %w", rmErr)
	}
	if _, rmErr := b.artifactInstaller().Remove(res, spec); rmErr != nil {
		return rmErr
	}
	return nil
}

func (b launchdBackend) writePlist(res *domain.Resource, spec ProcessSpec) (string, string, bool, bool, error) {
	layout, syncRes, err := b.artifactInstaller().Sync(res, spec)
	if err != nil {
		return "", "", false, false, err
	}
	artifactChanged := syncRes.Performed && syncRes.Changed
	programArgs, err := launchdProgramArguments(layout, spec)
	if err != nil {
		return "", "", false, false, err
	}

	logDir := filepath.Join(layout.RootDir, "logs")
	if mkErr := os.MkdirAll(logDir, 0755); mkErr != nil { //nolint:gosec
		return "", "", false, false, fmt.Errorf("create log dir: %w", mkErr)
	}
	if mkErr := os.MkdirAll(filepath.Dir(layout.PlistPath), 0755); mkErr != nil { //nolint:gosec
		return "", "", false, false, fmt.Errorf("create launch agents dir: %w", mkErr)
	}

	data := plistTemplateData{
		Label:             layout.ServiceName,
		ProgramArguments:  programArgs,
		WorkingDirectory:  layout.WorkingDir,
		StandardOutPath:   filepath.Join(logDir, "stdout.log"),
		StandardErrorPath: filepath.Join(logDir, "stderr.log"),
		Environment:       spec.Env,
	}
	for k, v := range spec.Env {
		data.EnvironmentEntries = append(data.EnvironmentEntries, plistEnvEntry{Key: k, Value: v})
	}

	rendered, err := renderLaunchdPlist(data)
	if err != nil {
		return "", "", false, false, err
	}
	plistChanged, err := writeFileIfChanged(layout.PlistPath, rendered, 0600)
	if err != nil {
		return "", "", false, false, fmt.Errorf("write plist: %w", err)
	}

	return layout.PlistPath, layout.ServiceName, artifactChanged, plistChanged, nil
}

func (b launchdBackend) loadedState(ctx context.Context, label string) (bool, domain.State, error) {
	target := b.serviceTarget(label)
	out, err := b.runner.CombinedOutput(ctx, "launchctl", "print", target)
	if err != nil {
		if isLaunchdNotFound(string(out), err) {
			return false, domain.StateStopped, nil
		}
		return false, domain.StateUnknown, fmt.Errorf("launchctl print %s: %w", label, err)
	}
	return true, parseLaunchdState(string(out)), nil
}

func writeFileIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // path is Cerberus-managed output
	if err == nil {
		if bytes.Equal(existing, data) {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return false, err
	}
	return true, nil
}

func (b launchdBackend) serviceName(res *domain.Resource, spec ProcessSpec) (string, error) {
	layout, err := defaultInstallLayoutFromBackend(b, res, spec)
	if err != nil {
		return "", err
	}
	return layout.ServiceName, nil
}

func (b launchdBackend) domainTarget() string {
	return fmt.Sprintf("gui/%d", b.uid())
}

func (b launchdBackend) serviceTarget(label string) string {
	return fmt.Sprintf("%s/%s", b.domainTarget(), label)
}

func renderLaunchdPlist(data plistTemplateData) ([]byte, error) {
	tmpl, err := template.New("launchd-process-plist").Parse(launchdProcessPlistTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse launchd plist template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render launchd plist template: %w", err)
	}
	return buf.Bytes(), nil
}

func launchdProgramArguments(layout InstallLayout, spec ProcessSpec) ([]string, error) {
	if len(spec.Command) == 0 && spec.RunFrom != ProcessRunFromArtifact {
		return nil, errors.New("process command is required")
	}
	switch spec.RunFrom {
	case "", ProcessRunFromWorkspace:
		return append([]string(nil), spec.Command...), nil
	case ProcessRunFromArtifact:
		if len(spec.Command) == 0 {
			return []string{layout.ArtifactPath}, nil
		}
		args := append([]string{layout.ArtifactPath}, spec.Command[1:]...)
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported run_from value %q", spec.RunFrom)
	}
}

func isLaunchdNotFound(out string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(out + " " + err.Error())
	return strings.Contains(text, "could not find service") ||
		strings.Contains(text, "service is disabled") ||
		strings.Contains(text, "no such process")
}

func parseLaunchdState(text string) domain.State {
	switch {
	case strings.Contains(text, "state = running"):
		return domain.StateRunning
	case strings.Contains(text, "state = spawn scheduled"),
		strings.Contains(text, "state = spawning"):
		return domain.StateStarting
	case strings.Contains(text, "state = throttled"):
		return domain.StateFailed
	case strings.Contains(text, "state = waiting") && hasNonZeroLaunchdExit(text):
		return domain.StateFailed
	case strings.Contains(text, "state = waiting"):
		return domain.StateStopped
	default:
		return domain.StateUnknown
	}
}

func hasNonZeroLaunchdExit(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "last exit code =") {
			continue
		}
		parts := strings.Split(line, "=")
		if len(parts) != 2 {
			return false
		}
		return strings.TrimSpace(parts[1]) != "0"
	}
	return false
}

func defaultInstallLayoutFromBackend(b launchdBackend, res *domain.Resource, spec ProcessSpec) (InstallLayout, error) {
	home, err := b.homeDir()
	if err != nil {
		return InstallLayout{}, fmt.Errorf("resolve home dir: %w", err)
	}
	return DefaultInstallLayout(home, res, spec)
}

func (b launchdBackend) artifactInstaller() artifactInstaller {
	out := b.install
	if out.homeDir == nil {
		out.homeDir = b.homeDir
	}
	if out.now == nil {
		out.now = time.Now
	}
	return out
}
