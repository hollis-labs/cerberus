package cerbapi

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/daemon"
	"github.com/chrispian/cerberus/internal/domain"
)

// ProtectServingDaemon marks the process that is about to serve this runtime.
// Configure it before starting the monitor or accepting socket/HTTP/MCP calls.
func (s *ResourceRuntimeService) ProtectServingDaemon(executable, label string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.servingExecutable = executable
	s.servingLabel = label
	s.servingDaemon = true
	if s.local == nil {
		s.local = localconn.New()
	}
	s.local.SetMutationGuard(func(res *domain.Resource, spec localconn.ProcessSpec) error {
		return s.refuseSelfMutation(&config.ResourceDef{ID: res.ID, Project: res.ProjectID}, spec)
	})
}

func (s *ResourceRuntimeService) refuseSelfMutation(res *config.ResourceDef, spec localconn.ProcessSpec) error {
	if !s.servingDaemon {
		return nil
	}
	self := res.ID == daemon.CanonicalDaemonResourceID
	if spec.ServiceName != "" && (spec.ServiceName == s.servingLabel || spec.ServiceName == daemon.CanonicalDaemonServiceLabel) {
		self = true
	}
	if spec.Mode == localconn.ProcessModeOSService && spec.RunFrom == localconn.ProcessRunFromArtifact {
		if home, err := os.UserHomeDir(); err == nil {
			if layout, layoutErr := localconn.DefaultInstallLayout(home, resourceDefToDomain(res), spec); layoutErr == nil {
				self = self || sameExecutablePath(layout.ArtifactPath, s.servingExecutable)
			}
		}
	} else if len(spec.Command) > 0 {
		command := spec.Command[0]
		if !filepath.IsAbs(command) && spec.Dir != "" {
			command = filepath.Join(spec.Dir, command)
		}
		self = self || sameExecutablePath(command, s.servingExecutable)
	}
	if !self {
		return nil
	}
	return fmt.Errorf("refusing to mutate serving Cerberus daemon resource %q through its own runtime: build to a temporary path, atomically move the binary over the daemon artifact, then run `launchctl kickstart -k %s` from an external terminal; do not use resource deploy, apply, sync, reload, stop or remove for this self-upgrade", res.ID, daemon.LaunchdServiceTarget())
}

func sameExecutablePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	aInfo, aErr := os.Stat(a) //nolint:gosec // read-only identity probe of trusted local resource paths
	bInfo, bErr := os.Stat(b) //nolint:gosec // serving executable captured by daemon startup
	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}
