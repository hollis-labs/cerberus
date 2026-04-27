package local

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

type runtimeBackend interface {
	Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec, svc *service.ManagedService) (ApplyResult, error)
	Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec, svc *service.ManagedService) error
	Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec, svc *service.ManagedService) error
	Destroy(ctx context.Context, res *domain.Resource, spec ProcessSpec, svc *service.ManagedService) error
	Status(ctx context.Context, res *domain.Resource, spec ProcessSpec, svc *service.ManagedService) (domain.State, error)
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

func (b devSessionBackend) Apply(_ context.Context, _ *domain.Resource, _ ProcessSpec, svc *service.ManagedService) (ApplyResult, error) {
	if svc == nil {
		return ApplyResult{}, fmt.Errorf("dev_session backend requires a managed service")
	}
	return ApplyResult{Action: ApplyActionStarted}, svc.Start()
}

func (b devSessionBackend) Stop(_ context.Context, _ *domain.Resource, _ ProcessSpec, svc *service.ManagedService) error {
	if svc == nil {
		return fmt.Errorf("dev_session backend requires a managed service")
	}
	return svc.Stop()
}

func (b devSessionBackend) Reload(_ context.Context, _ *domain.Resource, _ ProcessSpec, svc *service.ManagedService) error {
	if svc == nil {
		return fmt.Errorf("dev_session backend requires a managed service")
	}
	_ = svc.Stop()
	return svc.Start()
}

func (b devSessionBackend) Destroy(ctx context.Context, res *domain.Resource, spec ProcessSpec, svc *service.ManagedService) error {
	return b.Stop(ctx, res, spec, svc)
}

func (b devSessionBackend) Status(_ context.Context, _ *domain.Resource, _ ProcessSpec, svc *service.ManagedService) (domain.State, error) {
	if svc == nil {
		return domain.StateUnknown, fmt.Errorf("dev_session backend requires a managed service")
	}
	svc.Poll()
	return mapStatus(svc.Status), nil
}

type osServiceBackend struct {
	launchd launchdBackend
}

func (b osServiceBackend) Start(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *service.ManagedService) error {
	_, err := b.Apply(ctx, res, spec, nil)
	return err
}

func (b osServiceBackend) Apply(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *service.ManagedService) (ApplyResult, error) {
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

func (b osServiceBackend) Stop(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *service.ManagedService) error {
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

func (b osServiceBackend) Reload(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *service.ManagedService) error {
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

func (b osServiceBackend) Destroy(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *service.ManagedService) error {
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

func (b osServiceBackend) Status(ctx context.Context, res *domain.Resource, spec ProcessSpec, _ *service.ManagedService) (domain.State, error) {
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
