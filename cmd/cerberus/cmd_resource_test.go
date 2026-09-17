package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// newDeployTestCmd builds a throwaway cobra.Command with the same flag pair
// as resourceDeployCmd so resolveDeployFlags can be exercised without
// touching the real deploy RunE.
func newDeployTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "deploy"}
	cmd.Flags().Bool("install-after-build", true, "")
	cmd.Flags().Bool("no-install-after-build", false, "")
	return cmd
}

func TestResolveDeployFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		wantOverride *bool
		wantErr      bool
	}{
		{
			name:         "no flags = no override",
			args:         []string{},
			wantOverride: nil,
		},
		{
			name:         "--install-after-build bare = override true",
			args:         []string{"--install-after-build"},
			wantOverride: ptrBool(true),
		},
		{
			name:         "--install-after-build=true = override true",
			args:         []string{"--install-after-build=true"},
			wantOverride: ptrBool(true),
		},
		{
			name:         "--install-after-build=false = override false",
			args:         []string{"--install-after-build=false"},
			wantOverride: ptrBool(false),
		},
		{
			name:         "--no-install-after-build = override false",
			args:         []string{"--no-install-after-build"},
			wantOverride: ptrBool(false),
		},
		{
			name:    "both flags = error",
			args:    []string{"--install-after-build", "--no-install-after-build"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := newDeployTestCmd()
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatalf("ParseFlags(%v) failed: %v", tc.args, err)
			}
			opts, err := resolveDeployFlags(cmd)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveDeployFlags err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			got := cerbapi.ApplyDeployResourceOptions(opts).InstallAfterBuildOverride
			switch {
			case tc.wantOverride == nil && got != nil:
				t.Fatalf("expected no override, got *%v", *got)
			case tc.wantOverride != nil && got == nil:
				t.Fatalf("expected override=*%v, got nil", *tc.wantOverride)
			case tc.wantOverride != nil && got != nil && *tc.wantOverride != *got:
				t.Fatalf("override = %v, want %v", *got, *tc.wantOverride)
			}
		})
	}
}

func ptrBool(b bool) *bool { return &b }
