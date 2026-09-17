package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// fakeDockerCLI writes an executable stand-in for the docker binary that prints
// its own argv and a few environment variables, so a test can assert exactly
// what the connector would have invoked.
func fakeDockerCLI(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o600); err != nil {
		t.Fatalf("writing fake docker: %v", err)
	}
	//nolint:gosec // a stand-in for the docker binary has to be executable; it lives in the test's own TempDir
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("making fake docker executable: %v", err)
	}
	return path
}

const reportInvocation = `
echo "argv: $@"
echo "DOCKER_HOST=${DOCKER_HOST-<unset>}"
echo "HOME=${HOME-<unset>}"
echo "SSH_AUTH_SOCK=${SSH_AUTH_SOCK-<unset>}"
`

func TestCLIBackendSetsDockerHostWithoutStrippingTheEnvironment(t *testing.T) {
	t.Setenv("HOME", "/Users/tester")
	t.Setenv("SSH_AUTH_SOCK", "/private/tmp/agent.sock")

	backend := newCLIBackendWithPath(fakeDockerCLI(t, reportInvocation)).
		WithTarget(Target{Host: "ssh://cburks@muctlvaig"}).(*CLIBackend)

	out, err := backend.run(context.Background(), "ps")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "DOCKER_HOST=ssh://cburks@muctlvaig") {
		t.Errorf("DOCKER_HOST not passed to docker:\n%s", got)
	}
	// cmd.Env replaces rather than extends. Losing HOME breaks docker's own
	// config lookup, and losing SSH_AUTH_SOCK breaks the ssh:// transport
	// outright — both silently, and only on the remote path.
	if !strings.Contains(got, "HOME=/Users/tester") {
		t.Errorf("HOME was stripped from the docker environment:\n%s", got)
	}
	if !strings.Contains(got, "SSH_AUTH_SOCK=/private/tmp/agent.sock") {
		t.Errorf("SSH_AUTH_SOCK was stripped from the docker environment:\n%s", got)
	}
}

func TestCLIBackendPassesContextAsAGlobalFlag(t *testing.T) {
	backend := newCLIBackendWithPath(fakeDockerCLI(t, reportInvocation)).
		WithTarget(Target{Context: "azure-dev"}).(*CLIBackend)

	out, err := backend.run(context.Background(), "ps", "--format", "json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "argv: --context azure-dev ps --format json") {
		t.Errorf("context flag not placed before the subcommand:\n%s", got)
	}
	if strings.Contains(got, "DOCKER_HOST=ssh") {
		t.Errorf("a context target must not also set DOCKER_HOST:\n%s", got)
	}
	// Only ssh:// hides the cause of a failure, so only ssh:// pays for debug logging.
	if strings.Contains(got, "--log-level") {
		t.Errorf("debug logging enabled for a non-ssh target:\n%s", got)
	}
}

func TestCLIBackendEnablesDebugLoggingOnlyForSSH(t *testing.T) {
	backend := newCLIBackendWithPath(fakeDockerCLI(t, reportInvocation)).
		WithTarget(Target{Host: "ssh://muctlvaig"}).(*CLIBackend)

	out, err := backend.run(context.Background(), "ps")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(string(out), "argv: --log-level debug ps") {
		t.Errorf("ssh target did not enable the only channel that carries the failure cause:\n%s", out)
	}
}

func TestCLIBackendLeavesTheDefaultTargetUntouched(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	backend := newCLIBackendWithPath(fakeDockerCLI(t, reportInvocation))

	out, err := backend.run(context.Background(), "ps")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "argv: ps") {
		t.Errorf("default target gained global flags:\n%s", got)
	}
	if !strings.Contains(got, "DOCKER_HOST=\n") && !strings.Contains(got, "DOCKER_HOST=<unset>") {
		t.Errorf("default target set DOCKER_HOST:\n%s", got)
	}
}

func TestWithTargetDoesNotMutateTheReceiver(t *testing.T) {
	base := newCLIBackendWithPath("/usr/local/bin/docker")
	remote := base.WithTarget(Target{Host: "ssh://muctlvaig"}).(*CLIBackend)

	if !base.target.IsZero() {
		t.Errorf("base backend was rebound to %#v; host selection is per operation", base.target)
	}
	if remote.target.Host != "ssh://muctlvaig" {
		t.Errorf("remote backend target = %#v", remote.target)
	}
	if remote.dockerPath != base.dockerPath {
		t.Errorf("binary discovery was lost across WithTarget: %q", remote.dockerPath)
	}
}

func TestTargetRejectsHostAndContextTogether(t *testing.T) {
	err := Target{Host: "ssh://muctlvaig", Context: "azure-dev"}.Validate()
	if err == nil {
		t.Fatal("expected host and context to be rejected together")
	}
	// The docker CLI resolves the conflict silently in favor of --context, so
	// the message has to name both values for the operator to see the mistake.
	for _, want := range []string{"ssh://muctlvaig", "azure-dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	for _, target := range []Target{{}, {Host: "ssh://muctlvaig"}, {Context: "azure-dev"}} {
		if err := target.Validate(); err != nil {
			t.Errorf("Validate(%#v) = %v, want nil", target, err)
		}
	}
}

func TestTargetDescribeNamesTheHost(t *testing.T) {
	cases := map[Target]string{
		{}:                               "the local Docker daemon",
		{Host: "ssh://cburks@muctlvaig"}: "ssh://cburks@muctlvaig",
		{Context: "azure-dev"}:           "docker context azure-dev",
	}
	for target, want := range cases {
		if got := target.Describe(); got != want {
			t.Errorf("Target%#v.Describe() = %q, want %q", target, got, want)
		}
	}
}

// muctlvaigStderr is the verbatim stderr of
//
//	DOCKER_HOST=ssh://muctlvaig docker --log-level debug ps
//
// captured 2026-09-16. It is the shape the whole diagnosis exists for: the one
// line docker means for a human names a host that does not exist and blames the
// wrong thing, and the real cause is only on a debug line.
const muctlvaigStderr = `time="2026-09-16T22:37:50-05:00" level=debug msg="commandconn: starting ssh with [-- muctlvaig docker system dial-stdio]"
time="2026-09-16T22:37:50-05:00" level=debug msg="commandconn (ssh):failed to open the raw stream connection: dial unix /var/run/docker.sock: connect: permission denied\n"
Cannot connect to the Docker daemon at http://docker.example.com. Is the docker daemon running?
`

func TestFailureReasonRecoversTheCauseTheCLIHides(t *testing.T) {
	reason := failureReason([]byte(muctlvaigStderr))

	if !strings.Contains(reason, "permission denied") {
		t.Errorf("the real cause was lost:\n%s", reason)
	}
	if strings.Contains(reason, placeholderHost) {
		t.Errorf("reason still names docker's placeholder host:\n%s", reason)
	}
	if strings.Contains(reason, `level=debug`) || strings.Contains(reason, `time="`) {
		t.Errorf("raw debug log lines leaked into operator-facing text:\n%s", reason)
	}
	if !strings.Contains(reason, "docker group") {
		t.Errorf("permission failure did not name the recovery:\n%s", reason)
	}
}

func TestFailureReasonLeavesAnOrdinaryErrorAlone(t *testing.T) {
	reason := failureReason([]byte("Error response from daemon: No such container: web\n"))
	if reason != "Error response from daemon: No such container: web" {
		t.Errorf("reason = %q", reason)
	}
	if strings.Contains(reason, "docker group") {
		t.Errorf("unrelated failure gained a permission hint: %q", reason)
	}
}

func TestFailureReasonReportsSilentFailures(t *testing.T) {
	if reason := failureReason(nil); reason == "" {
		t.Error("an empty stderr must still produce something an operator can read")
	}
}

func TestRunNamesTheTargetHostInErrors(t *testing.T) {
	stderrFile := filepath.Join(t.TempDir(), "stderr")
	if err := os.WriteFile(stderrFile, []byte(muctlvaigStderr), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	backend := newCLIBackendWithPath(fakeDockerCLI(t, "cat "+stderrFile+" >&2; exit 1")).
		WithTarget(Target{Host: "ssh://cburks@muctlvaig"}).(*CLIBackend)

	_, err := backend.run(context.Background(), "ps")
	if err == nil {
		t.Fatal("expected an error")
	}
	// "connection refused" with no host named is the failure mode this exists
	// to prevent: the CLI's own text is identical whichever host was asked for.
	if !strings.Contains(err.Error(), "ssh://cburks@muctlvaig") {
		t.Errorf("error does not name the target host:\n%s", err)
	}
	if !strings.Contains(err.Error(), "docker group") {
		t.Errorf("error does not name the recovery:\n%s", err)
	}
}

// TestSocketPermissionRecoverySurvivesRedaction guards the rule in AGENTS.md:
// redact.Text runs over every operator-facing error path and has eaten its own
// guidance four times. A recovery instruction that arrives as [REDACTED] is
// worse than none.
func TestSocketPermissionRecoverySurvivesRedaction(t *testing.T) {
	err := failureReason([]byte(muctlvaigStderr))
	if got := redact.Text(err); got != err {
		t.Errorf("redaction rewrote the failure reason:\ngot:  %s\nwant: %s", got, err)
	}
	if strings.Contains(redact.Text(socketPermissionRecovery), redact.Marker) {
		t.Errorf("redaction ate the recovery instruction: %s", redact.Text(socketPermissionRecovery))
	}
	// The host is the other thing an operator needs and the other thing a
	// credential-shaped pattern could swallow.
	for _, host := range []string{"ssh://cburks@muctlvaig", "tcp://10.0.0.4:2376", "ssh://muctlvaig.corp.adtran.com"} {
		if got := redact.Text(Target{Host: host}.Describe()); got != host {
			t.Errorf("redaction rewrote the target host: %q -> %q", host, got)
		}
	}
}
