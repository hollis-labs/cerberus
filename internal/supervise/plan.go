package supervise

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gitenv"
	"github.com/hollis-labs/cerberus/internal/plan"
)

// The lane's plan function (the old build's planResource, internal/cerbapi/plans.go): used
// when an approval is asked for, when it is used, and when a caller asks to see the plan, so
// the three cannot be computed differently (I6).

// planResource is a resource verb's plan: the resource's definition (as a keyed digest, since
// its environment can carry values), what the verb would install and from where, and the
// state it would change.
//   - deploy builds: the checkout's commit and dirty flag, and the build output it would
//     install.
//   - apply and sync install the build output as it is now: its path and sha256.
//   - apply and deploy write a unit: the rendered unit (or plist), as a keyed digest.
//   - every verb: the observed state, so an approval to stop a running service does not stop
//     it after it was restarted as something else.
func (s *Service) planResource(ctx context.Context, cfg *config.ConfigV2, spec gate.Spec, id string) (plan.Plan, error) {
	res, pspec, err := requireLocalProcess(cfg, id)
	if err != nil {
		if res != nil {
			return plan.Plan{}, fmt.Errorf("decode process spec for %q: %w", res.ID, err)
		}
		return plan.Plan{}, err
	}
	digest := s.o.Gate.Sink().Digest
	tgt, _ := gate.AuditTarget(spec)
	p := plan.Plan{Lane: plan.LaneResource, Connector: spec.Connector, Operation: spec.Operation, Effect: string(spec.Op.Effect),
		Target: tgt, ArgsDigest: digest(spec.Config), Digests: map[string]string{"spec": digest(res)}}
	verb := spec.Operation
	builds := verb == localconn.OpDeploy
	installs := builds || verb == localconn.OpApply || verb == localconn.OpSync
	if builds && localconn.HasBuildStrategy(pspec) {
		p.Source = checkedSource(ctx, pspec.Dir)
	}
	if installs && pspec.RunFrom == localconn.ProcessRunFromArtifact {
		if path, perr := localconn.ResolveArtifactSourcePath(pspec); perr == nil {
			p.Artifact = path
			// A build comes before the install, so the output as it is now is not what would be
			// installed; the source binds that instead.
			if !builds {
				p.Digests["artifact"] = fileDigest(path)
			}
		} else {
			p.Artifact = "(unresolved: " + perr.Error() + ")"
		}
	}
	dr := ResourceToDomain(res)
	if verb == localconn.OpDeploy || verb == localconn.OpApply {
		if rendered, ok, perr := s.conn.PreviewPlist(dr); perr != nil {
			return plan.Plan{}, fmt.Errorf("render the launch agent %q would install: %w", id, perr)
		} else if ok {
			p.Digests["plist"] = digest(string(rendered))
		}
		if rendered, ok, perr := s.conn.PreviewSystemdUnit(dr); perr != nil {
			return plan.Plan{}, fmt.Errorf("render the systemd unit %q would install: %w", id, perr)
		} else if ok {
			p.Digests["unit"] = digest(string(rendered))
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, statusProbeTimeout)
	state, err := s.conn.Status(probeCtx, dr)
	cancel()
	if err != nil {
		p.State = "unknown: " + err.Error()
	} else {
		p.State = string(state)
	}
	return p, nil
}

// gitSource is a checkout's commit and dirty flag. A directory that is not a repository, or a
// git that cannot be found, is recorded as such rather than failing the plan: the plan still
// binds everything else.
func gitSource(ctx context.Context, dir string) *plan.Source {
	src := &plan.Source{Path: dir}
	if _, err := exec.LookPath(gitenv.Binary()); err != nil {
		src.HEAD = "(git not found)"
		return src
	}
	run := func(args ...string) ([]byte, error) { return gitenv.Command(ctx, dir, args...).Output() }
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		src.HEAD = "(not a git checkout)"
		return src
	}
	src.HEAD = strings.TrimSpace(string(head))
	status, err := run("status", "--porcelain")
	src.Dirty = err != nil || len(strings.TrimSpace(string(status))) > 0
	if src.Dirty {
		src.Content = dirtyContent(ctx, dir)
	}
	return src
}

// sourceKey carries the checkout a deploy was checked against: its commit and uncommitted
// content, read once before the gate, so the plan hashes it and the build refuses a tree that
// changed since (M10).
type sourceKey struct{}

type checkedCheckout struct {
	dir string
	src *plan.Source
}

// withCheckedSource is ctx carrying dir's checkout as read now.
func withCheckedSource(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, sourceKey{}, checkedCheckout{dir: dir, src: gitSource(ctx, dir)})
}

// checkedSource is dir's checkout as the call's gate read it, or as it is now outside such a
// call.
func checkedSource(ctx context.Context, dir string) *plan.Source {
	if c, ok := ctx.Value(sourceKey{}).(checkedCheckout); ok && c.dir == dir && c.src != nil {
		src := *c.src
		return &src
	}
	return gitSource(ctx, dir)
}

// sourceChanged says how dir's checkout differs from what the call's gate read, or "" when it
// does not, or no gate read it.
func sourceChanged(ctx context.Context, dir string) string {
	c, ok := ctx.Value(sourceKey{}).(checkedCheckout)
	if !ok || c.dir != dir || c.src == nil {
		return ""
	}
	now := gitSource(ctx, dir)
	if now.HEAD == c.src.HEAD && now.Dirty == c.src.Dirty && now.Content == c.src.Content {
		return ""
	}
	describe := func(s *plan.Source) string {
		out := s.HEAD
		if len(out) > 12 {
			out = out[:12]
		}
		if s.Dirty {
			out += " with uncommitted changes"
		}
		return out
	}
	return fmt.Sprintf("the checkout at %s changed after this deploy was checked (it was %s, it is %s), so nothing was built; run the deploy again, and it is checked as the tree is now", dir, describe(c.src), describe(now))
}

// dirtyContent is a digest of a checkout's uncommitted changes: its diff against HEAD, and
// each untracked file that is not ignored, by path and content. It reads the whole
// repository, not only dir, since a build in a subdirectory can reach the rest of it.
func dirtyContent(ctx context.Context, dir string) string {
	top, err := gitenv.Command(ctx, dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "(unreadable)"
	}
	root := strings.TrimSpace(string(top))
	h := sha256.New()
	diff, err := gitenv.Command(ctx, root, "diff", "HEAD", "--binary").Output()
	if err != nil {
		return "(unreadable)"
	}
	h.Write(diff)
	untracked, err := gitenv.Command(ctx, root, "ls-files", "-o", "--exclude-standard", "-z").Output()
	if err != nil {
		return "(unreadable)"
	}
	paths := strings.Split(strings.TrimRight(string(untracked), "\x00"), "\x00")
	sort.Strings(paths)
	for _, rel := range paths {
		if rel == "" {
			continue
		}
		h.Write([]byte("\x00untracked\x00" + rel + "\x00"))
		h.Write([]byte(fileDigest(filepath.Join(root, rel))))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// fileDigest is a file's sha256, or why it has none.
func fileDigest(path string) string {
	f, err := os.Open(path) //nolint:gosec // a resource's own build output
	if err != nil {
		return "(missing)"
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "(unreadable)"
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
