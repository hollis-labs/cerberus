package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// resolveDiagnoser is the shape both list paths already have on hand:
// the daemon socket client and the in-process runtime service. Naming
// it here lets one notice helper serve either without the caller
// caring which one answered.
type resolveDiagnoser interface {
	ResolveDiagnostics(ctx context.Context) (*cerbapi.ResolveDiagnostics, error)
}

// printResolveNotice writes a trailing notice when registry resolution
// dropped a config or resolved one with warnings.
//
// This is the whole point of the notice: a list that silently omits a
// skipped config is indistinguishable from a list of everything there
// is. On 2026-05-25 the registry was healthy and the configs were being
// dropped, and the operator saw a bare "No resources found". The notice
// never changes the list — it explains why the list may be short.
//
// It goes to stderr so a piped or grepped list stays clean, matching
// how `cerberus config validate` reports the same class of finding.
func printResolveNotice(w io.Writer, diag *cerbapi.ResolveDiagnostics) {
	if diag == nil || diag.Clean() {
		return
	}
	var parts []string
	if diag.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d config(s) skipped", diag.Skipped))
	}
	if diag.Warned > 0 {
		parts = append(parts, fmt.Sprintf("%d with warnings", diag.Warned))
	}
	fmt.Fprintf(w, "\n%s\n", strings.Join(parts, ", "))
	if owners := noticeOwners(diag); owners != "" {
		fmt.Fprintf(w, "  %s\n", owners)
	}
	fmt.Fprintln(w, "  run 'cerberus registry health' for detail")
}

// noticeOwners names the configs behind the counts. The count alone
// tells the operator something is wrong; the owner tells them where to
// look without a second command.
func noticeOwners(diag *cerbapi.ResolveDiagnostics) string {
	var parts []string
	if len(diag.SkippedOwners) > 0 {
		parts = append(parts, "skipped: "+strings.Join(diag.SkippedOwners, ", "))
	}
	if len(diag.WarnedOwners) > 0 {
		parts = append(parts, "warnings: "+strings.Join(diag.WarnedOwners, ", "))
	}
	return strings.Join(parts, "; ")
}

// reportResolveNotice fetches diagnostics from whichever surface served
// the list and prints the notice.
//
// Best-effort by design: a diagnostic read that fails must not turn a
// successful list into an error. A failure here means the notice is
// absent, which is the behavior every release before this one had.
func reportResolveNotice(ctx context.Context, d resolveDiagnoser) {
	if d == nil {
		return
	}
	diag, err := d.ResolveDiagnostics(ctx)
	if err != nil {
		return
	}
	printResolveNotice(os.Stderr, diag)
}
