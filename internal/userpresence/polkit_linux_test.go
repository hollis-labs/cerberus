//go:build linux

package userpresence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fakePolkit(t *testing.T, codes ...int) (*Polkit, *[][]string) {
	t.Helper()
	var calls [][]string
	p := &Polkit{
		setup:     func() error { return nil },
		startTime: func(int) (uint64, error) { return 123, nil },
		run: func(_ context.Context, args ...string) int {
			calls = append(calls, append([]string{}, args...))
			if len(calls) > len(codes) {
				t.Error("unexpected authentication call")
				return -1
			}
			return codes[len(calls)-1]
		},
	}
	return p, &calls
}

func TestPolkitRequiresFreshAuthenticationForTheSocketPeer(t *testing.T) {
	ctx := WithPeerSubject(context.Background(), 42, 1000, 123)
	p, calls := fakePolkit(t, 2, 0, 2)
	if err := p.Verify(ctx, "enroll"); err != nil {
		t.Fatal(err)
	}
	base := []string{"--action-id", PolkitAction, "--process", "42,123,1000"}
	want := [][]string{base, append(append([]string{}, base...), "--allow-user-interaction"), base}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls: %v", *calls)
	}
	for _, tc := range []struct {
		name  string
		codes []int
		want  error
	}{
		{"automatic grant", []int{0}, ErrUnavailable},
		{"policy missing", []int{127}, ErrUnavailable},
		{"policy denies", []int{1}, ErrUnavailable},
		{"password refused", []int{2, 1}, ErrRefused},
		{"canceled", []int{2, 3}, ErrRefused},
		{"no terminal agent", []int{2, 2}, ErrUnavailable},
		{"authority failed", []int{2, 127}, ErrUnavailable},
		{"cached after authentication", []int{2, 0, 0}, ErrUnavailable},
		{"authority lost after authentication", []int{2, 0, 127}, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := fakePolkit(t, tc.codes...)
			if err := p.Verify(ctx, "enroll"); !errors.Is(err, tc.want) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestPolkitRefusesMissingOrChangedKernelIdentity(t *testing.T) {
	for _, ctx := range []context.Context{
		context.Background(), WithPeerSubject(context.Background(), 0, 1000, 123),
		WithPeerSubject(context.Background(), 42, 0, 123), WithPeerSubject(context.Background(), 42, 1000, 0),
		WithPeerSubject(context.Background(), 42, 1000, 456),
	} {
		p, calls := fakePolkit(t)
		if err := p.Verify(ctx, "enroll"); err == nil || len(*calls) != 0 {
			t.Fatalf("unauthenticated identity: %v, %v", err, *calls)
		}
	}
	p, calls := fakePolkit(t, 2, 0)
	lookups := 0
	p.startTime = func(int) (uint64, error) {
		lookups++
		if lookups == 1 {
			return 123, nil
		}
		return 456, nil
	}
	if err := p.Verify(WithPeerSubject(context.Background(), 42, 1000, 123), "enroll"); !errors.Is(err, ErrRefused) || len(*calls) != 2 {
		t.Fatalf("PID reuse: %v", err)
	}
}

func TestPolkitPrerequisitesAndCancellationFailClosed(t *testing.T) {
	p, calls := fakePolkit(t)
	p.setup = func() error { return errors.New("missing system policy") }
	ctx := WithPeerSubject(context.Background(), 42, 1000, 123)
	if err := p.Verify(ctx, "enroll"); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "enrollment-policy") || len(*calls) != 0 {
		t.Fatal(err)
	}
	if st := p.EnrollmentSetup(); st.Available || !strings.Contains(st.NextStep, "SSH") {
		t.Fatalf("setup: %+v", st)
	}
	p.setup = func() error { return nil }
	ctx, cancel := context.WithCancel(ctx)
	p.run = func(_ context.Context, args ...string) int {
		if args[len(args)-1] == "--allow-user-interaction" {
			cancel()
			return 0
		}
		return 2
	}
	defer cancel()
	if err := p.Verify(ctx, "enroll"); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
}

func TestPolkitPolicyRequiresAuthenticationWithoutRetention(t *testing.T) {
	for _, mode := range []string{"auth_self", "yes", "no", "auth_self_keep", "auth_admin_keep", ""} {
		doc := fmt.Sprintf(`<policyconfig><action id="%s"><defaults><allow_any>%s</allow_any><allow_inactive>auth_self</allow_inactive><allow_active>auth_self</allow_active></defaults></action></policyconfig>`, PolkitAction, mode)
		err := validatePolkitPolicy(strings.NewReader(doc))
		if (err == nil) != (mode == "auth_self") {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
}

func TestProcessStartTimeHandlesParenthesesAndRejectsMalformedStat(t *testing.T) {
	stat := "42 (a name with ) parentheses) S " + strings.Repeat("0 ", 18) + "456 0"
	if got, err := parseStartTime(stat); err != nil || got != 456 {
		t.Fatalf("got %d: %v", got, err)
	}
	for _, bad := range []string{"", "42 (name) S 0", strings.Replace(stat, "456", "not-a-time", 1)} {
		if _, err := parseStartTime(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if start, err := ProcessStartTime(os.Getpid()); err != nil || start == 0 {
		t.Fatalf("own process: %d %v", start, err)
	}
}

func TestTerminalAgentStopsAndReapsOnBothSuccessAndCancellation(t *testing.T) {
	// This is an inert child, never polkit or a PAM helper. It validates
	// registration ordering and cleanup without reaching a live integration.
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec 3>&-\nread line\n"), 0700); err != nil { //nolint:gosec // inert executable fixture
		t.Fatal(err)
	}
	in, keep, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = keep.Close() }()
	for _, cancelFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		stop, err := startTerminalAgent(ctx, path, "42,123", in, os.Stderr)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if cancelFirst {
			cancel()
		}
		done := make(chan struct{})
		go func() { stop(); stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("agent cleanup blocked")
		}
		cancel()
	}
}

func TestPolkitRejectsWritableSystemFilesAndSymlinkTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untrusted")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil { //nolint:gosec // deliberately insecure fixture
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, link, filepath.Dir(path)} {
		if err := trustedSystemFile(candidate); err == nil {
			t.Fatalf("trusted unsafe path %s", candidate)
		}
	}
}
