package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// auditCmd is `cerberus audit`: verify, the old build's check of the audit chain (contiguous,
// hash-linked, its anchored head present), plus the owner-session seals on the approvals store
// (decision 0.1, @archie's ruling (b)), where a sealed decision that does not verify fails it
// and a decision from before the cutover is listed as legacy; and tail and query, the old reads
// (cli_audit.go). Its -o is old's, for tail and query.
func auditCmd() *cobra.Command {
	var output string
	group := &cobra.Command{Use: "audit", Short: "Read and verify the operation audit log"}
	group.PersistentFlags().StringVarP(&output, "output", "o", outputText, "output format: text|json (JSON is one record per line, as written)")
	group.AddCommand(auditReadCommands(auditDataDir(func() (string, error) {
		dirs, err := datadir.Resolve(os.Getenv)
		return dirs.Data, err
	}), &output)...)
	group.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Verify the audit chain and the approvals' owner-session seals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := datadir.Resolve(os.Getenv)
			if err != nil {
				return err
			}
			return auditVerify(dirs.Data, cmd.OutOrStdout())
		},
	})
	return group
}

// auditVerify checks the chain in <data>/audit against its keychain anchor and the approvals
// in <data>/approvals against the chain's digest key.
func auditVerify(data string, out io.Writer) error {
	dir := filepath.Join(data, "audit")
	audit.SetAnchor(dir, auditchain.KeychainAnchor{Dir: dir})
	checked, err := audit.Check(dir)
	if err != nil {
		return err
	}
	if err = checked.Err(); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	fmt.Fprintf(out, "audit chain intact: %d records in %d month file(s)\n", len(checked.Records), len(files))
	key, err := audit.OpenExistingDigester(dir)
	if errors.Is(err, audit.ErrNoDigestKey) {
		fmt.Fprintln(out, "approvals: no digest key, so no owner-session decision to verify")
		return nil
	}
	if err != nil {
		return err
	}
	report, err := gate.VerifyApprovals(filepath.Join(data, "approvals"), key)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "approvals: %d owner-session decision(s) verified, %d legacy (pre-cutover, accepted)\n", len(report.Verified), len(report.Legacy))
	for _, id := range report.Legacy {
		fmt.Fprintf(out, "  legacy: %s\n", id)
	}
	return report.Err()
}
