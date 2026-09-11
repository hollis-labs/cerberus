package local

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
)

type runtimeBackend interface {
	Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) (ApplyResult, error)
	Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) error
	Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) error
	Destroy(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) error
	Status(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) (domain.State, error)
}

type ApplyAction string

const (
	ApplyActionStarted   ApplyAction = "started"
	ApplyActionReloaded  ApplyAction = "reloaded"
	ApplyActionRestarted ApplyAction = "restarted"
	ApplyActionNoop      ApplyAction = "noop"
)

type ApplyResult struct {
	Action          ApplyAction
	ArtifactChanged bool
	PlistChanged    bool
}

type devSessionBackend struct{}

func (b devSessionBackend) Apply(_ context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) (ApplyResult, error) {
	if session == nil {
		return ApplyResult{}, fmt.Errorf("dev_session backend requires a session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.Update(res, spec)
	return ApplyResult{Action: ApplyActionStarted}, session.Start()
}

func (b devSessionBackend) Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) error {
	if session == nil {
		return fmt.Errorf("dev_session backend requires a session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.Update(res, spec)
	return session.stopContext(ctx)
}

func (b devSessionBackend) Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) error {
	if session == nil {
		return fmt.Errorf("dev_session backend requires a session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.Update(res, spec)
	if err := session.stopContext(ctx); err != nil {
		return err
	}
	return session.Start()
}

func (b devSessionBackend) Destroy(ctx context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) error {
	return b.Stop(ctx, res, spec, session)
}

func (b devSessionBackend) Status(_ context.Context, res *domain.Resource, spec ProcessSpec, session *devSession) (domain.State, error) {
	if session == nil {
		return domain.StateUnknown, fmt.Errorf("dev_session backend requires a session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.Update(res, spec)
	state := session.Poll()
	if state == domain.StateUnknown {
		return state, fmt.Errorf("%s", session.errMsg)
	}
	return state, nil
}

type osServiceBackend struct {
	launchd launchdBackend
}

func (b osServiceBackend) Start(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *devSession) error {
	_, err := b.Apply(ctx, res, spec, nil)
	return err
}

func (b osServiceBackend) Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *devSession) (ApplyResult, error) {
	supervisor, err := effectiveSupervisor(spec)
	if err != nil {
		return ApplyResult{}, err
	}
	switch supervisor {
	case ProcessSupervisorLaunchd:
		return b.launchdBackend().Apply(ctx, res, spec)
	default:
		return ApplyResult{}, fmt.Errorf("os_service start not implemented yet for resource %q via supervisor %q", res.ID, supervisor)
	}
}

func (b osServiceBackend) Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *devSession) error {
	supervisor, err := effectiveSupervisor(spec)
	if err != nil {
		return err
	}
	switch supervisor {
	case ProcessSupervisorLaunchd:
		return b.launchdBackend().Stop(ctx, res, spec)
	default:
		return fmt.Errorf("os_service stop not implemented yet for resource %q via supervisor %q", res.ID, supervisor)
	}
}

func (b osServiceBackend) Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *devSession) error {
	supervisor, err := effectiveSupervisor(spec)
	if err != nil {
		return err
	}
	switch supervisor {
	case ProcessSupervisorLaunchd:
		return b.launchdBackend().Reload(ctx, res, spec)
	default:
		return fmt.Errorf("os_service reload not implemented yet for resource %q via supervisor %q", res.ID, supervisor)
	}
}

func (b osServiceBackend) Destroy(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *devSession) error {
	supervisor, err := effectiveSupervisor(spec)
	if err != nil {
		return err
	}
	switch supervisor {
	case ProcessSupervisorLaunchd:
		return b.launchdBackend().Remove(ctx, res, spec)
	default:
		return fmt.Errorf("os_service remove not implemented yet for resource %q via supervisor %q", res.ID, supervisor)
	}
}

func (b osServiceBackend) Status(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *devSession) (domain.State, error) {
	supervisor, err := effectiveSupervisor(spec)
	if err != nil {
		return domain.StateUnknown, err
	}
	switch supervisor {
	case ProcessSupervisorLaunchd:
		return b.launchdBackend().Status(ctx, res, spec)
	default:
		return domain.StateUnknown, nil
	}
}

func effectiveSupervisor(spec ProcessSpec) (ProcessSupervisor, error) {
	switch spec.Supervisor {
	case "", ProcessSupervisorAuto:
		switch runtime.GOOS {
		case "darwin":
			return ProcessSupervisorLaunchd, nil
		case "linux":
			return ProcessSupervisorSystemdUser, nil
		case "windows":
			return ProcessSupervisorWindowsService, nil
		default:
			return "", fmt.Errorf("os_service mode is unsupported on %s", runtime.GOOS)
		}
	case ProcessSupervisorLaunchd:
		if runtime.GOOS != "darwin" {
			return "", fmt.Errorf("launchd supervisor requires darwin, got %s", runtime.GOOS)
		}
		return spec.Supervisor, nil
	case ProcessSupervisorSystemdUser:
		if runtime.GOOS != "linux" {
			return "", fmt.Errorf("systemd_user supervisor requires linux, got %s", runtime.GOOS)
		}
		return spec.Supervisor, nil
	case ProcessSupervisorWindowsService:
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("windows_service supervisor requires windows, got %s", runtime.GOOS)
		}
		return spec.Supervisor, nil
	default:
		return "", fmt.Errorf("unsupported process supervisor %q", spec.Supervisor)
	}
}

func newOSServiceBackend() osServiceBackend {
	return osServiceBackend{
		launchd: launchdBackend{
			runner:  execCommandRunner{},
			homeDir: os.UserHomeDir,
			uid:     os.Getuid,
			install: newArtifactInstaller(),
		},
	}
}

func (b osServiceBackend) launchdBackend() launchdBackend {
	out := b.launchd
	if out.runner == nil {
		out.runner = execCommandRunner{}
	}
	if out.homeDir == nil {
		out.homeDir = os.UserHomeDir
	}
	if out.uid == nil {
		out.uid = os.Getuid
	}
	if out.install.homeDir == nil {
		out.install.homeDir = out.homeDir
	}
	if out.install.now == nil {
		out.install.now = time.Now
	}
	return out
}
