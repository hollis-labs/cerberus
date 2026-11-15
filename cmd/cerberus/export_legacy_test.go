package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/legacyexport"
)

// export-legacy is not a data command: run as a process, boot creates nothing, and the command
// needs no server. With no data directory there is no handoff, so it refuses with the report on
// stdout, exit 1, and creates nothing.
func TestExportLegacyNeedsNoServerAndRefusesWithoutAHandoff(t *testing.T) {
	s := newSandbox(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := s.command(ctx, "export-legacy", "--since", "sha256:"+strings.Repeat("0", 64), "-o", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("export-legacy without a data directory: %v\n%s", err, stdout.String())
	}
	var rep legacyexport.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not the JSON report: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	if rep.Result != legacyexport.ResultRefused || len(rep.Refusals) == 0 {
		t.Fatalf("report: %s %v", rep.Result, rep.Refusals)
	}
	if _, err := os.Lstat(s.data); !os.IsNotExist(err) {
		t.Errorf("%s exists after a refused export-legacy (%v)", s.data, err)
	}

	cmd = s.command(ctx, "export-legacy", "--check-legacy-files")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "--d3-copy") {
		t.Fatalf("--check-legacy-files without --d3-copy: %v\n%s", err, out)
	}
}

// A pending class B step exits 3, wrapped or not; anything else exits 1.
func TestExitCodeCarriesThePendingClassBStep(t *testing.T) {
	pending := &legacyexport.PendingError{Handoff: "sha256:x", Cause: "down"}
	if exitCode(pending) != legacyexport.ExitTelemetryPending || exitCode(fmt.Errorf("run: %w", pending)) != 3 {
		t.Fatal("a pending class B step does not exit 3")
	}
	if exitCode(errors.New("refused")) != 1 || exitCode(legacyexport.ErrRefused) != 1 {
		t.Fatal("a refusal does not exit 1")
	}
}

// import-standby is not a data command either: with no data directory it refuses (no main key)
// and creates nothing.
func TestImportStandbyNeedsNoServerAndCreatesNothingWhenRefused(t *testing.T) {
	s := newSandbox(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	archive := t.TempDir()
	if err := os.MkdirAll(archive+"/audit", 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := s.command(ctx, "import-standby", "--from", archive, "-o", "json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil {
		t.Fatalf("import-standby without a data directory succeeded:\n%s", stdout.String())
	}
	var rep legacyexport.StandbyReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil || rep.Result != legacyexport.ResultRefused {
		t.Fatalf("report: %v %s %v", err, rep.Result, rep.Refusals)
	}
	if _, err := os.Lstat(s.data); !os.IsNotExist(err) {
		t.Errorf("%s exists after a refused import-standby (%v)", s.data, err)
	}
}
