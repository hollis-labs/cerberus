package local

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

// ActivationArtifact identifies the binary on disk used for this activation.
// It says nothing about unbuilt source edits or application-level readiness.
type ActivationArtifact struct {
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modified_at"`
}

func InspectActivationArtifact(res *domain.Resource, spec ProcessSpec) (*ActivationArtifact, error) {
	path, ok := devSessionRunBinary(ProcessSpec{Dir: spec.Dir, Command: spec.Command})
	if spec.RunFrom == ProcessRunFromArtifact {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		layout, err := DefaultInstallLayout(home, res, spec)
		if err != nil {
			return nil, err
		}
		path, ok = layout.ArtifactPath, true
	}
	if !ok {
		return nil, nil
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return nil, err
	}
	return &ActivationArtifact{Path: path, SHA256: hash, ModifiedAt: info.ModTime()}, nil
}

// ValidateDeployOutput refuses an ambiguous build-to-artifact join. Otherwise
// a successful make target could install command[0] that it never produced.
func ValidateDeployOutput(spec ProcessSpec) error {
	if HasBuildStrategy(spec) && spec.RunFrom == ProcessRunFromArtifact {
		if _, ok := buildStrategyOutputPath(spec); !ok {
			return fmt.Errorf("artifact deployment requires build_strategy.rules.output naming the binary the build produces; command[0] is not a build-output contract")
		}
	}
	return nil
}
