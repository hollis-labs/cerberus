package procscan

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// TestFilterByUID_SelfPasses exercises the REAL platform pidUID syscall
// (proc_pidinfo on darwin / stat /proc on linux) against the test
// process's own pid. If the darwin proc_bsdinfo offset (procBSDInfoUIDOff)
// is wrong — Apple kernel changes, refactor miscomputes the offset — this
// test fails loudly because the returned uid won't match os.Getuid() and
// every cascade-kill in production would silently skip every PID.
//
// This is the regression-trip for the entire feature: the offset is
// hand-computed from xnu's proc_info.h layout and must round-trip
// against the live kernel.
func TestFilterByUID_SelfPasses(t *testing.T) {
	t.Parallel()
	self := os.Getpid()
	got := filterByUID([]int{self}, discardLogger())
	if len(got) != 1 || got[0] != self {
		t.Fatalf("filterByUID(self) = %v, want [%d] — pidUID/offset regression?", got, self)
	}
}

// TestFilterByUID_DropsForeignUIDWithLog injects a fake pidUIDFn that
// reports a foreign uid for one pid and the caller's uid for another.
// Asserts the foreign pid is dropped and the rebuild.cascade.skipped_foreign_uid
// log event is emitted with the right pid+uid attributes.
func TestFilterByUID_DropsForeignUIDWithLog(t *testing.T) {
	// not parallel: mutates package-level pidUIDFn
	const foreignPID = 99001
	const localPID = 99002
	const foreignUID = 4242
	selfUID := os.Getuid()

	orig := pidUIDFn
	t.Cleanup(func() { pidUIDFn = orig })
	pidUIDFn = func(pid int) (int, error) {
		switch pid {
		case foreignPID:
			return foreignUID, nil
		case localPID:
			return selfUID, nil
		}
		return 0, errors.New("unexpected pid")
	}

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	got := filterByUID([]int{foreignPID, localPID}, logger)
	if len(got) != 1 || got[0] != localPID {
		t.Fatalf("filterByUID = %v, want [%d]", got, localPID)
	}
	logged := buf.String()
	if !strings.Contains(logged, "rebuild.cascade.skipped_foreign_uid") {
		t.Errorf("expected skipped_foreign_uid log event, got: %s", logged)
	}
	if !strings.Contains(logged, "pid=99001") {
		t.Errorf("expected pid=99001 in log, got: %s", logged)
	}
	if !strings.Contains(logged, "uid=4242") {
		t.Errorf("expected uid=4242 in log, got: %s", logged)
	}
}

// TestFilterByUID_PidUIDErrorKeepsPID asserts the fail-open semantics:
// if pidUIDFn errors (kernel race, EPERM, process gone), the pid is
// retained rather than silently dropped. Cascade-killing too many is
// safer than too few — kernel signal delivery will EPERM on foreign
// uids anyway.
func TestFilterByUID_PidUIDErrorKeepsPID(t *testing.T) {
	const probePID = 99003

	orig := pidUIDFn
	t.Cleanup(func() { pidUIDFn = orig })
	pidUIDFn = func(_ int) (int, error) {
		return 0, errors.New("synthetic probe failure")
	}

	got := filterByUID([]int{probePID}, discardLogger())
	if len(got) != 1 || got[0] != probePID {
		t.Fatalf("filterByUID = %v, want [%d] (fail-open on pidUID error)", got, probePID)
	}
}

// TestFilterByUID_ZeroInput verifies the empty-input fast path emits no
// log and returns nil.
func TestFilterByUID_ZeroInput(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	got := filterByUID(nil, logger)
	if len(got) != 0 {
		t.Errorf("filterByUID(nil) = %v, want empty", got)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output for empty input, got: %s", buf.String())
	}
}

// TestCaptureForService_ZeroOnUnresolvableCommand covers the
// helper's degenerate inputs: empty command and interpreter-wrapped
// command both yield a zero fingerprint (which makes the downstream
// cascade-kill a no-op). The full resolve path is exercised by
// resolve_test.go — this is just the contract test.
func TestCaptureForService_ZeroOnUnresolvableCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		command []string
		workDir string
	}{
		{name: "empty", command: nil, workDir: ""},
		{name: "empty_string", command: []string{""}, workDir: ""},
		{name: "interpreter_go_run", command: []string{"go", "run", "./cmd/foo"}, workDir: "/tmp"},
		{name: "interpreter_bash", command: []string{"bash", "-c", "echo hi"}, workDir: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fp := CaptureForService(tc.command, tc.workDir, discardLogger())
			if !fp.IsZero() {
				t.Errorf("expected zero fingerprint for %v, got %+v", tc.command, fp)
			}
		})
	}
}
