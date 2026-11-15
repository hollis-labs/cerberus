package local

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
)

// showOnly answers `systemctl --user show` with a fixed record and refuses anything else, so
// the test proves the helper only reads.
type showOnly struct {
	out   string
	calls []string
}

func (s *showOnly) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	if len(args) < 2 || args[0] != "--user" || args[1] != "show" {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	return []byte(s.out), nil
}

func TestObserveSystemdDetail(t *testing.T) {
	res := &domain.Resource{ID: "api", ProjectID: "demo", Type: domain.ResourceProcess, Connector: "local",
		Config: map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/bin/true"}}}
	r := &showOnly{out: "Id=x.service\nLoadState=loaded\nActiveState=activating\nSubState=auto-restart\nUnitFileState=enabled\n" +
		"Result=exit-code\nMainPID=0\nExecMainCode=1\nExecMainStatus=3\nNRestarts=4\n"}
	state, unit, rec, err := ObserveSystemdDetail(context.Background(), r, t.TempDir(), res)
	if err != nil {
		t.Fatal(err)
	}
	if state != domain.StateFailed || unit != "com.hollis-labs.cerberus.demo.api.service" {
		t.Fatalf("state %q unit %q", state, unit)
	}
	if rec.LoadState != "loaded" || rec.ActiveState != "activating" || rec.SubState != "auto-restart" ||
		rec.UnitFileState != "enabled" || rec.Result != "exit-code" || rec.Restarts != 4 ||
		rec.ExecMainStatus == nil || *rec.ExecMainStatus != 3 {
		t.Fatalf("record %+v", rec)
	}
	if !strings.Contains(rec.Diagnosis, "crash-looping") || len(rec.Highlights) == 0 {
		t.Fatalf("diagnosis %q highlights %v", rec.Diagnosis, rec.Highlights)
	}
	if rec.Raw != "" || rec.Journal != "" {
		t.Fatalf("raw or journal handed out: %q %q", rec.Raw, rec.Journal)
	}
	if len(r.calls) != 1 || !strings.HasPrefix(r.calls[0], "systemctl --user show com.hollis-labs.cerberus.demo.api.service") {
		t.Fatalf("calls %v", r.calls)
	}
}
