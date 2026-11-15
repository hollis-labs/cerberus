package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// resolveSSHTarget merges the configured resource's connection settings into an ssh
// operation's config. The key table has already refused every field but the operation's own,
// so the host, key and trust settings can only come from the resource.
func (s *Service) resolveSSHTarget(ctx context.Context, args OperationArgs) (OperationArgs, error) {
	lookup := s.lookupFor(ctx)
	id := stringFromConfig(args.Config, "id", "")
	if id == "" {
		return args, codedErr(args, gate.CodeInvalidArgs, errors.New("id is required: pass a configured ssh resource id"))
	}
	if lookup == nil {
		return args, codedErr(args, gate.CodeUnavailable, errors.New("no resource configuration is loaded, so ssh resource ids cannot be resolved"))
	}
	def, ok := lookup(id)
	if !ok {
		return args, codedErr(args, gate.CodeInvalidArgs, fmt.Errorf("resource %q not found in the registry", id))
	}
	if def.Connector != "ssh" {
		return args, codedErr(args, gate.CodeInvalidArgs, fmt.Errorf("resource %q uses connector %q, not ssh", id, def.Connector))
	}
	merged := make(map[string]any, len(def.Config)+len(args.Config)+2)
	for key, value := range def.Config {
		merged[key] = value
	}
	for key, value := range args.Config {
		merged[key] = value
	}
	merged["id"] = def.ID
	merged["name"] = def.Name
	args.Config = merged
	return args, nil
}

// resolveDockerResource replaces `resource: <id>` with the configured docker resource's
// config; fields the caller sent alongside it win.
func (s *Service) resolveDockerResource(ctx context.Context, args OperationArgs) (OperationArgs, error) {
	lookup := s.lookupFor(ctx)
	id := stringFromConfig(args.Config, dockerconn.ResourceKey, "")
	if id == "" {
		return args, nil
	}
	if lookup == nil {
		return args, codedErr(args, gate.CodeUnavailable, fmt.Errorf("no resource configuration is loaded, so docker resource %q cannot be resolved", id))
	}
	def, ok := lookup(id)
	if !ok {
		return args, codedErr(args, gate.CodeInvalidArgs, fmt.Errorf("resource %q not found in the registry", id))
	}
	if def.Connector != "docker" {
		return args, codedErr(args, gate.CodeInvalidArgs, fmt.Errorf("resource %q is %s/%s, not a docker resource", def.ID, def.Type, def.Connector))
	}
	merged := make(map[string]any, len(def.Config)+len(args.Config)+3)
	for key, value := range def.Config {
		merged[key] = value
	}
	for key, value := range args.Config {
		if key != dockerconn.ResourceKey {
			merged[key] = value
		}
	}
	name := def.Name
	if name == "" {
		name = def.ID
	}
	merged["id"] = def.ID
	merged["name"] = name
	// A resource that names a compose file operates as a stack; only a single-container
	// resource needs a container name inferred for it.
	if _, ok := merged["container"]; !ok && merged["compose_file"] == nil {
		merged["container"] = name
	}
	args.Config = merged
	return args, nil
}

// resourceLookupKey carries a call's resource lookup (M10).
type resourceLookupKey struct{}

func withResourceLookup(ctx context.Context, lookup func(string) (*config.ResourceDef, bool)) context.Context {
	return context.WithValue(ctx, resourceLookupKey{}, lookup)
}

// lookupFor is the resource lookup a call resolves through: the one it began with, or the
// lane's.
func (s *Service) lookupFor(ctx context.Context) gate.ResourceLookup {
	if lookup, ok := ctx.Value(resourceLookupKey{}).(func(string) (*config.ResourceDef, bool)); ok && lookup != nil {
		return lookup
	}
	return s.lookup()
}

// onceLookup resolves each id once and answers every later lookup of it from that: the
// definition a call was checked against is the one it runs.
func onceLookup(lookup func(string) (*config.ResourceDef, bool)) func(string) (*config.ResourceDef, bool) {
	if lookup == nil {
		return nil
	}
	type found struct {
		def *config.ResourceDef
		ok  bool
	}
	var mu sync.Mutex
	seen := map[string]found{}
	return func(id string) (*config.ResourceDef, bool) {
		mu.Lock()
		defer mu.Unlock()
		if f, ok := seen[id]; ok {
			return f.def, f.ok
		}
		def, ok := lookup(id)
		if def != nil {
			copied := *def
			def = &copied
		}
		seen[id] = found{def, ok}
		return def, ok
	}
}

// localPathField is the input every local file transfer names its path by.
const localPathField = "local_path"

// confinedToTransferRoot is whether p's local paths are held to the transfer root: everyone
// but a person at their own terminal, who this HTTP lane never serves.
func confinedToTransferRoot(p audit.Principal) bool {
	return p.Kind != string(gate.PrincipalHuman) || p.Via != gate.ViaCLI
}

// confineLocalPath holds args' local_path to the transfer root (<adopt_root>/transfers) for a
// confined caller and an operation that touches local files, and rewrites it to the resolved
// path (B3, CERB-GAP-850).
func (s *Service) confineLocalPath(args OperationArgs, op contract.Operation, p audit.Principal) (OperationArgs, error) {
	if op.LocalFS == contract.LocalFSNone || op.LocalFS == "" || !confinedToTransferRoot(p) {
		return args, nil
	}
	raw, present := args.Config[localPathField]
	if !present {
		return args, nil
	}
	requested, ok := raw.(string)
	if !ok || strings.TrimSpace(requested) == "" {
		return args, nil // the key table refuses it
	}
	root := s.o.Paths.Transfers()
	resolved, err := ConfineToRoot(root, requested)
	if err != nil {
		return args, codedErr(args, gate.CodeInvalidArgs, redact.GuidanceWrap(err,
			"%s from %s reads and writes local files only under the transfer root %s; name a path inside it (a relative path is taken from it)",
			localPathField, callerWords(p), root))
	}
	cfg := make(map[string]any, len(args.Config))
	for k, v := range args.Config {
		cfg[k] = v
	}
	cfg[localPathField] = resolved
	args.Config = cfg
	return args, nil
}

func callerWords(p audit.Principal) string {
	kind, via := p.Kind, p.Via
	if kind == "" {
		kind = "a caller of unknown kind"
	}
	if via == "" || via == gate.ViaUnknown {
		return kind
	}
	return kind + " over " + via
}

var errOutsideRoot = errors.New("the path resolves outside the transfer root")

// ConfineToRoot resolves path against root and returns it if it stays inside root once every
// symlink on the way is followed. A relative path is taken from root. A dangling symlink is
// refused. root is created, private, if it is missing.
func ConfineToRoot(root, path string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("the transfer root %q is not an absolute path", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create the transfer root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve the transfer root: %w", err)
	}
	path = config.ExpandHomePath(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(realRoot, path)
	}
	path = filepath.Clean(path)
	rel, ok := within(realRoot, path)
	if !ok {
		if rel, ok = within(filepath.Clean(root), path); !ok {
			return "", errOutsideRoot
		}
	}
	cur := realRoot
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			cur = filepath.Join(append([]string{cur}, parts[i:]...)...)
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("%s is a symlink that cannot be resolved, and writing it would follow it: %w", part, errOutsideRoot)
			}
			next = target
		}
		if _, ok := within(realRoot, next); !ok {
			return "", errOutsideRoot
		}
		cur = next
	}
	if _, ok := within(realRoot, cur); !ok {
		return "", errOutsideRoot
	}
	return cur, nil
}

func within(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// credentialScope is the scope a call's credentials resolve under (I9): its access, which its
// effect decides (a dry run or plan reads), and its resolved target.
func credentialScope(spec gate.Spec) secrets.CredentialScope {
	effect := spec.Op.Effect
	if !spec.Known {
		effect = contract.EffectExec
	}
	_, resolved := gate.AuditTarget(spec)
	return secrets.CredentialScope{Access: secrets.AccessFor(effect, spec.DryRun || spec.PlanOnly), Target: resolved}
}

// withCredentialScope marks ctx with spec's credential scope, before the gate: a plan the gate
// hashes resolves the credentials the run then uses.
func withCredentialScope(ctx context.Context, spec gate.Spec) context.Context {
	return secrets.WithCredentialScope(ctx, credentialScope(spec))
}

// labeledCredentialNames are a definition's declared secrets as records name them: with the
// binding each would use for this call.
func labeledCredentialNames(def contract.Definition, scope secrets.CredentialScope, file secrets.BindingFile) []string {
	names := make([]string, 0, len(def.Config.Secrets))
	for _, sec := range def.Config.Secrets {
		names = append(names, secrets.CredentialName(def.ID, sec.Name, file[def.ID].Resolve(def.ID, sec.Name, &scope)))
	}
	sort.Strings(names)
	return names
}

// noCredentialRefusal codes a call its binding gave no credential as credential_missing.
func noCredentialRefusal(args OperationArgs, err error) error {
	var none *secrets.NoCredentialError
	var coded *gate.Error
	if err == nil || errors.As(err, &coded) || !errors.As(err, &none) {
		return err
	}
	return codedErr(args, gate.CodeCredentialMissing, redact.Guidance("%s", none.Error()))
}

// CredentialBindingView is one declared secret's binding for a call, for `cerberus policy
// explain` (the old cerbapi.CredentialBindingView).
type CredentialBindingView struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// None is a binding that refuses the call: no credential for its access.
	None bool `json:"none,omitempty"`
}

// ExplainCredentials is which binding each of def's declared secrets would use for operation,
// as a preview or not, on the resolved target: the old cerbapi.ExplainCredentials. An error is
// connector-secrets.yaml failing to load, when every call needing a credential fails.
func (s *Service) ExplainCredentials(def contract.Definition, operation string, preview bool, resolved target.Target) ([]CredentialBindingView, error) {
	file, err := s.refs.Bindings()
	if err != nil {
		return nil, err
	}
	op, known := def.Operation(operation)
	scope := credentialScope(gate.Spec{Connector: def.ID, Operation: operation, Op: op, Known: known, DryRun: preview})
	scope.Target = resolved
	out := make([]CredentialBindingView, 0, len(def.Config.Secrets))
	for _, sec := range def.Config.Secrets {
		b := file[def.ID].Resolve(def.ID, sec.Name, &scope)
		out = append(out, CredentialBindingView{Name: secrets.CredentialName(def.ID, sec.Name, b), Label: b.Label, None: b.None})
	}
	return out, nil
}
