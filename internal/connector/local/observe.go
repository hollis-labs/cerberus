package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/launchenv"
	"github.com/hollis-labs/cerberus/internal/service"
)

// The read-only half of the local connector, for the Substrate build's observe
// mode (docs/substrate-port-plan.md section 7.2 C): while the old build still
// supervises, the new one renders, reads and reports, and writes nothing.

// CommandRunner runs a system tool and returns its combined output.
type CommandRunner interface {
	CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error)
}

// UserBusRunner runs systemd tools against the user manager, with the bus
// address a process started outside a login session needs.
func UserBusRunner() CommandRunner { return envCommandRunner{environ: launchenv.UserBusEnviron} }

// RenderEnv is what a rendered unit depends on besides its resource: the home
// directory its layout derives from, the PATH the serving daemon runs with
// (a unit without its own PATH gets this one), and the cerberus executable that
// fronts a service whose environment carries secret references.
type RenderEnv struct {
	Home     string
	Path     string
	Cerberus string
}

// RenderSystemdUnit renders the systemd user unit a resource installs as,
// byte for byte what the systemd backend writes, and its unit name. It writes
// nothing: the env file, if any, is only read for its digest.
func RenderSystemdUnit(res *domain.Resource, env RenderEnv) (string, []byte, error) {
	spec, err := SpecFromResourceConfig(res.Config)
	if err != nil {
		return "", nil, fmt.Errorf("decode process spec for %q: %w", res.ID, err)
	}
	b := systemdBackend{
		homeDir:  func() (string, error) { return env.Home, nil },
		envPath:  func() string { return env.Path },
		selfPath: func() (string, error) { return env.Cerberus, nil },
	}
	layout, err := b.layout(res, spec)
	if err != nil {
		return "", nil, err
	}
	unit, err := b.renderUnit(res, spec, layout)
	return layout.UnitName, unit, err
}

// ObserveSystemdUnit reports a systemd-supervised resource's state from the
// user manager (`systemctl --user show`), and its unit name. It only reads.
func ObserveSystemdUnit(ctx context.Context, runner CommandRunner, home string, res *domain.Resource) (domain.State, string, error) {
	spec, err := SpecFromResourceConfig(res.Config)
	if err != nil {
		return domain.StateUnknown, "", fmt.Errorf("decode process spec for %q: %w", res.ID, err)
	}
	b := systemdBackend{runner: runner, homeDir: func() (string, error) { return home, nil }}
	layout, err := b.layout(res, spec)
	if err != nil {
		return domain.StateUnknown, "", err
	}
	state, err := b.Status(ctx, res, spec)
	return state, layout.UnitName, err
}

// ObserveDevSession reports a dev session's state from the pid and launch
// metadata files under stateDir/pids (the old build's ~/.cerberus) without
// adopting, stopping or tidying anything: a stale pid file is reported as
// stopped and left where it is, and no directory is created.
func ObserveDevSession(stateDir, id string) (domain.State, string) {
	pidPath := filepath.Join(stateDir, "pids", id+".pid")
	raw, err := os.ReadFile(pidPath) //nolint:gosec // the adopted state tree's own pid file
	if errors.Is(err, os.ErrNotExist) {
		return domain.StateStopped, "no pid file"
	}
	if err != nil {
		return domain.StateUnknown, "the pid file could not be read: " + err.Error()
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return domain.StateUnknown, "the pid file does not hold a pid"
	}
	if !processAlive(pid) {
		return domain.StateStopped, fmt.Sprintf("pid %d is not running (stale pid file, left in place)", pid)
	}
	var meta service.PIDMeta
	metaRaw, err := os.ReadFile(filepath.Join(stateDir, "pids", id+".meta")) //nolint:gosec // the adopted state tree's own meta file
	if err != nil || json.Unmarshal(metaRaw, &meta) != nil || meta.PID != pid {
		return domain.StateUnknown, fmt.Sprintf("pid %d is running but has no matching launch metadata", pid)
	}
	identity := processStartIdentity(pid)
	if identity == "" {
		return domain.StateUnknown, fmt.Sprintf("cannot verify the launch identity of pid %d", pid)
	}
	if meta.ProcessStart != "" {
		if identity == meta.ProcessStart {
			return domain.StateRunning, fmt.Sprintf("pid %d", pid)
		}
	} else if started, parseErr := time.ParseInLocation("Mon Jan 2 15:04:05 2006", identity, time.Local); parseErr == nil &&
		meta.StartedAt.Sub(started) >= 0 && meta.StartedAt.Sub(started) < 5*time.Second {
		// Older launches recorded wall time but no ps identity (ownedPID).
		return domain.StateRunning, fmt.Sprintf("pid %d", pid)
	}
	return domain.StateUnknown, fmt.Sprintf("pid %d's identity differs from the recorded launch", pid)
}

// IsSystemdSupervised reports whether spec runs as a systemd user unit on this
// host: an os_service whose supervisor is systemd_user, or auto on Linux.
func IsSystemdSupervised(spec ProcessSpec) bool {
	if spec.Mode != ProcessModeOSService {
		return false
	}
	sup, err := effectiveSupervisor(spec)
	return err == nil && sup == ProcessSupervisorSystemdUser
}
