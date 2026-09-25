package pluginhost

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

type TrustMode string

const (
	TrustModeCatalogSigned TrustMode = "catalog_signed"
	TrustModeDeveloper     TrustMode = "developer"
	// TrustModeLocal accepts an unsigned plugin from a local directory. It is
	// the supported path for plugins an operator installs themselves, and it
	// does not require a devmode build. It records what actually happened:
	// TrustTierUnsigned, never TrustTierSigned.
	TrustModeLocal TrustMode = "local"
)

type TrustTier string

const (
	TrustTierBuiltin     TrustTier = "builtin"
	TrustTierSigned      TrustTier = "signed"
	TrustTierLocalDev    TrustTier = "local_dev"
	TrustTierUnsignedDev TrustTier = "unsigned_dev"
	// TrustTierUnsigned is an unsigned plugin the operator installed from a
	// local path. Neither integrity nor provenance is tracked. A host-computed
	// entrypoint hash is recorded at install time, but nothing ever reads it
	// back: ValidateInstall below only asserts it is non-empty, and no load
	// path re-hashes the binary to compare against it. A binary replaced
	// underneath us is therefore not detectable. See CERB-GAP-336.
	TrustTierUnsigned  TrustTier = "unsigned"
	TrustTierUntrusted TrustTier = "untrusted"
)

type SandboxProfile string

const (
	SandboxProfileDefault    SandboxProfile = "default"
	SandboxProfileDockerHost SandboxProfile = "docker_host"
)

// TrustPolicy controls which connector plugins may be installed or loaded.
// Runtime sandboxes can narrow access, but they do not replace manifest trust.
type TrustPolicy struct {
	Mode                  TrustMode `json:"mode" yaml:"mode"`
	RequireSignature      bool      `json:"require_signature" yaml:"require_signature"`
	RequireArchiveHash    bool      `json:"require_archive_hash" yaml:"require_archive_hash"`
	RequireArchiveSig     bool      `json:"require_archive_signature" yaml:"require_archive_signature"`
	AllowUnsignedLocal    bool      `json:"allow_unsigned_local,omitempty" yaml:"allow_unsigned_local,omitempty"`
	AllowedDeveloperRoots []string  `json:"allowed_developer_roots,omitempty" yaml:"allowed_developer_roots,omitempty"`
}

func DefaultTrustPolicy() TrustPolicy {
	return TrustPolicy{
		Mode:               TrustModeCatalogSigned,
		RequireSignature:   true,
		RequireArchiveHash: true,
		RequireArchiveSig:  true,
	}
}

// LocalTrustPolicy allows unsigned plugins from a local directory. Signatures
// are not required; an archive hash still is, but the host computes it rather
// than asking the operator to supply one.
func LocalTrustPolicy() TrustPolicy {
	return TrustPolicy{
		Mode:               TrustModeLocal,
		RequireSignature:   false,
		RequireArchiveHash: true,
		RequireArchiveSig:  false,
		AllowUnsignedLocal: true,
	}
}

func DeveloperTrustPolicy(roots ...string) TrustPolicy {
	return TrustPolicy{
		Mode:                  TrustModeDeveloper,
		RequireSignature:      false,
		RequireArchiveHash:    true,
		RequireArchiveSig:     false,
		AllowUnsignedLocal:    true,
		AllowedDeveloperRoots: roots,
	}
}

type TrustCheck struct {
	SourcePath      string
	CatalogSigned   bool
	ArchiveSHA256   string
	ArchiveSigned   bool
	LocalPath       bool
	RequestedTier   TrustTier
	SandboxProfile  SandboxProfile
	SandboxEnforced bool
	Manifest        contract.Manifest
}

type TrustDecision struct {
	Tier TrustTier
}

func (p TrustPolicy) ValidateInstall(check TrustCheck) (TrustDecision, error) {
	if err := check.Manifest.Validate(); err != nil {
		return TrustDecision{Tier: TrustTierUntrusted}, err
	}
	if check.RequestedTier == TrustTierUntrusted {
		return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("plugin %q is marked untrusted", check.Manifest.ID)
	}
	if p.RequireArchiveHash && check.ArchiveSHA256 == "" {
		return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("plugin %q requires archive sha256", check.Manifest.ID)
	}
	if check.SandboxProfile != "" && !check.SandboxEnforced {
		return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("plugin %q requested sandbox profile %q but sandbox is not enforced", check.Manifest.ID, check.SandboxProfile)
	}
	switch p.Mode {
	case TrustModeLocal:
		if !check.LocalPath {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("local trust mode for plugin %q only allows local plugin paths", check.Manifest.ID)
		}
		// An operator who genuinely has signatures still gets credit for them.
		if check.CatalogSigned && check.ArchiveSigned {
			return TrustDecision{Tier: TrustTierSigned}, nil
		}
		return TrustDecision{Tier: TrustTierUnsigned}, nil
	case "", TrustModeCatalogSigned:
		if p.RequireSignature && !check.CatalogSigned {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("plugin %q requires a trusted catalog signature", check.Manifest.ID)
		}
		if p.RequireArchiveSig && !check.ArchiveSigned {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("plugin %q requires a trusted archive signature", check.Manifest.ID)
		}
		return TrustDecision{Tier: TrustTierSigned}, nil
	case TrustModeDeveloper:
		if !DevModeEnabled {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("developer trust mode requires a devmode build")
		}
		if check.CatalogSigned && check.ArchiveSigned {
			return TrustDecision{Tier: TrustTierSigned}, nil
		}
		if !p.AllowUnsignedLocal {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("developer mode for plugin %q does not allow unsigned local plugins", check.Manifest.ID)
		}
		if len(p.AllowedDeveloperRoots) == 0 {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("developer mode for plugin %q requires allowed developer roots", check.Manifest.ID)
		}
		if !check.LocalPath {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("developer mode for plugin %q only allows local plugin paths", check.Manifest.ID)
		}
		if !pathAllowed(check.SourcePath, p.AllowedDeveloperRoots) {
			return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("plugin %q source path %q is outside allowed developer roots", check.Manifest.ID, check.SourcePath)
		}
		if check.ArchiveSigned {
			return TrustDecision{Tier: TrustTierLocalDev}, nil
		}
		return TrustDecision{Tier: TrustTierUnsignedDev}, nil
	default:
		return TrustDecision{Tier: TrustTierUntrusted}, fmt.Errorf("unsupported trust mode %q", p.Mode)
	}
}

// ErrPreviewUnsupported is a dry run of a plugin operation whose manifest does
// not declare supports_dry. The plugin is never called. The text leads with
// the same code the admin lane uses, so the one-shot plugin path, which
// returns this unwrapped, still names it.
var ErrPreviewUnsupported = errors.New("preview_unsupported: the manifest does not declare supports_dry, so there is no dry-run preview; nothing was executed")

func OperationAllowed(tier TrustTier, op contract.ManifestOperation, acknowledged bool) error {
	switch tier {
	// TrustTierUnsigned sits with the trusted tiers rather than the dev tiers,
	// and the distinction is deliberate: a signature attests to PROVENANCE,
	// while the acknowledgment below attests to INTENT. An operator installing
	// an unsigned plugin from a local path has established provenance out of
	// band by building or vetting it themselves. Grouping it with the dev tiers
	// would block every destructive operation, which would make the plugin lane
	// read-only — and a read-only plugin lane cannot carry the provider
	// integrations it exists for. Destructive operations still require --ack.
	case TrustTierBuiltin, TrustTierSigned, TrustTierUnsigned:
	case TrustTierLocalDev, TrustTierUnsignedDev:
		if op.Destructive {
			return fmt.Errorf("destructive operation %q is not agent-auto executable for %s plugins", op.Name, tier)
		}
	case TrustTierUntrusted, "":
		return fmt.Errorf("operation %q is not allowed for untrusted plugin", op.Name)
	default:
		return fmt.Errorf("operation %q has unsupported trust tier %q", op.Name, tier)
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
