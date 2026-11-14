package standby

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReadFlag(t *testing.T) {
	p := PathsIn(t.TempDir())
	if st := p.Read(); st.Active {
		t.Fatalf("no flag: %+v", st)
	}
	for content, want := range map[string]State{
		"cerberus-substrate\n":        {Active: true, Resource: "cerberus-substrate"},
		"  cerberus-substrate  \nx\n": {Active: true, Resource: "cerberus-substrate"},
		"":                            {Active: true, Problem: "the flag file names no resource"},
		"../etc\n":                    {Active: true, Problem: `the flag file's first line "../etc" is not a resource id`},
	} {
		if err := os.WriteFile(p.Flag, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := p.Read(); got != want {
			t.Fatalf("flag %q: got %+v, want %+v", content, got, want)
		}
	}
}

// Watch reports the state at start and every change after it. A flag caught
// half-written reads as standby naming no resource (a full fence) until the
// next check sees the name, so the test looks for the states that matter.
func TestWatchReportsEveryChange(t *testing.T) {
	p := PathsIn(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var seen []State
	done := make(chan struct{})
	go func() {
		defer close(done)
		Watch(ctx, p, 5*time.Millisecond, func(s State) { mu.Lock(); seen = append(seen, s); mu.Unlock() })
	}()
	waitFor := func(want State) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			last := State{Active: true, Problem: "none yet"}
			if len(seen) > 0 {
				last = seen[len(seen)-1]
			}
			mu.Unlock()
			if last == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("never saw %+v in %+v", want, seen)
	}
	waitFor(State{})
	tmp := p.Flag + ".tmp"
	if err := os.WriteFile(tmp, []byte("res-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Flag); err != nil {
		t.Fatal(err)
	}
	waitFor(State{Active: true, Resource: "res-a"})
	if err := os.Remove(p.Flag); err != nil {
		t.Fatal(err)
	}
	waitFor(State{})
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			t.Fatalf("reported an unchanged state twice: %+v", seen)
		}
	}
}

func TestSessionsAreWrittenOnce(t *testing.T) {
	p := PathsIn(t.TempDir())
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	wrote, err := p.WriteSessionsOnce([]string{"a", "b"}, now)
	if err != nil || !wrote {
		t.Fatalf("first write: %v %v", wrote, err)
	}
	wrote, err = p.WriteSessionsOnce(nil, now.Add(time.Hour))
	if err != nil || wrote {
		t.Fatalf("second write must leave the first list: %v %v", wrote, err)
	}
	s, ok, err := p.ReadSessions()
	if err != nil || !ok || len(s.IDs) != 2 || s.IDs[0] != "a" || !s.WrittenAt.Equal(now) {
		t.Fatalf("read: %+v %v %v", s, ok, err)
	}
	for _, d := range []string{p.Dir, p.SessionsPath()} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode %v", d, fi.Mode().Perm())
		}
	}
	if err := p.ClearSessions(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := p.ReadSessions(); ok {
		t.Fatal("cleared list still there")
	}
	if p.AuditDir() != filepath.Join(p.Dir, "audit") {
		t.Fatal(p.AuditDir())
	}
}
