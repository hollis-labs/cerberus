package procscan

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCapture_Existing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	if err := os.WriteFile(path, []byte("payload"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write: %v", err)
	}
	fp, err := Capture(path)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if fp.IsZero() {
		t.Fatal("expected non-zero fingerprint")
	}
	if fp.Path != path {
		t.Errorf("Path = %q, want %q", fp.Path, path)
	}
	if fp.Inode == 0 {
		t.Error("Inode unexpectedly zero")
	}
}

func TestCapture_Missing(t *testing.T) {
	t.Parallel()
	_, err := Capture(filepath.Join(t.TempDir(), "no-such-file"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCapture_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := Capture("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestCapture_DetectsReplacement(t *testing.T) {
	// The whole point of the package: a fingerprint captured before
	// `go install` should differ from a fingerprint captured after,
	// because the new file has a new inode.
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	if err := os.WriteFile(path, []byte("original"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write: %v", err)
	}
	before, err := Capture(path)
	if err != nil {
		t.Fatalf("Capture before: %v", err)
	}
	// Simulate `go install`: write to a temp file, then rename over the
	// target. This unlinks the old inode and creates a new one — the
	// same primitive Go's install path uses.
	tmp := path + ".new"
	if writeErr := os.WriteFile(tmp, []byte("replacement"), 0o755); writeErr != nil { //nolint:gosec // test fixture
		t.Fatalf("write tmp: %v", writeErr)
	}
	if renameErr := os.Rename(tmp, path); renameErr != nil {
		t.Fatalf("rename: %v", renameErr)
	}
	after, err := Capture(path)
	if err != nil {
		t.Fatalf("Capture after: %v", err)
	}
	if before.Inode == after.Inode {
		t.Errorf("expected inode to change after replacement, got %d both times", before.Inode)
	}
}

// fakeEnumerator returns canned data for PID enumeration. Used to
// drive PIDsByFingerprint without touching the OS.
type fakeEnumerator struct {
	pids       []int
	prints     map[int]BinaryFingerprint
	pidsErr    error
	printErr   map[int]error
	pidsCalls  int
	printCalls int
	mu         sync.Mutex
}

func (f *fakeEnumerator) PIDs() ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pidsCalls++
	return f.pids, f.pidsErr
}

func (f *fakeEnumerator) Fingerprint(pid int) (BinaryFingerprint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.printCalls++
	if err, ok := f.printErr[pid]; ok && err != nil {
		return BinaryFingerprint{}, err
	}
	fp, ok := f.prints[pid]
	if !ok {
		return BinaryFingerprint{}, errors.New("not found")
	}
	return fp, nil
}

func TestPIDsByFingerprint_MatchesByDevInode(t *testing.T) {
	t.Parallel()
	target := BinaryFingerprint{Device: 1, Inode: 100, Path: "/bin/clockwork"}
	other := BinaryFingerprint{Device: 1, Inode: 200, Path: "/bin/something-else"}
	enum := &fakeEnumerator{
		pids: []int{1001, 1002, 1003, 1004},
		prints: map[int]BinaryFingerprint{
			1001: target,
			1002: other,
			1003: target,
			1004: {Device: 2, Inode: 100}, // same inode, different device
		},
	}
	got, err := pidsByFingerprintWith(enum, target, 99999, discardLogger())
	if err != nil {
		t.Fatalf("PIDsByFingerprint: %v", err)
	}
	want := []int{1001, 1003}
	if len(got) != len(want) {
		t.Fatalf("matched %v, want %v", got, want)
	}
	for i, pid := range want {
		if got[i] != pid {
			t.Errorf("got[%d] = %d, want %d", i, got[i], pid)
		}
	}
}

func TestPIDsByFingerprint_ExcludesSelf(t *testing.T) {
	t.Parallel()
	target := BinaryFingerprint{Device: 1, Inode: 100, Path: "/bin/x"}
	enum := &fakeEnumerator{
		pids: []int{42, 7},
		prints: map[int]BinaryFingerprint{
			42: target,
			7:  target,
		},
	}
	got, err := pidsByFingerprintWith(enum, target, 42, discardLogger())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0] != 7 {
		t.Errorf("got %v, want [7]", got)
	}
}

func TestPIDsByFingerprint_FingerprintErrorsTreatedAsNoMatch(t *testing.T) {
	t.Parallel()
	target := BinaryFingerprint{Device: 1, Inode: 100, Path: "/bin/x"}
	enum := &fakeEnumerator{
		pids: []int{10, 20, 30},
		prints: map[int]BinaryFingerprint{
			10: target,
			30: target,
		},
		printErr: map[int]error{
			20: errors.New("permission denied"),
		},
	}
	got, err := pidsByFingerprintWith(enum, target, 99, discardLogger())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 || got[0] != 10 || got[1] != 30 {
		t.Errorf("got %v, want [10 30]", got)
	}
}

func TestPIDsByFingerprint_ZeroFingerprintReturnsNil(t *testing.T) {
	t.Parallel()
	got, err := PIDsByFingerprint(BinaryFingerprint{}, discardLogger())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestPIDsByFingerprint_EnumeratorErrorPropagated(t *testing.T) {
	t.Parallel()
	enum := &fakeEnumerator{pidsErr: errors.New("enum failed")}
	_, err := pidsByFingerprintWith(enum, BinaryFingerprint{Device: 1, Inode: 1}, 99, discardLogger())
	if err == nil {
		t.Fatal("expected error")
	}
}

// fakeKiller drives KillAndWait without spawning real processes.
type fakeKiller struct {
	mu          sync.Mutex
	live        map[int]bool
	exitOnTerm  map[int]bool
	exitOnKill  map[int]bool
	signalErr   map[int]error
	signalCalls []sigCall
}

type sigCall struct {
	PID int
	Sig syscall.Signal
}

func newFakeKiller() *fakeKiller {
	return &fakeKiller{
		live:       map[int]bool{},
		exitOnTerm: map[int]bool{},
		exitOnKill: map[int]bool{},
		signalErr:  map[int]error{},
	}
}

func (f *fakeKiller) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signalCalls = append(f.signalCalls, sigCall{PID: pid, Sig: sig})
	if err, ok := f.signalErr[pid]; ok && err != nil {
		return err
	}
	switch sig { //nolint:exhaustive // only TERM/KILL relevant to cascade-kill
	case syscall.SIGTERM:
		if f.exitOnTerm[pid] {
			f.live[pid] = false
		}
	case syscall.SIGKILL:
		if f.exitOnKill[pid] {
			f.live[pid] = false
		}
	}
	return nil
}

func (f *fakeKiller) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[pid]
}

func TestKillAndWait_SigtermSufficient(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	k.live[100] = true
	k.exitOnTerm[100] = true

	out := killAndWait([]int{100}, 200*time.Millisecond, k, discardLogger())
	if len(out) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(out))
	}
	if !out[0].SentTerm || out[0].SentKill {
		t.Errorf("expected SIGTERM-only, got %+v", out[0])
	}
	if !out[0].Exited {
		t.Errorf("expected Exited=true, got %+v", out[0])
	}
	if out[0].Err != nil {
		t.Errorf("unexpected err: %v", out[0].Err)
	}
}

func TestKillAndWait_EscalatesToSigkill(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	k.live[100] = true
	// SIGTERM does NOT exit. SIGKILL does.
	k.exitOnKill[100] = true

	out := killAndWait([]int{100}, 50*time.Millisecond, k, discardLogger())
	if !out[0].SentTerm || !out[0].SentKill {
		t.Errorf("expected SIGTERM+SIGKILL, got %+v", out[0])
	}
	if !out[0].Exited {
		t.Errorf("expected Exited=true, got %+v", out[0])
	}
}

func TestKillAndWait_ReportsStubbornProcess(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	k.live[100] = true
	// Neither signal makes the process exit.

	out := killAndWait([]int{100}, 30*time.Millisecond, k, discardLogger())
	if !out[0].SentKill {
		t.Errorf("expected SIGKILL sent, got %+v", out[0])
	}
	if out[0].Exited {
		t.Errorf("expected Exited=false")
	}
	if out[0].Err == nil {
		t.Error("expected Err to be set for stubborn process")
	}
}

func TestKillAndWait_AlreadyDead(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	// PID 100 is not in live map → Alive returns false.

	out := killAndWait([]int{100}, 50*time.Millisecond, k, discardLogger())
	if out[0].SentTerm || out[0].SentKill {
		t.Errorf("expected no signals sent for dead pid, got %+v", out[0])
	}
	if !out[0].Exited {
		t.Errorf("expected Exited=true for dead pid")
	}
}

func TestKillAndWait_InvalidPid(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	out := killAndWait([]int{0, -1}, 50*time.Millisecond, k, discardLogger())
	for i, o := range out {
		if o.Err == nil {
			t.Errorf("out[%d]: expected err for invalid pid", i)
		}
	}
}

func TestKillAndWait_SignalErrorRecorded(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	k.live[100] = true
	k.signalErr[100] = errors.New("ESRCH")

	out := killAndWait([]int{100}, 30*time.Millisecond, k, discardLogger())
	if out[0].Err == nil {
		t.Error("expected err when SIGTERM delivery fails")
	}
}

func TestKillAndWait_OrderMatchesInput(t *testing.T) {
	t.Parallel()
	k := newFakeKiller()
	for _, pid := range []int{42, 17, 99} {
		k.live[pid] = true
		k.exitOnTerm[pid] = true
	}
	out := killAndWait([]int{42, 17, 99}, 30*time.Millisecond, k, discardLogger())
	for i, want := range []int{42, 17, 99} {
		if out[i].PID != want {
			t.Errorf("out[%d].PID = %d, want %d", i, out[i].PID, want)
		}
	}
}
