package main

import (
	"fmt"
	"os"
	"time"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/opsref"
	"github.com/hollis-labs/cerberus/internal/opssink"
	"github.com/spf13/cobra"
)

// addOpsFlags adds --ops-id, --workstream and --task to a command.
func addOpsFlags(cmd *cobra.Command) {
	cmd.Flags().String("ops-id", "", "the Ops item this deploy belongs to (else $OPS_ID, then the ops mapping in events.yaml)")
	cmd.Flags().String("workstream", "", "the workstream this deploy belongs to, workstream:<ULID> (else $TETHER_WORKSTREAM_ID, then the ops mapping)")
	cmd.Flags().String("task", "", "the Torque task this deploy belongs to (else $TETHER_TASK_ID)")
}

// opsRefsFromCommand resolves the identifiers the caller can see: the flag, then
// the caller's environment, each separately. The serving process completes the
// ones still empty from its configured mapping.
func opsRefsFromCommand(cmd *cobra.Command) opsref.Refs {
	flags := opsref.Refs{}
	flags.OpsID, _ = cmd.Flags().GetString("ops-id")
	flags.WorkstreamID, _ = cmd.Flags().GetString("workstream")
	flags.Task, _ = cmd.Flags().GetString("task")
	return flags.Fill(opsref.FromEnv(os.Getenv))
}

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Report deploys made outside `resource deploy`",
}

var (
	deployRecordResource, deployRecordRevision, deployRecordOutcome, deployRecordHealth string
)

var deployRecordCmd = &cobra.Command{
	Use:   "record --resource <id> --revision <sha|tag> --outcome deployed|deploy_failed|rolled_back --health <text>",
	Short: "Record a deploy done outside `resource deploy`",
	Long: `Records the outcome of a deploy that Cerberus did not run itself (its own
release scripts for the Tether hub, outposts and Surface). It writes the same
audit record that ` + "`resource deploy`" + ` writes, which drives the same
deploy.state_changed event, and posts the outcome to Ops when ops.enabled is
set. The post is best effort: an Ops refusal or outage is logged and never makes
this command fail.

Nothing is deployed or restarted; this reports a fact.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if deployRecordResource == "" || deployRecordOutcome == "" {
			return fmt.Errorf("deploy record: --resource and --outcome are required")
		}
		if !opssink.ValidOutcome(deployRecordOutcome) {
			return fmt.Errorf("deploy record: --outcome %q is not one of deployed, deploy_failed, rolled_back", deployRecordOutcome)
		}
		svc := newResourceRuntimeService()
		rec, err := svc.RecordDeploy(inProcessContext(cmd.Context()), cerbapi.DeployRecord{
			Resource: deployRecordResource, Revision: deployRecordRevision,
			Outcome: deployRecordOutcome, Health: deployRecordHealth,
			Refs: opsRefsFromCommand(cmd),
		})
		if err != nil {
			return err
		}
		// The post runs in the background; this process is about to exit.
		app.WaitOps(30 * time.Second)
		fmt.Fprintf(cmd.OutOrStdout(), "Recorded %s %s for %s (operation %s)\n", deployRecordOutcome, deployRecordRevision, deployRecordResource, rec.OperationID)
		return nil
	},
}

func init() {
	deployRecordCmd.Flags().StringVar(&deployRecordResource, "resource", "", "the resource that was deployed")
	deployRecordCmd.Flags().StringVar(&deployRecordRevision, "revision", "", "the commit or tag that was deployed")
	deployRecordCmd.Flags().StringVar(&deployRecordOutcome, "outcome", "", "deployed, deploy_failed or rolled_back")
	deployRecordCmd.Flags().StringVar(&deployRecordHealth, "health", "", "what the health check said (a URL and status, or text)")
	addOpsFlags(deployRecordCmd)
	deployCmd.AddCommand(deployRecordCmd)
	rootCmd.AddCommand(deployCmd)
}
