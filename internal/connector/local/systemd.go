package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/launchenv"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secretref"
)

// systemdBackend supervises an os_service resource as a systemd user unit,
// the Linux counterpart of launchdBackend. It keeps the same guarantees:
// the unit is written only when its bytes change, the service is restarted
// only when there is a reason, and startup is confirmed by observing the
// same main PID twice rather than trusting that systemctl accepted the job.
//
// Semantics carried over from the launch agent: KeepAlive is Restart=always
// (with no start limit, so a crash-looping service keeps being retried the
// way launchd keeps respawning one), RunAtLoad is the unit being enabled
// under default.target, and StandardOutPath/StandardErrorPath are
// StandardOutput=append:/StandardError=append: into the same
// ~/.cerberus/apps/<project>/<resource>/logs/ layout.
type systemdBackend struct {
	runner  commandRunner
	homeDir func() (string, error)
	install artifactInstaller
	// lookPath resolves systemctl, journalctl and loginctl on every call; nil
	// is resolveSystemTool. Tests return the bare name.
	lookPath func(string) (string, error)
	// envPath is the PATH a unit's composed PATH starts from; nil is the
	// serving process's own PATH.
	envPath func() string
	// selfPath resolves the Cerberus executable that fronts a service whose
	// environment carries secret references; nil is os.Executable.
	selfPath     func() (string, error)
	startTimeout time.Duration
}

// SystemdRecord is the exported read-only view of a systemd user unit.
type SystemdRecord = systemdRecord

type systemdRecord struct {
	// Loaded is true when the user manager knows the unit (LoadState=loaded).
	Loaded           bool
	LoadState        string
	ActiveState      string
	SubState         string
	UnitFileState    string
	Result           string
	NeedDaemonReload bool
	PID              int
	ExecMainStatus   *int
	Restarts         int
	Diagnosis        string
	Highlights       []string
	// Raw is the redacted `systemctl --user show` output, and Journal a
	// redacted tail of the unit's journal: the manager's own start, stop and
	// failure messages, since the service's output goes to its log files.
	Raw     string
	Journal string
}

// systemdShowProperties are the properties Inspect reads. Environment is left
// out deliberately: a literal credential in a resource's env would otherwise
// be echoed by every inspect.
var systemdShowProperties = []string{
	"Id", "LoadState", "ActiveState", "SubState", "UnitFileState", "Result",
	"NeedDaemonReload", "MainPID", "ExecMainStatus", "ExecMainCode", "NRestarts",
	"FragmentPath", "ActiveEnterTimestamp", "InactiveEnterTimestamp", "StatusText", "ExecStart",
}

// envCommandRunner is execCommandRunner with extra environment: the user-bus
// defaults a `systemctl --user` call needs when the daemon was started
// outside a login session.
type envCommandRunner struct {
	environ func() []string
}

func (r envCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // systemd tools with Cerberus's own unit names
	cmd.Env = r.environ()
	return cmd.CombinedOutput()
}

// systemToolFallbacks are searched after PATH. The daemon's PATH is not the
// operator's (AGENTS.md), so a tool is never assumed to resolve by name, and
// it is resolved per call: a failure at boot is not cached for the daemon's
// lifetime.
var systemToolFallbacks = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/run/current-system/sw/bin"}

func resolveSystemTool(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range systemToolFallbacks {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH or in %s; systemd_user supervision needs systemd's user tools", name, strings.Join(systemToolFallbacks, ", "))
}

func (b systemdBackend) run(ctx context.Context, tool string, args ...string) ([]byte, error) {
	lookPath := b.lookPath
	if lookPath == nil {
		lookPath = resolveSystemTool
	}
	path, err := lookPath(tool)
	if err != nil {
		return nil, err
	}
	return b.runner.CombinedOutput(ctx, path, args...)
}

func (b systemdBackend) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return b.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

func (b systemdBackend) Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec) (ApplyResult, error) {
	layout, artifactChanged, unitChanged, err := b.writeUnit(res, spec)
	if err != nil {
		return ApplyResult{}, err
	}
	before, err := b.show(ctx, layout.UnitName)
	if err != nil {
		return ApplyResult{}, err
	}
	if unitChanged || before.NeedDaemonReload || !before.Loaded {
		if out, err := b.systemctl(ctx, "daemon-reload"); err != nil {
			return ApplyResult{}, fmt.Errorf("systemctl --user daemon-reload: %w%s", err, formatSystemdFailureDetails(out, layout))
		}
	}

	state := systemdState(before)
	if state != domain.StateRunning && state != domain.StateStarting {
		if err := checkServicePortConflict(res, spec); err != nil {
			return ApplyResult{}, err
		}
	}

	if before.UnitFileState != "enabled" {
		if out, err := b.systemctl(ctx, "enable", layout.UnitName); err != nil {
			return ApplyResult{}, fmt.Errorf("systemctl --user enable %s: %w%s", layout.UnitName, err, formatSystemdFailureDetails(out, layout))
		}
	}

	activationPending := false
	if spec.RunFrom == ProcessRunFromArtifact {
		_, art, statusErr := b.artifactInstaller().Status(res, spec)
		if statusErr != nil {
			return ApplyResult{}, statusErr
		}
		activationPending = art.ActivationPending
	}
	active := state == domain.StateRunning || state == domain.StateStarting
	changed := artifactChanged || unitChanged || activationPending
	if active && !changed {
		if state == domain.StateStarting {
			if err := b.waitRunning(ctx, res, spec, layout); err != nil {
				return ApplyResult{}, err
			}
		}
		return ApplyResult{Action: ApplyActionNoop, ArtifactChanged: artifactChanged, PlistChanged: unitChanged}, nil
	}
	// restart starts a stopped unit, so one verb covers a first start too.
	if out, err := b.systemctl(ctx, "restart", layout.UnitName); err != nil {
		return ApplyResult{}, fmt.Errorf("systemctl --user restart %s failed; service may be stopped, retry cerberus resource apply %s --ack: %w%s", layout.UnitName, res.ID, err, formatSystemdFailureDetails(out, layout))
	}
	if err := b.waitRunning(ctx, res, spec, layout); err != nil {
		return ApplyResult{}, err
	}
	if err := recordArtifactActivation(layout, spec); err != nil {
		return ApplyResult{}, fmt.Errorf("record artifact activation: %w", err)
	}
	action := ApplyActionRestarted
	switch {
	case !active:
		action = ApplyActionStarted
	case changed:
		action = ApplyActionReloaded
	}
	return ApplyResult{Action: action, ArtifactChanged: artifactChanged, PlistChanged: unitChanged}, nil
}

// Stop stops the unit and leaves it enabled, as a launchd bootout leaves the
// plist: the service comes back at the next login (or boot, with lingering)
// until it is removed.
func (b systemdBackend) Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	layout, err := b.layout(res, spec)
	if err != nil {
		return err
	}
	if out, err := b.systemctl(ctx, "stop", layout.UnitName); err != nil && !isSystemdNotFound(string(out), err) {
		return fmt.Errorf("systemctl --user stop %s: %w%s", layout.UnitName, err, formatSystemdFailureDetails(out, layout))
	}
	return nil
}

func (b systemdBackend) Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	layout, err := b.layout(res, spec)
	if err != nil {
		return err
	}
	rec, err := b.show(ctx, layout.UnitName)
	if err != nil {
		return err
	}
	if !rec.Loaded {
		return fmt.Errorf("systemd unit %q is not installed; use resource apply", layout.UnitName)
	}
	if out, err := b.systemctl(ctx, "restart", layout.UnitName); err != nil {
		return fmt.Errorf("systemctl --user restart %s: %w%s", layout.UnitName, err, formatSystemdFailureDetails(out, layout))
	}
	if err := b.waitRunning(ctx, res, spec, layout); err != nil {
		return err
	}
	return recordArtifactActivation(layout, spec)
}

// Remove disables and stops the unit, deletes it, and removes the install
// tree, the counterpart of launchdBackend.Remove.
func (b systemdBackend) Remove(ctx context.Context, res *domain.Resource, spec ProcessSpec) error {
	layout, err := b.layout(res, spec)
	if err != nil {
		return err
	}
	if out, err := b.systemctl(ctx, "disable", "--now", layout.UnitName); err != nil && !isSystemdNotFound(string(out), err) {
		return fmt.Errorf("systemctl --user disable --now %s: %w%s", layout.UnitName, err, formatSystemdFailureDetails(out, layout))
	}
	if err := os.Remove(layout.UnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	if out, err := b.systemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w%s", err, formatSystemdFailureDetails(out, layout))
	}
	// A unit that failed stays listed as failed until reset; not-found is fine.
	_, _ = b.systemctl(ctx, "reset-failed", layout.UnitName)
	if _, err := b.artifactInstaller().Remove(res, spec); err != nil {
		return err
	}
	return nil
}

// Status is the unit's state, or an error that says why it could not be read;
// it is never (unknown, nil).
func (b systemdBackend) Status(ctx context.Context, res *domain.Resource, spec ProcessSpec) (domain.State, error) {
	layout, err := b.layout(res, spec)
	if err != nil {
		return domain.StateUnknown, err
	}
	rec, err := b.show(ctx, layout.UnitName)
	if err != nil {
		return domain.StateUnknown, err
	}
	state := systemdState(rec)
	if state == domain.StateUnknown {
		return state, fmt.Errorf("systemd unit %s is in a state Cerberus does not recognize (LoadState=%s ActiveState=%s SubState=%s); run `cerberus resource inspect %s`", layout.UnitName, rec.LoadState, rec.ActiveState, rec.SubState, res.ID)
	}
	return state, nil
}

// Inspect is the unit's `systemctl --user show` record plus a short journal
// tail, redacted.
func (b systemdBackend) Inspect(ctx context.Context, res *domain.Resource, spec ProcessSpec) (systemdRecord, error) {
	layout, err := b.layout(res, spec)
	if err != nil {
		return systemdRecord{}, err
	}
	rec, err := b.show(ctx, layout.UnitName)
	if err != nil {
		return systemdRecord{}, err
	}
	rec.Raw = OutputRedactor(spec).Text(redact.Systemd(rec.Raw))
	if rec.Loaded {
		rec.Journal = b.journalTail(ctx, spec, layout.UnitName)
	}
	return rec, nil
}

func (b systemdBackend) journalTail(ctx context.Context, spec ProcessSpec, unit string) string {
	out, err := b.run(ctx, "journalctl", "--user", "--unit", unit, "--lines", "20", "--no-pager", "--output", "short-iso")
	if err != nil {
		// No persistent user journal, or no permission to read it: the
		// record is still complete without a tail.
		return ""
	}
	return OutputRedactor(spec).Text(redact.Text(strings.TrimSpace(string(out))))
}

func (b systemdBackend) show(ctx context.Context, unit string) (systemdRecord, error) {
	out, err := b.systemctl(ctx, "show", unit, "--property", strings.Join(systemdShowProperties, ","))
	if err != nil {
		return systemdRecord{}, fmt.Errorf("systemctl --user show %s: %w%s", unit, err, systemdOutputDetail(out))
	}
	return parseSystemdShow(string(out)), nil
}

// waitRunning confirms startup the way launchdBackend.waitRunning does: a
// command systemd accepted says nothing about whether the binary could run,
// so it requires active/running with the same main PID on two successive
// observations.
func (b systemdBackend) waitRunning(ctx context.Context, res *domain.Resource, spec ProcessSpec, layout InstallLayout) error {
	timeout := b.startTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last systemdRecord
	interrupted := func(cause error) error {
		// The wait only bounds confirmation. Restart=always may start the new
		// artifact after it ends, so callers must inspect before retrying.
		detailCtx, detailCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer detailCancel()
		details := ""
		if len(last.Highlights) > 0 {
			details += "; systemd state: " + strings.Join(last.Highlights, ", ")
		}
		if journal := b.journalTail(detailCtx, spec, layout.UnitName); journal != "" {
			details += "; journal: " + journal
		}
		return fmt.Errorf("systemd service %q did not reach running: %w%s%s; startup was not confirmed and systemd may still retry; check `cerberus resource status %s` before retrying the operation", layout.UnitName, cause, details, formatSystemdFailureDetails(nil, layout), res.ID)
	}
	previousPID := 0
	for {
		if err := waitCtx.Err(); err != nil {
			return interrupted(err)
		}
		rec, err := b.show(waitCtx, layout.UnitName)
		if err != nil {
			if cause := waitCtx.Err(); cause != nil {
				return interrupted(cause)
			}
			return err
		}
		last = rec
		if rec.ActiveState == "active" && rec.SubState == "running" && rec.PID > 0 {
			if rec.PID == previousPID {
				return nil
			}
			previousPID = rec.PID
		} else {
			previousPID = 0
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// writeUnit syncs the artifact and writes the unit, each only when it changed.
func (b systemdBackend) writeUnit(res *domain.Resource, spec ProcessSpec) (InstallLayout, bool, bool, error) {
	layout, syncRes, err := b.artifactInstaller().Sync(res, spec)
	if err != nil {
		return InstallLayout{}, false, false, err
	}
	artifactChanged := syncRes.Performed && syncRes.Changed
	if mkErr := os.MkdirAll(filepath.Join(layout.RootDir, "logs"), 0o755); mkErr != nil { //nolint:gosec // logs dir under Cerberus-managed install root
		return InstallLayout{}, false, false, fmt.Errorf("create log dir: %w", mkErr)
	}
	if mkErr := os.MkdirAll(filepath.Dir(layout.UnitPath), 0o755); mkErr != nil { //nolint:gosec // the user manager's unit directory
		return InstallLayout{}, false, false, fmt.Errorf("create systemd user unit dir: %w", mkErr)
	}
	rendered, err := b.renderUnit(res, spec, layout)
	if err != nil {
		return InstallLayout{}, false, false, err
	}
	// 0600: a literal env value lands in the unit, as it does in a plist.
	unitChanged, err := writeFileIfChanged(layout.UnitPath, rendered, 0o600)
	if err != nil {
		return InstallLayout{}, false, false, fmt.Errorf("write unit: %w", err)
	}
	return layout, artifactChanged, unitChanged, nil
}

// renderUnit is the unit a resource installs as, rendered without writing
// anything: writeUnit writes it, and a plan hashes it.
func (b systemdBackend) renderUnit(res *domain.Resource, spec ProcessSpec, layout InstallLayout) ([]byte, error) {
	args, err := launchdProgramArguments(layout, spec)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(spec.Env)+1)
	for key, value := range spec.Env {
		env[key] = value
	}
	// The user manager hands a unit systemd's compiled-in PATH, which has
	// none of the operator's toolchain. Unless the resource sets its own,
	// the unit gets the PATH the serving daemon runs with, which
	// `cerberus install` composed from the installing shell.
	if _, ok := env["PATH"]; !ok {
		envPath := os.Getenv("PATH")
		if b.envPath != nil {
			envPath = b.envPath()
		}
		env["PATH"] = launchenv.Path(envPath, launchenv.SystemdBasePath)
	}
	args[0] = systemdExecutable(args[0], layout.WorkingDir, env["PATH"])

	envFile, envFileDigest := "", ""
	if spec.EnvFile != "" {
		envFile = spec.EnvFile
		if !filepath.IsAbs(envFile) {
			envFile = filepath.Join(spec.Dir, envFile)
		}
		envFileDigest = "missing"
		if digest, err := fileSHA256(envFile); err == nil {
			envFileDigest = digest
		}
	}
	// Secret references may come from env or env_file; either way the service
	// is fronted by `cerberus run-secrets`, which resolves them in the
	// service's own process. The unit keeps the references.
	merged, _ := launchdEnvironment(spec)
	if secretref.EnvHasRefs(merged) {
		shim, shimErr := b.cerberusPath()
		if shimErr != nil {
			return nil, fmt.Errorf("resource %q uses secret references but the cerberus executable could not be located to front it: %w", res.ID, shimErr)
		}
		args = append([]string{shim, "run-secrets", "--"}, args...)
	}

	logDir := filepath.Join(layout.RootDir, "logs")
	var buf bytes.Buffer
	line := func(format string, a ...any) { fmt.Fprintf(&buf, format+"\n", a...) }
	line("# Written by Cerberus for resource %s; `cerberus resource apply` overwrites edits.", systemdSingleLine(res.ID))
	line("[Unit]")
	line("Description=%s", systemdEscapeValue("Cerberus resource "+res.ID))
	// No start limit: like launchd's KeepAlive, a crash-looping service keeps
	// being retried instead of being parked as failed after five tries.
	line("StartLimitIntervalSec=0")
	line("")
	line("[Service]")
	line("Type=simple")
	line("WorkingDirectory=%s", systemdEscapeValue(layout.WorkingDir))
	line("ExecStart=%s", systemdExecLine(args))
	line("Restart=always")
	line("RestartSec=5")
	line("StandardOutput=append:%s", systemdEscapeValue(filepath.Join(logDir, "stdout.log")))
	line("StandardError=append:%s", systemdEscapeValue(filepath.Join(logDir, "stderr.log")))
	if envFile != "" {
		// Leading "-": a missing env_file is skipped, as launchd's rendering
		// skips it. The digest makes a changed file a changed unit, so apply
		// restarts the service to pick it up.
		line("# env_file sha256: %s", envFileDigest)
		line("EnvironmentFile=-%s", systemdEscapeValue(envFile))
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		line("Environment=%s", systemdQuote(key+"="+env[key]))
	}
	line("")
	line("[Install]")
	line("WantedBy=default.target")
	for _, value := range append(append([]string{layout.WorkingDir, envFile}, args...), keysAndValues(env)...) {
		if strings.ContainsAny(value, "\n\r") {
			return nil, fmt.Errorf("resource %q: a command, path or env value contains a newline, which a systemd unit cannot carry", res.ID)
		}
	}
	return buf.Bytes(), nil
}

func keysAndValues(env map[string]string) []string {
	out := make([]string, 0, 2*len(env))
	for key, value := range env {
		out = append(out, key, value)
	}
	return out
}

// systemdExecutable makes ExecStart's program something systemd can execute.
// systemd accepts an absolute path or a bare name it searches in its own
// fixed directories, never the unit's PATH, and not "./x": a relative path is
// taken against the working directory, and a bare name is looked up in the
// PATH the unit runs with, as a shell would. A bare name found nowhere is left
// for systemd to search.
func systemdExecutable(program, workingDir, path string) string {
	if program == "" || filepath.IsAbs(program) {
		return program
	}
	if strings.ContainsRune(program, '/') {
		return filepath.Join(workingDir, program)
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, program)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return program
}

// systemdEscapeValue escapes a single-valued setting: "%" starts a specifier
// in every unit setting.
func systemdEscapeValue(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func systemdSingleLine(value string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(value)
}

// systemdQuote double-quotes one word of a setting that is split on
// whitespace (Environment=, ExecStart=).
func systemdQuote(value string) string {
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return `"` + systemdEscapeValue(value) + `"`
}

// systemdExecLine is ExecStart's value: every argument quoted, and "$"
// doubled, since ExecStart= expands $VAR from the unit's environment while
// launchd passes arguments through untouched.
func systemdExecLine(args []string) string {
	words := make([]string, len(args))
	for i, arg := range args {
		words[i] = strings.ReplaceAll(systemdQuote(arg), "$", "$$")
	}
	return strings.Join(words, " ")
}

// splitSystemdWords reverses systemdExecLine and systemdQuote for the words
// Cerberus wrote: double-quoted, with backslash escapes and doubled "%"/"$".
func splitSystemdWords(value string) []string {
	var words []string
	var cur strings.Builder
	inQuote, escaped, has := false, false, false
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case escaped:
			cur.WriteByte(c)
			escaped = false
		case c == '\\' && inQuote:
			escaped = true
		case c == '"':
			inQuote = !inQuote
			has = true
		case (c == '%' || c == '$') && i+1 < len(value) && value[i+1] == c:
			cur.WriteByte(c)
			i++
		case (c == ' ' || c == '\t') && !inQuote:
			if has || cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if has || cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func parseSystemdShow(text string) systemdRecord {
	props := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if ok {
			props[key] = value
		}
	}
	rec := systemdRecord{
		LoadState:        props["LoadState"],
		ActiveState:      props["ActiveState"],
		SubState:         props["SubState"],
		UnitFileState:    props["UnitFileState"],
		Result:           props["Result"],
		NeedDaemonReload: props["NeedDaemonReload"] == "yes",
		Raw:              strings.TrimSpace(text),
	}
	rec.Loaded = rec.LoadState == "loaded"
	rec.PID, _ = strconv.Atoi(props["MainPID"])
	rec.Restarts, _ = strconv.Atoi(props["NRestarts"])
	// ExecMainStatus is 0 for a unit that never ran; it only means an exit
	// status once ExecMainCode says the process exited or was killed.
	if code := props["ExecMainCode"]; code != "" && code != "0" {
		if status, err := strconv.Atoi(props["ExecMainStatus"]); err == nil {
			rec.ExecMainStatus = &status
		}
	}
	rec.Diagnosis, rec.Highlights = diagnoseSystemdRecord(rec)
	return rec
}

func (r systemdRecord) exitedNonZero() bool {
	return (r.ExecMainStatus != nil && *r.ExecMainStatus != 0) || (r.Result != "" && r.Result != "success")
}

// systemdState maps a unit onto Cerberus's states the way parseLaunchdState
// maps a launchd job: a unit that is not installed is stopped, and one that
// systemd keeps restarting after non-zero exits is failed, not starting.
func systemdState(r systemdRecord) domain.State {
	switch r.LoadState {
	case "not-found", "":
		if r.ActiveState == "" || r.ActiveState == "inactive" {
			return domain.StateStopped
		}
	case "loaded":
	case "masked":
		return domain.StateStopped
	default: // error, bad-setting: the unit file cannot be used
		return domain.StateFailed
	}
	switch r.ActiveState {
	case "active", "reloading", "refreshing":
		if r.SubState == "exited" {
			return domain.StateStopped
		}
		return domain.StateRunning
	case "activating":
		if strings.HasPrefix(r.SubState, "auto-restart") && r.exitedNonZero() {
			return domain.StateFailed
		}
		return domain.StateStarting
	case "deactivating":
		return domain.StateStopped
	case "inactive":
		if r.exitedNonZero() {
			return domain.StateFailed
		}
		return domain.StateStopped
	case "failed":
		return domain.StateFailed
	default:
		return domain.StateUnknown
	}
}

func diagnoseSystemdRecord(r systemdRecord) (string, []string) {
	highlights := make([]string, 0, 6)
	add := func(key, value string) {
		if value != "" {
			highlights = append(highlights, key+"="+value)
		}
	}
	add("LoadState", r.LoadState)
	add("ActiveState", r.ActiveState)
	add("SubState", r.SubState)
	if r.PID > 0 {
		add("MainPID", strconv.Itoa(r.PID))
	}
	if r.ExecMainStatus != nil {
		add("ExecMainStatus", strconv.Itoa(*r.ExecMainStatus))
	}
	if r.Restarts > 0 {
		add("NRestarts", strconv.Itoa(r.Restarts))
	}
	add("Result", r.Result)

	switch {
	case r.LoadState == "not-found":
		return "unit is not installed", highlights
	case r.LoadState == "masked":
		return "unit is masked; `systemctl --user unmask` it before applying", highlights
	case r.LoadState != "" && r.LoadState != "loaded":
		return "systemd could not load the unit file (" + r.LoadState + "); see the journal", highlights
	case r.ActiveState == "activating" && strings.HasPrefix(r.SubState, "auto-restart") && r.exitedNonZero():
		return "process is crash-looping: it exits non-zero on launch and systemd keeps restarting it", highlights
	case r.ActiveState == "activating":
		return "systemd is starting the process", highlights
	case r.ActiveState == "active" && r.SubState == "running":
		return "service is loaded and running", highlights
	case r.ActiveState == "failed":
		return "process failed (" + r.Result + ")", highlights
	case r.ActiveState == "inactive" && r.exitedNonZero():
		return "process exited with a non-zero status", highlights
	case r.ActiveState == "inactive":
		return "service is installed but not running", highlights
	case r.ActiveState == "deactivating":
		return "systemd is stopping the process", highlights
	default:
		return "", highlights
	}
}

func isSystemdNotFound(out string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(out + " " + err.Error())
	return strings.Contains(text, "not loaded") ||
		strings.Contains(text, "not found") ||
		strings.Contains(text, "does not exist") ||
		strings.Contains(text, "no such file")
}

func systemdOutputDetail(out []byte) string {
	if text := strings.TrimSpace(redact.Text(string(out))); text != "" {
		return "; systemd output: " + text
	}
	return ""
}

func formatSystemdFailureDetails(out []byte, layout InstallLayout) string {
	parts := make([]string, 0, 6)
	if text := strings.TrimSpace(redact.Text(string(out))); text != "" {
		parts = append(parts, "systemd output: "+text)
	}
	parts = append(parts,
		"stderr log: "+filepath.Join(layout.RootDir, "logs", "stderr.log"),
		"stdout log: "+filepath.Join(layout.RootDir, "logs", "stdout.log"),
		"unit: "+layout.UnitPath,
		"install: "+layout.RootDir,
	)
	if layout.ArtifactPath != "" {
		parts = append(parts, "artifact: "+layout.ArtifactPath)
	}
	return "; " + strings.Join(parts, "; ")
}

func (b systemdBackend) cerberusPath() (string, error) {
	if b.selfPath != nil {
		return b.selfPath()
	}
	if path, err := os.Executable(); err == nil && path != "" {
		return filepath.EvalSymlinks(path)
	}
	return exec.LookPath("cerberus")
}

func (b systemdBackend) layout(res *domain.Resource, spec ProcessSpec) (InstallLayout, error) {
	home, err := b.homeDir()
	if err != nil {
		return InstallLayout{}, fmt.Errorf("resolve home dir: %w", err)
	}
	return DefaultInstallLayout(home, res, spec)
}

func (b systemdBackend) artifactInstaller() artifactInstaller {
	out := b.install
	if out.homeDir == nil {
		out.homeDir = b.homeDir
	}
	if out.now == nil {
		out.now = time.Now
	}
	return out
}

// PreviewSystemdUnit is the unit an os_service resource under systemd_user
// would be installed with, rendered and not written, and false for a resource
// that is not one. It is the same rendering apply writes.
func (c *Connector) PreviewSystemdUnit(res *domain.Resource) ([]byte, bool, error) {
	spec, err := SpecFromResourceConfig(res.Config)
	if err != nil {
		return nil, false, fmt.Errorf("decode process spec for %q: %w", res.ID, err)
	}
	if spec.Mode != ProcessModeOSService {
		return nil, false, nil
	}
	if supervisor, supErr := effectiveSupervisor(spec); supErr != nil || supervisor != ProcessSupervisorSystemdUser {
		return nil, false, nil //nolint:nilerr // not a systemd service: there is no unit to preview
	}
	svc, ok := c.service.(osServiceBackend)
	if !ok {
		return nil, false, nil
	}
	b := svc.systemdBackend()
	layout, err := b.layout(res, spec)
	if err != nil {
		return nil, false, err
	}
	rendered, err := b.renderUnit(res, spec, layout)
	return rendered, err == nil, err
}

// InspectSystemdRecord returns the live systemd record for an os_service resource.
func InspectSystemdRecord(ctx context.Context, res *domain.Resource, spec ProcessSpec) (SystemdRecord, error) {
	return newOSServiceBackend().systemdBackend().Inspect(ctx, res, spec)
}

// SystemdUserLinger reports whether the user manager outlives the operator's
// sessions (`loginctl enable-linger`). Without it, user services stop at
// logout and do not start at boot.
func SystemdUserLinger(ctx context.Context) (bool, error) {
	return newOSServiceBackend().systemdBackend().linger(ctx)
}

func (b systemdBackend) linger(ctx context.Context) (bool, error) {
	u, err := user.Current()
	if err != nil {
		return false, fmt.Errorf("resolve current user: %w", err)
	}
	out, err := b.run(ctx, "loginctl", "show-user", u.Username, "--property", "Linger")
	if err == nil {
		value := strings.TrimSpace(string(out))
		if v, ok := strings.CutPrefix(value, "Linger="); ok {
			return v == "yes", nil
		}
	}
	// loginctl answers only for a user with a session or lingering; logind
	// records lingering as a file, which needs no session to read.
	if _, statErr := os.Stat(filepath.Join("/var/lib/systemd/linger", u.Username)); statErr == nil {
		return true, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("read lingering state: %w", statErr)
	}
	return false, nil
}

// CheckSecretReferenceUnit is CheckSecretReferencePlist for a systemd unit:
// references must survive on disk and run-secrets must front ExecStart. It
// checks the generated unit, not the live process's resolved environment.
func CheckSecretReferenceUnit(path string, spec ProcessSpec) error {
	env, _ := launchdEnvironment(spec)
	if !secretref.EnvHasRefs(env) {
		return nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // managed unit path from the resource install layout
	if err != nil {
		return fmt.Errorf("read secret-reference unit: %w", err)
	}
	return validateSecretReferenceUnit(data, spec)
}

func validateSecretReferenceUnit(data []byte, spec ProcessSpec) error {
	var exec []string
	unitEnv := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "ExecStart="); ok {
			exec = splitSystemdWords(value)
		}
		if value, ok := strings.CutPrefix(line, "Environment="); ok {
			for _, word := range splitSystemdWords(value) {
				if key, val, ok := strings.Cut(word, "="); ok {
					unitEnv[key] = val
				}
			}
		}
	}
	if len(exec) < 4 || exec[0] == "" || exec[1] != "run-secrets" || exec[2] != "--" {
		return fmt.Errorf("secret references require ExecStart to start with cerberus run-secrets --; upgrade the serving daemon and re-apply the resource")
	}
	// A reference from env_file stays in the env file, which the unit names.
	for key, value := range spec.Env {
		if secretref.IsRef(value) && unitEnv[key] != value {
			return fmt.Errorf("unit environment %s must retain its configured secret reference; re-apply the resource", key)
		}
	}
	return nil
}
