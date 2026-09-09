package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/cerbapi"
)

func TestPrintResolveNoticeSilentWhenClean(t *testing.T) {
	for name, diag := range map[string]*cerbapi.ResolveDiagnostics{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printResolveNotice(&buf, diag)
			if buf.Len() != 0 {
				t.Errorf("notice on a clean resolve: %q", buf.String())
			}
		})
	}
}

func TestPrintResolveNoticeReportsSkipsAndWarnings(t *testing.T) {
	tests := []struct {
		name string
		diag cerbapi.ResolveDiagnostics
		want []string
		omit []string
	}{
		{
			name: "skips only",
			diag: cerbapi.ResolveDiagnostics{Skipped: 2, SkippedOwners: []string{"torque", "tether"}},
			want: []string{"2 config(s) skipped", "skipped: torque, tether", "cerberus registry health"},
			omit: []string{"with warnings"},
		},
		{
			name: "warnings only",
			diag: cerbapi.ResolveDiagnostics{Warned: 1, WarnedOwners: []string{"futureapp"}},
			want: []string{"1 with warnings", "warnings: futureapp"},
			omit: []string{"skipped"},
		},
		{
			name: "both",
			diag: cerbapi.ResolveDiagnostics{
				Skipped: 2, SkippedOwners: []string{"torque", "tether"},
				Warned: 1, WarnedOwners: []string{"futureapp"},
			},
			want: []string{"2 config(s) skipped, 1 with warnings", "skipped: torque, tether", "warnings: futureapp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printResolveNotice(&buf, &tt.diag)
			got := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("notice %q missing %q", got, want)
				}
			}
			for _, omit := range tt.omit {
				if strings.Contains(got, omit) {
					t.Errorf("notice %q mentions %q it should not", got, omit)
				}
			}
		})
	}
}

// The count alone is enough to fire the notice. Owners are a
// convenience the daemon may omit; losing them must not lose the
// warning, which is the only signal the operator gets that the list is
// short.
func TestPrintResolveNoticeFiresWithoutOwners(t *testing.T) {
	var buf bytes.Buffer
	printResolveNotice(&buf, &cerbapi.ResolveDiagnostics{Skipped: 3})
	got := buf.String()
	if !strings.Contains(got, "3 config(s) skipped") {
		t.Errorf("notice = %q, want the skipped count", got)
	}
	if !strings.Contains(got, "cerberus registry health") {
		t.Errorf("notice = %q, want the next step", got)
	}
}
