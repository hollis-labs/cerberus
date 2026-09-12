package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secretref"
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
	// selfPath resolves the Cerberus executable used to front a service whose
	// environment carries secret references. Nil falls back to os.Executable
	// with a PATH lookup behind it; tests override it.
	selfPath     func() (string, error)
	startTimeout time.Duration
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

type launchdRecord struct {
	Loaded       bool
	State        string
	PID          int
	LastExitCode *int
	Throttled    bool
	Reason       string
	Diagnosis    string
	Highlights   []string
	Raw          string
}

// LaunchdRecord is the exported read-only launchd inspection view.
type LaunchdRecord = launchdRecord

func (b launchdBackend) Start(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	_, err := b.Apply(ctx, res, spec)
	return err
}

func (b launchdBackend) Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec) (ApplyResult, error) {
	layout, plistPath, label, artifactChanged, plistChanged, err := b.writePlist(res, spec)
	if err != nil {
		return ApplyResult{}, err
	}

	domainTarget := b.domainTarget()
	serviceTarget := b.serviceTarget(label)
	loaded, state, err := b.loadedState(ctx, label)
	if err != nil {
		return ApplyResult{}, err
	}

	activationPending := false
	if spec.RunFrom == ProcessRunFromArtifact {
		_, art, statusErr := b.artifactInstaller().Status(res, spec)
		if statusErr != nil {
			return ApplyResult{}, statusErr
		}
		activationPending = art.ActivationPending
	}
	needsReload := !loaded || artifactChanged || plistChanged || activationPending
	if needsReload && loaded {
		if out, err := b.runner.CombinedOutput(ctx, "launchctl", "bootout", serviceTarget); err != nil && !isLaunchdNotFound(string(out), err) {
			return ApplyResult{}, fmt.Errorf("launchctl bootout %s: %w%s", label, err, formatLaunchdFailureDetails(out, layout))
		}
		if err := waitLaunchdSlot(ctx); err != nil {
			return ApplyResult{}, err
		}
	}
	if needsReload {
		if out, err := b.bootstrapService(ctx, domainTarget, serviceTarget, plistPath); err != nil {
			return ApplyResult{}, fmt.Errorf("launchctl bootstrap %s failed; service may be stopped, retry cerberus resource apply %s: %w%s", label, res.ID, err, formatLaunchdFailureDetails(out, layout))
		}
	}
	if !needsReload && (state == domain.StateRunning || state == domain.StateStarting) {
		if state == domain.StateStarting {
			if err := b.waitRunning(ctx, res, spec, layout); err != nil {
				return ApplyResult{}, err
			}
		}
		return ApplyResult{
			Action:          ApplyActionNoop,
			ArtifactChanged: artifactChanged,
			PlistChanged:    plistChanged,
		}, nil
	}
	if out, err := b.runner.CombinedOutput(ctx, "launchctl", "kickstart", "-k", serviceTarget); err != nil {
		return ApplyResult{}, fmt.Errorf("launchctl kickstart %s: %w%s", label, err, formatLaunchdFailureDetails(out, layout))
	}
	if err := b.waitRunning(ctx, res, spec, layout); err != nil {
		return ApplyResult{}, err
	}
	if err := recordArtifactActivation(layout, spec); err != nil {
		return ApplyResult{}, fmt.Errorf("record artifact activation: %w", err)
	}
	action := ApplyActionRestarted
	if !loaded {
		action = ApplyActionStarted
	} else if artifactChanged || plistChanged || activationPending {
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
	rec, err := b.Inspect(ctx, res, spec)
	if err != nil {
		return domain.StateUnknown, err
	}
	if !rec.Loaded {
		return domain.StateStopped, nil
	}
	return parseLaunchdState(rec.Raw), nil
}

func (b launchdBackend) Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	label, err := b.serviceName(res, spec)
	if err != nil {
		return err
	}
	loaded, _, err := b.loadedState(ctx, label)
	if err != nil {
		return err
	}
	if !loaded {
		return fmt.Errorf("launchd service %q is not loaded; use resource apply", label)
	}
	target := b.serviceTarget(label)
	if _, err := b.runner.CombinedOutput(ctx, "launchctl", "kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w", label, err)
	}
	layout, err := defaultInstallLayoutFromBackend(b, res, spec)
	if err != nil {
		return err
	}
	if err := b.waitRunning(ctx, res, spec, layout); err != nil {
		return err
	}
	return recordArtifactActivation(layout, spec)
}

// Command acceptance does not mean launchd could execute the binary. Require
// a running PID across successive observations, and keep failure diagnostics.
func (b launchdBackend) waitRunning(ctx context.Context, res *domain.Resource, spec ProcessSpec, layout InstallLayout) error {
	timeout := b.startTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last launchdRecord
	interrupted := func(cause error) error {
		// The wait only bounds confirmation. KeepAlive may launch the new
		// artifact after it ends, so callers must inspect before retrying.
		return fmt.Errorf("launchd service %q did not reach running: %w%s; startup was not confirmed and launchd may still retry; check `cerberus resource status %s` before retrying the operation", layout.ServiceName, cause, formatLaunchdFailureDetails([]byte(last.Raw), layout), res.ID)
	}
	previousPID := 0
	for {
		if err := ctx.Err(); err != nil {
			return interrupted(err)
		}
		rec, err := b.Inspect(ctx, res, spec)
		if err != nil {
			if cause := ctx.Err(); cause != nil {
				return interrupted(cause)
			}
			return err
		}
		last = rec
		if rec.Loaded && rec.State == "running" && rec.PID > 0 {
			if rec.PID == previousPID {
				return nil
			}
			previousPID = rec.PID
		} else {
			previousPID = 0
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (b launchdBackend) Inspect(ctx context.Context, res *domain.Resource, spec ProcessSpec) (launchdRecord, error) {
	label, err := b.serviceName(res, spec)
	if err != nil {
		return launchdRecord{}, err
	}
	target := b.serviceTarget(label)
	out, err := b.runner.CombinedOutput(ctx, "launchctl", "print", target)
	if err != nil {
		if isLaunchdNotFound(string(out), err) {
			return launchdRecord{Loaded: false, Raw: string(out)}, nil
		}
		return launchdRecord{}, fmt.Errorf("launchctl print %s: %w", label, err)
	}
	text := OutputRedactor(spec).Text(redact.Launchd(string(out)))
	diagnosis, highlights := diagnoseLaunchdRecord(text)
	return launchdRecord{
		Loaded:       true,
		State:        extractLaunchdValue(text, "state ="),
		PID:          extractLaunchdInt(text, "pid ="),
		LastExitCode: extractLaunchdOptionalInt(text, "last exit code ="),
		Throttled:    strings.Contains(text, "state = throttled"),
		Reason:       extractLaunchdValue(text, "reason ="),
		Diagnosis:    diagnosis,
		Highlights:   highlights,
		Raw:          redactLaunchdRecordSecrets(text),
	}, nil
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

func (b launchdBackend) writePlist(res *domain.Resource, spec ProcessSpec) (InstallLayout, string, string, bool, bool, error) {
	layout, syncRes, err := b.artifactInstaller().Sync(res, spec)
	if err != nil {
		return InstallLayout{}, "", "", false, false, err
	}
	artifactChanged := syncRes.Performed && syncRes.Changed
	programArgs, err := launchdProgramArguments(layout, spec)
	if err != nil {
		return InstallLayout{}, "", "", false, false, err
	}

	logDir := filepath.Join(layout.RootDir, "logs")
	if mkErr := os.MkdirAll(logDir, 0755); mkErr != nil { //nolint:gosec
		return InstallLayout{}, "", "", false, false, fmt.Errorf("create log dir: %w", mkErr)
	}
	if mkErr := os.MkdirAll(filepath.Dir(layout.PlistPath), 0755); mkErr != nil { //nolint:gosec
		return InstallLayout{}, "", "", false, false, fmt.Errorf("create launch agents dir: %w", mkErr)
	}

	data := plistTemplateData{
		Label:             layout.ServiceName,
		ProgramArguments:  programArgs,
		WorkingDirectory:  layout.WorkingDir,
		StandardOutPath:   filepath.Join(logDir, "stdout.log"),
		StandardErrorPath: filepath.Join(logDir, "stderr.log"),
	}
	data.Environment, data.EnvironmentEntries = launchdEnvironment(spec)

	// A service whose environment carries secret references is fronted by
	// `cerberus run-secrets`, which resolves them in the service's own process.
	// The plist keeps the references; the credentials never reach disk.
	if secretref.EnvHasRefs(data.Environment) {
		shim, shimErr := b.cerberusPath()
		if shimErr != nil {
			return InstallLayout{}, "", "", false, false, fmt.Errorf("resource %q uses secret references but the cerberus executable could not be located to front it: %w", res.ID, shimErr)
		}
		data.ProgramArguments = append([]string{shim, "run-secrets", "--"}, data.ProgramArguments...)
	}

	rendered, err := renderLaunchdPlist(data)
	if err != nil {
		return InstallLayout{}, "", "", false, false, err
	}
	plistChanged, err := writeFileIfChanged(layout.PlistPath, rendered, 0600)
	if err != nil {
		return InstallLayout{}, "", "", false, false, fmt.Errorf("write plist: %w", err)
	}

	return layout, layout.PlistPath, layout.ServiceName, artifactChanged, plistChanged, nil
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

func (b launchdBackend) bootstrapService(ctx context.Context, domainTarget, serviceTarget, plistPath string) ([]byte, error) {
	out, err := b.runner.CombinedOutput(ctx, "launchctl", "bootstrap", domainTarget, plistPath)
	if err == nil {
		return out, nil
	}
	if !isLaunchdBootstrapConflict(string(out), err) {
		return out, err
	}
	// A conflict can mean a just-removed slot is still draining. Probe the
	// label first: booting out an already-absent job returns a misleading EIO.
	printOut, printErr := b.runner.CombinedOutput(ctx, "launchctl", "print", serviceTarget)
	if printErr == nil {
		if recoveryOut, recoveryErr := b.runner.CombinedOutput(ctx, "launchctl", "bootout", serviceTarget); recoveryErr != nil && !isLaunchdNotFound(string(recoveryOut), recoveryErr) {
			return recoveryOut, fmt.Errorf("recovery bootout failed: %w", recoveryErr)
		}
	} else if !isLaunchdNotFound(string(printOut), printErr) {
		return printOut, fmt.Errorf("inspect bootstrap conflict: %w", printErr)
	}
	if err := waitLaunchdSlot(ctx); err != nil {
		return out, err
	}
	return b.runner.CombinedOutput(ctx, "launchctl", "bootstrap", domainTarget, plistPath)
}

func waitLaunchdSlot(ctx context.Context) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

func formatLaunchdFailureDetails(out []byte, layout InstallLayout) string {
	parts := make([]string, 0, 6)
	if text := strings.TrimSpace(redact.Text(string(out))); text != "" {
		parts = append(parts, "launchd output: "+text)
	}
	parts = append(parts,
		"stderr log: "+filepath.Join(layout.RootDir, "logs", "stderr.log"),
		"stdout log: "+filepath.Join(layout.RootDir, "logs", "stdout.log"),
		"plist: "+layout.PlistPath,
		"install: "+layout.RootDir,
	)
	if layout.ArtifactPath != "" {
		parts = append(parts, "artifact: "+layout.ArtifactPath)
	}
	return "; " + strings.Join(parts, "; ")
}

func launchdEnvironment(spec ProcessSpec) (map[string]string, []plistEnvEntry) {
	env := make(map[string]string)
	if spec.EnvFile != "" {
		envPath := spec.EnvFile
		if !filepath.IsAbs(envPath) {
			envPath = filepath.Join(spec.Dir, envPath)
		}
		for _, entry := range loadEnvFile(envPath) {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || key == "" {
				continue
			}
			env[key] = value
		}
	}
	for key, value := range spec.Env {
		env[key] = value
	}
	if len(env) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]plistEnvEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, plistEnvEntry{Key: key, Value: env[key]})
	}
	return env, entries
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

// cerberusPath resolves the Cerberus executable that fronts services using
// secret references. os.Executable is preferred so a service is pinned to the
// same binary that installed it; a PATH lookup covers callers that exec through
// a wrapper.
func (b launchdBackend) cerberusPath() (string, error) {
	if b.selfPath != nil {
		return b.selfPath()
	}
	if path, err := os.Executable(); err == nil && path != "" {
		return filepath.EvalSymlinks(path)
	}
	return exec.LookPath("cerberus")
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

func isLaunchdBootstrapConflict(out string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(out + " " + err.Error())
	return strings.Contains(text, "input/output error") ||
		strings.Contains(text, "bootstrap failed: 5")
}

func parseLaunchdState(text string) domain.State {
	switch {
	case strings.Contains(text, "state = running"):
		return domain.StateRunning
	case strings.Contains(text, "state = spawn scheduled"),
		strings.Contains(text, "state = spawning"):
		// A scheduled/spawning service that has already exited non-zero is
		// crash-looping: launchd keeps rescheduling a process that fails on
		// launch. Reporting "starting" here makes a permanently broken
		// service look transient — surface it as failed instead.
		if hasNonZeroLaunchdExit(text) {
			return domain.StateFailed
		}
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

func extractLaunchdValue(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func extractLaunchdInt(text, prefix string) int {
	if v := extractLaunchdOptionalInt(text, prefix); v != nil {
		return *v
	}
	return 0
}

func extractLaunchdOptionalInt(text, prefix string) *int {
	value := extractLaunchdValue(text, prefix)
	if value == "" {
		return nil
	}
	var out int
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil {
		return nil
	}
	return &out
}

func diagnoseLaunchdRecord(text string) (string, []string) {
	highlights := make([]string, 0, 5)
	for _, prefix := range []string{"state =", "pid =", "last exit code =", "runs =", "reason ="} {
		if v := extractLaunchdValue(text, prefix); v != "" {
			highlights = append(highlights, strings.TrimSpace(prefix)+" "+v)
		}
	}

	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "state = throttled"):
		return "launchd is throttling restarts after repeated failures", highlights
	case strings.Contains(lower, "reason = crashed"):
		return "process crashed after launch", highlights
	case strings.Contains(lower, "state = waiting") && hasNonZeroLaunchdExit(text):
		return "process exited with a non-zero status", highlights
	case (strings.Contains(lower, "state = spawn scheduled") ||
		strings.Contains(lower, "state = spawning")) && hasNonZeroLaunchdExit(text):
		return "process is crash-looping: it exits non-zero on launch and launchd keeps rescheduling it", highlights
	case strings.Contains(lower, "state = spawn scheduled"):
		return "launchd is waiting to spawn the process", highlights
	case strings.Contains(lower, "state = spawning"):
		return "launchd is spawning the process", highlights
	case strings.Contains(lower, "state = running"):
		return "service is loaded and running", highlights
	case strings.Contains(lower, "state = waiting"):
		return "service is loaded but not running (launchd waiting state)", highlights
	default:
		return "", highlights
	}
}

func redactLaunchdRecordSecrets(text string) string { return redact.Launchd(text) }

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

// InspectLaunchdRecord returns the live launchd record for an os_service resource.
func InspectLaunchdRecord(ctx context.Context, res *domain.Resource, spec ProcessSpec) (LaunchdRecord, error) {
	return newOSServiceBackend().launchdBackend().Inspect(ctx, res, spec)
}
