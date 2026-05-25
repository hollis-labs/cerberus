package local

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BuildStrategyConfig is the resource-local build contract. It mirrors the
// Fragments-style kind/source/rules shape so new strategies can be added
// without changing process-resource fields again.
type BuildStrategyConfig struct {
	Kind   string
	Source map[string]any
	Rules  map[string]any
}

type BuildConfig struct {
	WorkDir string
	Env     []string
	Source  map[string]any
	Rules   map[string]any
}

type BuildResult struct {
	Output string
	// Command is the argv the strategy executed, and Dir the directory it ran
	// in — surfaced for diagnostics (deploy errors and the build log) so a
	// build failure is self-explanatory instead of a bare "exit status 2".
	// Command is empty for go_standard matrix builds, which run one command
	// per os/arch variant (no single command to record); Dir is still set.
	Command   []string
	Dir       string
	Artifacts []BuildArtifact
}

type BuildArtifact struct {
	Path     string
	Checksum string
	Metadata map[string]string
}

type BuildStrategy interface {
	Kind() string
	Build(ctx context.Context, cfg BuildConfig) (*BuildResult, error)
}

// LegacyCommandKind is the build_strategy kind that runs an explicit
// command list. It is the translation target for the deprecated `build:`
// resource field (see SpecFromResourceConfig): a config carrying
// `build: [make, build]` is decoded as a legacy_command strategy so
// existing apps keep building while they migrate to a first-class
// strategy (go_standard, make_standard, ...).
const LegacyCommandKind = "legacy_command"

var buildStrategies = map[string]BuildStrategy{
	"go_standard":     goStandardBuildStrategy{},
	"make_standard":   makeStandardBuildStrategy{},
	LegacyCommandKind: legacyCommandBuildStrategy{},
}

func BuildProcess(spec ProcessSpec) (string, error) {
	return BuildProcessContext(context.Background(), spec)
}

func BuildProcessContext(ctx context.Context, spec ProcessSpec) (string, error) {
	result, err := BuildProcessResultContext(ctx, spec)
	if result == nil {
		return "", err
	}
	return result.Output, err
}

func BuildProcessResultContext(ctx context.Context, spec ProcessSpec) (*BuildResult, error) {
	if spec.BuildStrategy == nil || spec.BuildStrategy.Kind == "" {
		return nil, fmt.Errorf("no build_strategy configured")
	}
	strategy := buildStrategies[spec.BuildStrategy.Kind]
	if strategy == nil {
		return nil, fmt.Errorf("unknown build_strategy kind %q", spec.BuildStrategy.Kind)
	}
	result, err := strategy.Build(ctx, BuildConfig{
		WorkDir: spec.Dir,
		Env:     sessionEnv(spec),
		Source:  spec.BuildStrategy.Source,
		Rules:   spec.BuildStrategy.Rules,
	})
	if result == nil {
		result = &BuildResult{}
	}
	return result, err
}

func HasBuildStrategy(spec ProcessSpec) bool {
	return spec.BuildStrategy != nil && spec.BuildStrategy.Kind != ""
}

type goStandardBuildStrategy struct{}

func (goStandardBuildStrategy) Kind() string { return "go_standard" }

func (goStandardBuildStrategy) Build(ctx context.Context, cfg BuildConfig) (*BuildResult, error) {
	root := stringRule(cfg.Source, "root", ".")
	dir := resolveBuildDir(cfg.WorkDir, root)
	target := stringRule(cfg.Rules, "target", stringRule(cfg.Source, "package", "."))
	if matrix := matrixRule(cfg.Rules, "matrix"); len(matrix) > 0 {
		res, err := buildGoMatrix(ctx, cfg, dir, target, matrix)
		if res != nil {
			// A matrix build runs one `go build` per os/arch variant, so there
			// is no single Command to record; surface the dir at least.
			res.Dir = dir
		}
		return res, err
	}

	output := stringRule(cfg.Rules, "output", "")
	if output == "" {
		return nil, fmt.Errorf("go_standard rule output is required")
	}

	args := append([]string{"build"}, stringSliceRule(cfg.Rules, "flags")...)
	args = append(args, "-o", output)
	if ldflags := stringSliceRule(cfg.Rules, "ldflags"); len(ldflags) > 0 {
		args = append(args, "-ldflags", strings.Join(ldflags, " "))
	}
	args = append(args, target)

	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // strategy arguments come from trusted local Cerberus config
	cmd.Dir = dir
	cmd.Env = cfg.Env
	out, err := cmd.CombinedOutput()
	return &BuildResult{Output: string(out), Command: append([]string{"go"}, args...), Dir: dir}, err
}

func buildGoMatrix(ctx context.Context, cfg BuildConfig, dir, target string, matrix []map[string]string) (*BuildResult, error) {
	distDir := resolveBuildDir(dir, stringRule(cfg.Rules, "dist", "dist"))
	if err := os.MkdirAll(distDir, 0755); err != nil { //nolint:gosec // dist dir is configured by trusted local project config
		return nil, fmt.Errorf("create dist dir: %w", err)
	}

	artifactRules := anyMapField(cfg.Rules, "artifacts")
	archive := boolRule(artifactRules, "archive", false)
	checksum := boolRule(artifactRules, "checksum", false)
	version := stringRule(cfg.Rules, "version", envValue(cfg.Env, "VERSION"))
	buildDate := stringRule(cfg.Rules, "build_date", envValue(cfg.Env, "BUILD_DATE"))
	if buildDate == "" {
		buildDate = time.Now().UTC().Format(time.RFC3339)
	}
	nameTemplate := stringRule(artifactRules, "name", "")
	baseName := stringRule(cfg.Rules, "output", filepath.Base(strings.TrimPrefix(target, "./")))
	if baseName == "." || baseName == string(filepath.Separator) || baseName == "" {
		baseName = "app"
	}
	if nameTemplate == "" {
		nameTemplate = defaultArtifactName(baseName, version)
	}

	var combinedOutput strings.Builder
	var artifacts []BuildArtifact
	var checksumLines []string
	for _, variant := range matrix {
		tokens := cloneStringMap(variant)
		tokens["VERSION"] = version
		tokens["version"] = version
		tokens["BUILD_DATE"] = buildDate
		tokens["build_date"] = buildDate
		tokens["target"] = target

		artifactName := renderBuildTemplate(nameTemplate, tokens)
		binaryPath := filepath.Join(distDir, artifactName)
		args := append([]string{"build"}, stringSliceRule(cfg.Rules, "flags")...)
		args = append(args, "-o", binaryPath)
		if ldflags := renderStringSlice(stringSliceRule(cfg.Rules, "ldflags"), tokens); len(ldflags) > 0 {
			args = append(args, "-ldflags", strings.Join(ldflags, " "))
		}
		args = append(args, target)

		cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // strategy arguments come from trusted local Cerberus config
		cmd.Dir = dir
		cmd.Env = appendVariantEnv(cfg.Env, variant)
		out, err := cmd.CombinedOutput()
		combinedOutput.Write(out)
		if err != nil {
			return &BuildResult{Output: combinedOutput.String(), Artifacts: artifacts}, err
		}

		artifactPath := binaryPath
		if archive {
			archivePath := binaryPath + ".tar.gz"
			if err := writeTarGz(archivePath, binaryPath, baseName); err != nil {
				return &BuildResult{Output: combinedOutput.String(), Artifacts: artifacts}, err
			}
			artifactPath = archivePath
		}

		sum := ""
		if checksum {
			var err error
			sum, err = fileSHA256(artifactPath)
			if err != nil {
				return &BuildResult{Output: combinedOutput.String(), Artifacts: artifacts}, err
			}
			checksumLines = append(checksumLines, fmt.Sprintf("%s  %s\n", sum, filepath.Base(artifactPath)))
		}
		artifacts = append(artifacts, BuildArtifact{
			Path:     artifactPath,
			Checksum: sum,
			Metadata: cloneStringMap(variant),
		})
	}

	if checksum && len(checksumLines) > 0 {
		sort.Strings(checksumLines)
		if err := os.WriteFile(filepath.Join(distDir, "checksums.txt"), []byte(strings.Join(checksumLines, "")), 0644); err != nil {
			return &BuildResult{Output: combinedOutput.String(), Artifacts: artifacts}, fmt.Errorf("write checksums: %w", err)
		}
	}
	return &BuildResult{Output: combinedOutput.String(), Artifacts: artifacts}, nil
}

type makeStandardBuildStrategy struct{}

func (makeStandardBuildStrategy) Kind() string { return "make_standard" }

func (makeStandardBuildStrategy) Build(ctx context.Context, cfg BuildConfig) (*BuildResult, error) {
	root := stringRule(cfg.Source, "root", ".")
	dir := resolveBuildDir(cfg.WorkDir, root)
	target := stringRule(cfg.Rules, "target", "build")
	args := append([]string{target}, stringSliceRule(cfg.Rules, "args")...)

	cmd := exec.CommandContext(ctx, "make", args...) //nolint:gosec // strategy arguments come from trusted local Cerberus config
	cmd.Dir = dir
	cmd.Env = cfg.Env
	out, err := cmd.CombinedOutput()
	return &BuildResult{Output: string(out), Command: append([]string{"make"}, args...), Dir: dir}, err
}

// legacyCommandBuildStrategy runs an explicit command list in the
// resource's build directory. It exists only as the compatibility target
// for the deprecated `build:` field; new configs should prefer a
// first-class strategy. The command is read from rules.command.
type legacyCommandBuildStrategy struct{}

func (legacyCommandBuildStrategy) Kind() string { return LegacyCommandKind }

func (legacyCommandBuildStrategy) Build(ctx context.Context, cfg BuildConfig) (*BuildResult, error) {
	root := stringRule(cfg.Source, "root", ".")
	dir := resolveBuildDir(cfg.WorkDir, root)
	command := stringSliceRule(cfg.Rules, "command")
	if len(command) == 0 {
		return nil, fmt.Errorf("legacy_command build_strategy requires a non-empty rules.command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...) //nolint:gosec // command comes from trusted local Cerberus config
	cmd.Dir = dir
	cmd.Env = cfg.Env
	out, err := cmd.CombinedOutput()
	return &BuildResult{Output: string(out), Command: append([]string(nil), command...), Dir: dir}, err
}

func resolveBuildDir(base, root string) string {
	if root == "" || root == "." {
		return base
	}
	if filepath.IsAbs(root) || base == "" {
		return root
	}
	return filepath.Join(base, root)
}

func buildStrategyConfigFromAny(raw any) (*BuildStrategyConfig, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected map, got %T", raw)
	}
	kind, _ := stringField(m, "kind")
	if kind == "" {
		return nil, fmt.Errorf("kind is required")
	}
	return &BuildStrategyConfig{
		Kind:   kind,
		Source: anyMapField(m, "source"),
		Rules:  anyMapField(m, "rules"),
	}, nil
}

func (c *BuildStrategyConfig) expandHome() {
	if c == nil {
		return
	}
	c.Source = expandHomeAnyMap(c.Source)
	c.Rules = expandHomeAnyMap(c.Rules)
}

func (c *BuildStrategyConfig) toConfigMap() map[string]any {
	out := map[string]any{"kind": c.Kind}
	if len(c.Source) > 0 {
		out["source"] = cloneAnyMap(c.Source)
	}
	if len(c.Rules) > 0 {
		out["rules"] = cloneAnyMap(c.Rules)
	}
	return out
}

func anyMapField(cfg map[string]any, key string) map[string]any {
	raw, ok := cfg[key]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return cloneAnyMap(m)
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func expandHomeAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch typed := v.(type) {
		case string:
			out[k] = expandHomeValue(typed)
		case []string:
			out[k] = expandHomeSlice(typed)
		case []any:
			xs := make([]any, 0, len(typed))
			for _, item := range typed {
				if s, ok := item.(string); ok {
					xs = append(xs, expandHomeValue(s))
				} else {
					xs = append(xs, item)
				}
			}
			out[k] = xs
		default:
			out[k] = v
		}
	}
	return out
}

func expandHomeValue(v string) string {
	if strings.HasPrefix(v, "~/") || v == "~" {
		return expandHomeSlice([]string{v})[0]
	}
	return v
}

func stringRule(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return fallback
}

func stringSliceRule(m map[string]any, key string) []string {
	if len(m) == 0 {
		return nil
	}
	return stringSliceField(m, key)
}

func boolRule(m map[string]any, key string, fallback bool) bool {
	if len(m) == 0 {
		return fallback
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return fallback
}

func matrixRule(m map[string]any, key string) []map[string]string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	axes, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(axes))
	values := make(map[string][]string, len(axes))
	for k, rawValues := range axes {
		xs := stringValues(rawValues)
		if len(xs) == 0 {
			return nil
		}
		keys = append(keys, k)
		values[k] = xs
	}
	sort.Strings(keys)
	var out []map[string]string
	var walk func(int, map[string]string)
	walk = func(i int, current map[string]string) {
		if i == len(keys) {
			out = append(out, cloneStringMap(current))
			return
		}
		key := keys[i]
		for _, v := range values[key] {
			current[key] = v
			walk(i+1, current)
		}
		delete(current, key)
	}
	walk(0, map[string]string{})
	return out
}

func stringValues(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, v := range typed {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func defaultArtifactName(baseName, version string) string {
	if version == "" {
		return baseName + "-${os}-${arch}"
	}
	return baseName + "-" + version + "-${os}-${arch}"
}

func renderStringSlice(in []string, tokens map[string]string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, renderBuildTemplate(s, tokens))
	}
	return out
}

func renderBuildTemplate(s string, tokens map[string]string) string {
	out := s
	for k, v := range tokens {
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	return out
}

func appendVariantEnv(env []string, variant map[string]string) []string {
	out := append([]string(nil), env...)
	for k, v := range variant {
		out = append(out, strings.ToUpper(k)+"="+v)
		switch strings.ToLower(k) {
		case "os":
			out = append(out, "GOOS="+v)
		case "arch":
			out = append(out, "GOARCH="+v)
		}
	}
	return out
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func writeTarGz(dst, src, entryName string) error {
	in, err := os.Open(src) //nolint:gosec // source path is produced by the local build strategy
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat artifact: %w", err)
	}
	out, err := os.Create(dst) //nolint:gosec // destination path is produced under configured dist dir
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("create archive header: %w", err)
	}
	header.Name = entryName
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write archive header: %w", err)
	}
	if _, err := io.Copy(tw, in); err != nil {
		return fmt.Errorf("write archive body: %w", err)
	}
	return nil
}
