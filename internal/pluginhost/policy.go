package pluginhost

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// InstallOrigin records how a plugin was installed. It is not a trust level:
// Cerberus does not vet, sign or verify plugins, and a plugin runs with the
// operator's authority whichever origin it has. The two origins differ only
// in what the host restricts.
type InstallOrigin string

const (
	// OriginInstalled is a plugin the operator installed from a local
	// directory. Its operations are gated like a built-in's: destructive ones
	// need acknowledgment.
	OriginInstalled InstallOrigin = "installed"
	// OriginDev is a development install (`--dev`, devmode builds only). It
	// must sit under an allowed developer root, and every destructive
	// operation is refused, acknowledged or not.
	OriginDev InstallOrigin = "dev"
)

type SandboxProfile string

const (
	SandboxProfileDefault    SandboxProfile = "default"
	SandboxProfileDockerHost SandboxProfile = "docker_host"
)

// InstallPolicy controls which plugin directories may be installed.
type InstallPolicy struct {
	// Dev selects a development install: devmode build only, source under
	// DeveloperRoots, destructive operations refused.
	Dev            bool     `json:"dev,omitempty" yaml:"dev,omitempty"`
	DeveloperRoots []string `json:"developer_roots,omitempty" yaml:"developer_roots,omitempty"`
}

// LocalInstallPolicy installs a plugin from a local directory.
func LocalInstallPolicy() InstallPolicy { return InstallPolicy{} }

// DeveloperInstallPolicy installs a development plugin, which must live under
// one of roots.
func DeveloperInstallPolicy(roots ...string) InstallPolicy {
	return InstallPolicy{Dev: true, DeveloperRoots: roots}
}

type InstallCheck struct {
	SourcePath string
	// EntrypointSHA256 fingerprints the entrypoint binary as installed. It is
	// change detection, not a trust signal: it says nothing about who built
	// the binary. Nothing compares it yet — refusing to load a binary that no
	// longer matches is P1 (CERB-GAP-336).
	EntrypointSHA256 string
	SandboxProfile   SandboxProfile
	SandboxEnforced  bool
	Manifest         contract.Manifest
}

type InstallDecision struct {
	Origin InstallOrigin `json:"origin"`
}

func (p InstallPolicy) ValidateInstall(check InstallCheck) (InstallDecision, error) {
	if err := check.Manifest.Validate(); err != nil {
		return InstallDecision{}, err
	}
	if check.EntrypointSHA256 == "" {
		return InstallDecision{}, fmt.Errorf("plugin %q entrypoint could not be fingerprinted", check.Manifest.ID)
	}
	if check.SandboxProfile != "" && !check.SandboxEnforced {
		return InstallDecision{}, fmt.Errorf("plugin %q requested sandbox profile %q but sandbox is not enforced", check.Manifest.ID, check.SandboxProfile)
	}
	if !p.Dev {
		return InstallDecision{Origin: OriginInstalled}, nil
	}
	if !DevModeEnabled {
		return InstallDecision{}, fmt.Errorf("a development install (--dev) requires a devmode build")
	}
	if len(p.DeveloperRoots) == 0 {
		return InstallDecision{}, fmt.Errorf("development install of plugin %q requires allowed developer roots", check.Manifest.ID)
	}
	if !pathAllowed(check.SourcePath, p.DeveloperRoots) {
		return InstallDecision{}, fmt.Errorf("plugin %q source path %q is outside allowed developer roots", check.Manifest.ID, check.SourcePath)
	}
	return InstallDecision{Origin: OriginDev}, nil
}

// ErrPreviewUnsupported is a dry run of a plugin operation whose manifest does
// not declare supports_dry. The plugin is never called. The text leads with
// the same code the admin lane uses, so the one-shot plugin path, which
// returns this unwrapped, still names it.
var ErrPreviewUnsupported = errors.New("preview_unsupported: the manifest does not declare supports_dry, so there is no dry-run preview; nothing was executed")

func OperationAllowed(origin InstallOrigin, op contract.ManifestOperation, acknowledged bool) error {
	switch origin {
	case OriginInstalled:
	case OriginDev:
		if op.Destructive {
			return fmt.Errorf("destructive operation %q is refused for a development (--dev) plugin; install it without --dev to run it", op.Name)
		}
	default:
		return fmt.Errorf("operation %q refused: plugin has unknown install origin %q", op.Name, origin)
	}
	// Any destructive operation needs acknowledgment. RequiresAck used to be
	// ANDed in here, which let a manifest declare destructive: true,
	// requires_ack: false and opt out of the host's gate. It is metadata now.
	if op.Destructive && !acknowledged {
		return fmt.Errorf("destructive operation %q requires operator acknowledgment", op.Name)
	}
	return nil
}

func pathAllowed(path string, roots []string) bool {
	if path == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}
