package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/app"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var resourceCmd = &cobra.Command{
	Use:   "resource",
	Short: "V2 resource management",
	Long:  "Manages v2 resources. After editing source, use deploy to build and activate it, or ensure-fresh --force. Without --force, ensure-fresh checks existing binary/install drift; it cannot detect unbuilt source edits. Lower-level verbs: deploy for build+activate, apply for already-built activation, reload for restart-only (NO rebuild), sync for artifact-copy only, stop for non-destructive stop/pause intent, and remove only for uninstalling runtime state. NOTE: run_from: artifact services run an installed copy under ~/.cerberus/apps/...; building (go/make) or reload/restart does not update them — only deploy/apply/ensure-fresh do.",
}

var resourceListProject string
var resourceListOutput string
var resourceStatusOutput string
var resourceLogsLines int
var resourceLogsStream string
var resourceDeployInstallAfterBuild bool
var resourceDeployNoInstallAfterBuild bool

var resourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List resources",
	Long:  "Lists all resources defined in the v2 resource lane. Use --project to filter by project. For local process resources, the NEXT column is a compact action code; use status, inspect, or doctor for fuller next-step guidance.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if client, err := newResourceSocketClient(); err == nil {
			list, listErr := client.ListResources(cmd.Context(), cerbapi.ResourceListArgs{ProjectID: resourceListProject})
			if listErr == nil {
				// Diagnostics come from whichever surface served the
				// list, so the notice describes the same view of the
				// config tree the rows came from.
				return renderResourceList(cmd.Context(), client, list, resourceListOutput)
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(listErr, &dErr) {
				return listErr
			}
		}
		svc := newResourceRuntimeService()
		list, err := svc.ListResources(cmd.Context(), cerbapi.ResourceListArgs{ProjectID: resourceListProject})
		if err != nil {
			return err
		}
		return renderResourceList(cmd.Context(), svc, list, resourceListOutput)
	},
}

var resourceShowCmd = &cobra.Command{
	Use:   "show <resource-id>",
	Short: "Show resource details",
	Long:  "Shows detailed information about a specific resource.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := registry.ResolveConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		id := args[0]
		for _, r := range v2.Resources {
			if r.ID == id {
				var spec *localconn.ProcessSpec
				if r.Type == string(domain.ResourceProcess) && r.Connector == "local" {
					decoded, err := localconn.SpecFromResourceConfig(r.Config)
					if err == nil {
						spec = &decoded
					}
				}

				fmt.Printf("ID:        %s\n", r.ID)
				fmt.Printf("Name:      %s\n", r.Name)
				fmt.Printf("Type:      %s\n", r.Type)
				fmt.Printf("Project:   %s\n", r.Project)
				fmt.Printf("Connector: %s\n", r.Connector)
				if len(r.Tags) > 0 {
					fmt.Printf("Tags:      %s\n", strings.Join(r.Tags, ", "))
				}
				if len(r.DependsOn) > 0 {
					fmt.Printf("Depends:   %s\n", strings.Join(r.DependsOn, ", "))
				}
				if spec != nil {
					mode := spec.Mode
					if mode == "" {
						mode = localconn.ProcessModeDevSession
					}
					supervisor := spec.Supervisor
					if supervisor == "" {
						supervisor = localconn.ProcessSupervisorAuto
					}
					runFrom := spec.RunFrom
					if runFrom == "" {
						runFrom = localconn.ProcessRunFromWorkspace
					}
					fmt.Printf("Mode:      %s\n", mode)
					fmt.Printf("Supervisor:%s\n", supervisor)
					fmt.Printf("Run From:  %s\n", runFrom)
					if spec.ServiceName != "" {
						fmt.Printf("Service:   %s\n", spec.ServiceName)
					}
					if spec.ArtifactPath != "" {
						fmt.Printf("Artifact:  %s\n", spec.ArtifactPath)
					}
					if spec.InstallRoot != "" {
						fmt.Printf("Install:   %s\n", spec.InstallRoot)
					}
				}
				if len(r.Config) > 0 {
					fmt.Println("Config:")
					data, err := redact.MarshalIndent(r.Config, "  ", "  ")
					if err != nil {
						return err
					}
					fmt.Printf("  %s\n", data)
				}
				return nil
			}
		}

		return fmt.Errorf("resource %q not found in config; run `cerberus resource list` to see available resources", id)
	},
}

var resourceInspectCmd = &cobra.Command{
	Use:   "inspect <resource-id>",
	Short: "Inspect local process resource runtime details",
	Long:  "Shows detailed runtime, install, and log-path details for a local process resource. For os_service resources on macOS, this includes launchd label, plist path, artifact install layout, and log files.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if !cerbapi.SupervisedLocally(res.Type, res.Connector) {
			return cerbapi.UnsupervisedOperationError("inspect", res)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			out, inspectErr := client.GetResourceInspect(cmd.Context(), res.ID)
			if inspectErr == nil {
				printResourceInspect(out)
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(inspectErr, &dErr) {
				return inspectErr
			}
		}

		out, err := newResourceRuntimeService().GetResourceInspect(inProcessContext(cmd.Context()), res.ID)
		if err != nil {
			return err
		}
		printResourceInspect(out)
		return nil
	},
}

var resourceDoctorCmd = &cobra.Command{
	Use:   "doctor <resource-id>",
	Short: "Run explicit runtime checks for a local process resource",
	Long:  "Runs pass/warn/fail checks against the runtime and install surface of a local process resource. For os_service resources on macOS, this validates launchd/plist, install paths, artifact state, and log paths.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if !cerbapi.SupervisedLocally(res.Type, res.Connector) {
			return cerbapi.UnsupervisedOperationError("doctor", res)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			out, doctorErr := client.GetResourceDoctor(cmd.Context(), res.ID)
			if doctorErr == nil {
				printResourceDoctor(out)
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(doctorErr, &dErr) {
				return doctorErr
			}
		}

		out, err := newResourceRuntimeService().GetResourceDoctor(inProcessContext(cmd.Context()), res.ID)
		if err != nil {
			return err
		}
		printResourceDoctor(out)
		return nil
	},
}

var resourceReloadCmd = &cobra.Command{
	Use:   "reload <resource-id>",
	Short: "Kickstart a local process resource through its runtime backend",
	Long:  "Asks the configured runtime backend to restart the current installed resource without syncing artifacts or rewriting service definitions. For launchd-backed os_service resources, this runs launchctl kickstart -k against the loaded service. IMPORTANT: reload does NOT rebuild or re-sync — it relaunches the existing (possibly stale) installed artifact. If your source changed, use `cerberus resource deploy` (or `ensure-fresh`), not reload.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The runtime refuses an unknown or unsupervised id, so the attempt is
		// recorded; the local lookup only adds a hint to that refusal.
		id, hint := args[0], mutationHint("reload", args[0])
		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if client != nil {
			out, reloadErr := client.ReloadResource(cmd.Context(), id, ackOption(cmd))
			if reloadErr != nil {
				return withHint(reloadErr, hint)
			}
			return withHint(printResourceOpResult(out, fmt.Sprintf("Reloaded resource %s", id)), hint)
		}
		out, err := newResourceRuntimeService().ReloadResource(inProcessContext(cmd.Context()), id, ackOption(cmd))
		if err != nil {
			return withHint(err, hint)
		}
		return withHint(printResourceOpResult(out, fmt.Sprintf("Reloaded resource %s", id)), hint)
	},
}

var resourceStopCmd = &cobra.Command{
	Use:   "stop <resource-id>",
	Short: "Stop a local process resource without removing install state",
	Long:  "Stops a local process resource through its configured runtime backend without deleting installed artifacts or plist state. For dev_session resources, this also suppresses auto-restart until the resource is explicitly applied, deployed, or reloaded.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The runtime refuses an unknown or unsupervised id, so the attempt is
		// recorded; the local lookup only adds a hint to that refusal.
		id, hint := args[0], mutationHint("stop", args[0])
		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if client != nil {
			out, stopErr := client.StopResource(cmd.Context(), id, ackOption(cmd))
			if stopErr != nil {
				return withHint(stopErr, hint)
			}
			return withHint(printResourceOpResult(out, fmt.Sprintf("Stopped resource %s", id)), hint)
		}
		out, err := newResourceRuntimeService().StopResource(inProcessContext(cmd.Context()), id, ackOption(cmd))
		if err != nil {
			return withHint(err, hint)
		}
		return withHint(printResourceOpResult(out, fmt.Sprintf("Stopped resource %s", id)), hint)
	},
}

var resourceApplyCmd = &cobra.Command{
	Use:   "apply <resource-id>",
	Short: "Apply an already-built local process resource",
	Long:  "Applies a local process resource using its configured runtime backend. For os_service resources on macOS, this syncs the currently-built artifact and updates the launch agent. It does not run the build command first; use `cerberus resource deploy` when source changes need to be built.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The runtime refuses an unknown or unsupervised id, so the attempt is
		// recorded; the local lookup only adds a hint to that refusal.
		id, hint := args[0], mutationHint("apply", args[0])

		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if client != nil {
			out, applyErr := client.ApplyResource(cmd.Context(), id, ackOption(cmd))
			if applyErr != nil {
				return withHint(applyErr, hint)
			}
			return withHint(printResourceOpResult(out, fmt.Sprintf("Applied resource %s", id)), hint)
		}

		out, err := newResourceRuntimeService().ApplyResource(inProcessContext(cmd.Context()), id, ackOption(cmd))
		if err != nil {
			return withHint(err, hint)
		}
		return withHint(printResourceOpResult(out, fmt.Sprintf("Applied resource %s", id)), hint)
	},
}

var resourceDeployCmd = &cobra.Command{
	Use:   "deploy <resource-id>",
	Short: "Build then apply a local process resource",
	Long:  "Runs the resource's declared build contract first, then applies it through the configured runtime backend. Use this when the intent is source-to-runtime deployment: make the running service match the current source tree. For already-built artifacts, use `cerberus resource apply`. Pass --no-install-after-build to skip the post-build install step for this invocation (useful when bisecting build vs install failures).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The runtime refuses an unknown or unsupervised id, so the attempt is
		// recorded; the local lookup only adds a hint to that refusal.
		id, hint := args[0], mutationHint("deploy", args[0])

		deployOpts, err := resolveDeployFlags(cmd)
		if err != nil {
			return err
		}
		deployOpts = append(deployOpts, ackOption(cmd))

		socketClient, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if socketClient != nil {
			out, deployErr := socketClient.DeployResource(cmd.Context(), id, deployOpts...)
			if deployErr != nil {
				return withHint(deployErr, hint)
			}
			return withHint(printResourceOpResult(out, fmt.Sprintf("Deployed resource %s", id)), hint)
		}

		out, err := newResourceRuntimeService().DeployResource(inProcessContext(cmd.Context()), id, deployOpts...)
		if err != nil {
			return withHint(err, hint)
		}
		return withHint(printResourceOpResult(out, fmt.Sprintf("Deployed resource %s", id)), hint)
	},
}

// ackOption reads a command's --ack, and its --approval where it has one,
// into the mutation's options.
func ackOption(cmd *cobra.Command) cerbapi.MutationOption {
	ack, _ := cmd.Flags().GetBool("ack")
	approvalID, _ := cmd.Flags().GetString("approval")
	return func(o *cerbapi.MutationOpts) {
		cerbapi.WithAcknowledged(ack)(o)
		cerbapi.WithApprovalID(approvalID)(o)
	}
}

var resourcePlanCmd = &cobra.Command{
	Use:   "plan <resource-id> <deploy|apply|reload|stop|sync|remove>",
	Short: "Show the plan an approval of a resource verb would bind to",
	Long: `Compute and print the plan for one resource verb, and its hash, without
running it: the resource's definition (as a keyed digest), the checkout's
commit and dirty flag for deploy, the build output apply and sync would
install, the launch agent apply and deploy would write (as a keyed digest),
and the resource's observed state.

This is the same function an approval is asked for and used with, so the hash
printed is the one an approval of the verb would record. It is recorded like a
dry run, and writes nothing.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, verb := args[0], args[1]
		opts := []cerbapi.MutationOption{ackOption(cmd), cerbapi.WithPlan()}
		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		var runner resourceVerbs = client
		ctx := cmd.Context()
		if client == nil {
			runner, ctx = newResourceRuntimeService(), inProcessContext(ctx)
		}
		verbs := map[string]func(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error){
			"deploy": runner.DeployResource, "apply": runner.ApplyResource, "reload": runner.ReloadResource,
			"stop": runner.StopResource, "sync": runner.SyncResource, "remove": runner.RemoveResource,
		}
		run, ok := verbs[verb]
		if !ok {
			return fmt.Errorf("resource plan: unknown verb %q; want deploy, apply, reload, stop, sync or remove", verb)
		}
		out, err := run(ctx, id, opts...)
		if err != nil {
			return err
		}
		if out == nil || out.Plan == nil {
			return fmt.Errorf("resource plan %s %s: the serving Cerberus returned no plan; it may predate plans, so restart the daemon on this build", id, verb)
		}
		return writeJSON(cmd.OutOrStdout(), out.Plan)
	},
}

// resourceVerbs are the resource mutations a plan can be asked of.
type resourceVerbs interface {
	DeployResource(ctx context.Context, id string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error)
	ApplyResource(ctx context.Context, id string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error)
	ReloadResource(ctx context.Context, id string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error)
	StopResource(ctx context.Context, id string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error)
	SyncResource(ctx context.Context, id string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error)
	RemoveResource(ctx context.Context, id string, opts ...cerbapi.MutationOption) (*cerbapi.OpResult, error)
}

// resolveDeployFlags collapses the --install-after-build / --no-install-after-build
// flag pair into a slice of cerbapi DeployResource options. The flags are
// mutually exclusive; setting both is a user error rather than a precedence
// puzzle, so we reject it up front instead of silently picking a winner.
//
// --install-after-build is value-bearing: --install-after-build (bare),
// --install-after-build=true, and --install-after-build=false each pass
// the actual bound bool through as the override.
//
// --no-install-after-build is a presence-only "hard false" shortcut: any
// explicit set forces the override to false regardless of any parsed value.
var resourceEnsureFreshForce bool

var resourceEnsureFreshCmd = &cobra.Command{
	Use:   "ensure-fresh <resource-id>",
	Short: "Reconcile built-binary drift; --force rebuilds source",
	Long: `Without --force, reconciles the built binaries with installed/running
resources using status advice. It does not check unbuilt source edits.
If activation is unconfirmed, it stops without changing the service and asks
you to verify the running image before retrying activation.
After editing code, use resource deploy or ensure-fresh --force to build,
install and activate the new binary. Apply activates existing build output;
sync only copies it; reload only restarts the current installed binary.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		deployOpts, err := resolveDeployFlags(cmd)
		if err != nil {
			return err
		}
		deployOpts = append(deployOpts, ackOption(cmd))
		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if client != nil {
			res, efErr := cerbapi.EnsureFresh(cmd.Context(), client, id, resourceEnsureFreshForce, deployOpts...)
			if efErr != nil {
				return efErr
			}
			return printEnsureFreshResult(res)
		}
		res, err := cerbapi.EnsureFresh(inProcessContext(cmd.Context()), newResourceRuntimeService(), id, resourceEnsureFreshForce, deployOpts...)
		if err != nil {
			return err
		}
		return printEnsureFreshResult(res)
	},
}

func printEnsureFreshResult(res *cerbapi.EnsureFreshResult) error {
	if res == nil {
		return errors.New("ensure-fresh returned no result")
	}
	if !res.Success {
		if res.Message != "" {
			return errors.New(res.Message)
		}
		return fmt.Errorf("ensure-fresh %s: %s failed", res.ServiceID, res.Action)
	}
	switch res.Action {
	case "noop":
		fmt.Printf("%s: %s\n", res.ServiceID, res.Message)
	default:
		reason := res.Reason
		if reason != "" {
			reason = " (" + reason + ")"
		}
		fmt.Printf("%s: %s%s\n", res.ServiceID, res.Action, reason)
		if res.Message != "" {
			fmt.Println(res.Message)
		}
	}
	return nil
}

func resolveDeployFlags(cmd *cobra.Command) ([]cerbapi.MutationOption, error) {
	yesSet := cmd.Flags().Changed("install-after-build")
	noSet := cmd.Flags().Changed("no-install-after-build")
	if yesSet && noSet {
		return nil, errors.New("--install-after-build and --no-install-after-build are mutually exclusive")
	}
	switch {
	case yesSet:
		val, err := cmd.Flags().GetBool("install-after-build")
		if err != nil {
			return nil, fmt.Errorf("read --install-after-build: %w", err)
		}
		return []cerbapi.MutationOption{cerbapi.WithInstallAfterBuildOverride(val)}, nil
	case noSet:
		return []cerbapi.MutationOption{cerbapi.WithInstallAfterBuildOverride(false)}, nil
	default:
		return nil, nil
	}
}

var resourceStatusCmd = &cobra.Command{
	Use:   "status <resource-id>",
	Short: "Show the runtime status of a local process resource",
	Long:  "Resolves runtime status for a local process resource through its configured backend. Status includes operator guidance such as artifact drift, compact action codes, and a prose next step.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		// A server or container resource is a named handle for connector
		// operations, not a broken workload. Answer the question.
		if !cerbapi.SupervisedLocally(res.Type, res.Connector) {
			return renderResourceRuntimeStatus(cerbapi.NewUnsupervisedRuntimeStatus(res), resourceStatusOutput)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			st, statusErr := client.GetResourceRuntime(cmd.Context(), res.ID)
			if statusErr == nil {
				return renderResourceRuntimeStatus(st, resourceStatusOutput)
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(statusErr, &dErr) {
				return statusErr
			}
		}

		st, err := newResourceRuntimeService().GetResourceRuntime(context.Background(), res.ID)
		if err != nil {
			return err
		}
		return renderResourceRuntimeStatus(st, resourceStatusOutput)
	},
}

var resourceLogsCmd = &cobra.Command{
	Use:   "logs <resource-id>",
	Short: "Show local process resource logs",
	Long:  "Shows recent logs for a local process resource. For os_service resources on macOS, use --stream stdout or --stream stderr to select the launchd log stream.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if !cerbapi.SupervisedLocally(res.Type, res.Connector) {
			return cerbapi.UnsupervisedOperationError("logs", res)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			out, logErr := client.ResourceLogs(cmd.Context(), res.ID, resourceLogsLines, resourceLogsStream)
			if logErr == nil {
				fmt.Print(out.Content)
				if !strings.HasSuffix(out.Content, "\n") {
					fmt.Println()
				}
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(logErr, &dErr) {
				return logErr
			}
		}

		out, err := newResourceRuntimeService().ResourceLogs(inProcessContext(cmd.Context()), res.ID, resourceLogsLines, resourceLogsStream)
		if err != nil {
			return err
		}
		fmt.Print(out.Content)
		if !strings.HasSuffix(out.Content, "\n") {
			fmt.Println()
		}
		return nil
	},
}

var resourceSyncCmd = &cobra.Command{
	Use:   "sync <resource-id>",
	Short: "Sync installed runtime artifacts for a local process resource",
	Long:  "Syncs installed runtime artifacts without applying the runtime backend. This is primarily useful for os_service resources using run_from=artifact when you want to copy the artifact now and activate it later.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The runtime refuses an unknown or unsupervised id, so the attempt is
		// recorded; the local lookup only adds a hint to that refusal.
		id, hint := args[0], mutationHint("sync", args[0])

		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if client != nil {
			out, syncErr := client.SyncResource(cmd.Context(), id, ackOption(cmd))
			if syncErr != nil {
				return withHint(syncErr, hint)
			}
			return withHint(printResourceOpResult(out, fmt.Sprintf("Synced resource %s", id)), hint)
		}

		out, err := newResourceRuntimeService().SyncResource(inProcessContext(cmd.Context()), id, ackOption(cmd))
		if err != nil {
			return withHint(err, hint)
		}
		return withHint(printResourceOpResult(out, fmt.Sprintf("Synced resource %s", id)), hint)
	},
}

var resourceRemoveCmd = &cobra.Command{
	Use:   "remove <resource-id>",
	Short: "Uninstall a local process resource from its runtime backend",
	Long:  "Removes a local process resource from its configured runtime backend. For os_service resources on macOS, this unloads the launch agent and removes the installed artifact tree. Use stop, not remove, for non-destructive stop/pause intent.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The runtime refuses an unknown or unsupervised id, so the attempt is
		// recorded; the local lookup only adds a hint to that refusal.
		id, hint := args[0], mutationHint("remove", args[0])

		client, err := resourceMutationSocket(cmd.Context())
		if err != nil {
			return err
		}
		if client != nil {
			out, removeErr := client.RemoveResource(cmd.Context(), id, ackOption(cmd))
			if removeErr != nil {
				return withHint(removeErr, hint)
			}
			return withHint(printResourceOpResult(out, fmt.Sprintf("Removed resource %s", id)), hint)
		}

		out, err := newResourceRuntimeService().RemoveResource(inProcessContext(cmd.Context()), id, ackOption(cmd))
		if err != nil {
			return withHint(err, hint)
		}
		return withHint(printResourceOpResult(out, fmt.Sprintf("Removed resource %s", id)), hint)
	},
}

// mutationHint is what the CLI can say about a mutation's target from its
// own view of the config: that the id is unknown, or that the resource is not
// one the supervision lane operates. It is only a hint. The mutation is sent
// to the runtime either way, which refuses it and records the attempt:
// every mutation attempt is recorded, whichever surface it came from.
func mutationHint(verb, id string) error {
	res, err := loadResource(id)
	switch {
	case err != nil:
		return err
	case !cerbapi.SupervisedLocally(res.Type, res.Connector):
		return cerbapi.UnsupervisedOperationError(verb, res)
	}
	return nil
}

// withHint adds the local hint to a refused mutation.
func withHint(err, hint error) error {
	if err == nil || hint == nil {
		return err
	}
	return fmt.Errorf("%w\nhint: %s", err, hint.Error())
}

func loadResource(id string) (*config.ResourceDef, error) {
	v2, err := registry.ResolveConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	for i := range v2.Resources {
		if v2.Resources[i].ID == id {
			return &v2.Resources[i], nil
		}
	}
	return nil, fmt.Errorf("resource %q not found in config; run `cerberus resource list` to see available resources", id)
}

// resourceMutationSocket chooses the transport for a mutating resource
// command before the operation is sent.
//
// Reads may attempt the daemon and fall back in-process when the call
// fails. A mutation may not: once the request is on the wire an error
// does not tell us whether the daemon executed it, and re-running it here
// would bypass the serving runtime's guards — including the one that
// refuses a resource mutation targeting the daemon itself. So the choice
// is made here, while nothing is yet at stake, exactly as commandSocket
// does for the connector lane (see cmd_transport.go).
//
// A nil client with a nil error means the daemon is not running and the
// caller should execute in-process.
func resourceMutationSocket(ctx context.Context) (*cerbapi.SocketClient, error) {
	return commandSocket(ctx)
}

func newResourceSocketClient(opts ...cerbapi.SocketClientOption) (*cerbapi.SocketClient, error) {
	path, err := cerbapi.SocketPath()
	if err != nil {
		return nil, err
	}
	return cerbapi.NewSocketClient(path, opts...), nil
}

func newResourceRuntimeService() *cerbapi.ResourceRuntimeService {
	return app.NewResourceRuntimeService(cfgPath, nil)
}

func printResourceOpResult(out *cerbapi.OpResult, fallback string) error {
	if out == nil {
		fmt.Println(fallback)
		return nil
	}
	for _, warning := range out.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
	if !out.Success {
		// Surface captured build/install output and the log path so a failed
		// deploy is diagnosable inline instead of a bare "exit status 2".
		if out.BuildOutput != "" {
			fmt.Fprintf(os.Stderr, "--- build output ---\n%s\n--------------------\n", out.BuildOutput)
		}
		if out.InstallOutput != "" {
			fmt.Fprintf(os.Stderr, "--- install output ---\n%s\n----------------------\n", out.InstallOutput)
		}
		if out.BuildLogPath != "" {
			fmt.Fprintf(os.Stderr, "build log: %s\n", out.BuildLogPath)
		}
		if out.Error != "" {
			return errors.New(out.Error)
		}
		return fmt.Errorf("%s failed", fallback)
	}
	if out.Message != "" {
		fmt.Println(out.Message)
	} else {
		fmt.Println(fallback)
	}
	return nil
}

func printResourceRuntimeStatus(st *cerbapi.ResourceRuntimeStatus) {
	for _, warning := range st.DependencyWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	for _, warning := range st.ConfigWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	fmt.Printf("Resource:    %s\n", st.ID)
	fmt.Printf("Name:        %s\n", st.Name)
	fmt.Printf("Status:      %s\n", st.Status)
	if st.Mode != "" {
		fmt.Printf("Mode:        %s\n", st.Mode)
	}
	if st.Supervisor != "" {
		fmt.Printf("Supervisor:  %s\n", st.Supervisor)
	}
	if st.RunFrom != "" {
		fmt.Printf("Run From:    %s\n", st.RunFrom)
	}
	if st.ServiceName != "" {
		fmt.Printf("Service:     %s\n", st.ServiceName)
	}
	if st.InstallRoot != "" {
		fmt.Printf("Install:     %s\n", st.InstallRoot)
	}
	if st.ArtifactPath != "" {
		fmt.Printf("Artifact:    %s\n", st.ArtifactPath)
	}
	if st.ArtifactInstalled {
		fmt.Printf("Installed:   true\n")
	}
	if st.ArtifactStale {
		fmt.Printf("Stale:       true\n")
	}
	if st.ArtifactStaleReason != "" {
		fmt.Printf("Drift:       %s\n", st.ArtifactStaleReason)
	}
	if st.ArtifactSource != "" {
		fmt.Printf("Source:      %s\n", st.ArtifactSource)
	}
	if st.ArtifactSyncedAt != "" {
		fmt.Printf("Synced At:   %s\n", st.ArtifactSyncedAt)
	}
	if st.RecommendedAction != "" {
		fmt.Printf("Recommend:   %s\n", st.RecommendedAction)
	}
	if st.RecommendedReason != "" {
		fmt.Printf("Context:     %s\n", st.RecommendedReason)
	}
	if st.RecommendedNextStep != "" {
		fmt.Printf("Next Step:   %s\n", st.RecommendedNextStep)
	}
	if st.OperatorStopped {
		fmt.Printf("Stopped By:  operator stop (apply, deploy, or reload resumes dev_session auto-restart)\n")
	}
	if st.LaunchdLoaded {
		fmt.Printf("Loaded:      true\n")
	}
	if st.LaunchdState != "" {
		fmt.Printf("Launchd:     %s\n", st.LaunchdState)
	}
	if st.LaunchdPID > 0 {
		fmt.Printf("Launchd PID: %d\n", st.LaunchdPID)
	}
	if st.LaunchdLastExitCode != nil {
		fmt.Printf("Last Exit:   %d\n", *st.LaunchdLastExitCode)
	}
	if st.LaunchdThrottled {
		fmt.Printf("Throttled:   true\n")
	}
	if st.LaunchdReason != "" {
		fmt.Printf("Reason:      %s\n", st.LaunchdReason)
	}
	if st.LaunchdDiagnosis != "" {
		fmt.Printf("Diagnosis:   %s\n", st.LaunchdDiagnosis)
	}
	if len(st.LaunchdHighlights) > 0 {
		fmt.Printf("Highlights:  %s\n", strings.Join(st.LaunchdHighlights, " | "))
	}
}

func printResourceInspect(st *cerbapi.ResourceInspect) {
	for _, warning := range st.DependencyWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	for _, warning := range st.ConfigWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	fmt.Printf("Resource:    %s\n", st.ID)
	fmt.Printf("Name:        %s\n", st.Name)
	fmt.Printf("Status:      %s\n", st.Status)
	fmt.Printf("Type:        %s\n", st.Type)
	fmt.Printf("Project:     %s\n", st.Project)
	fmt.Printf("Connector:   %s\n", st.Connector)
	if st.Mode != "" {
		fmt.Printf("Mode:        %s\n", st.Mode)
	}
	if st.Supervisor != "" {
		fmt.Printf("Supervisor:  %s\n", st.Supervisor)
	}
	if st.RunFrom != "" {
		fmt.Printf("Run From:    %s\n", st.RunFrom)
	}
	if st.WorkspaceDir != "" {
		fmt.Printf("Workspace:   %s\n", st.WorkspaceDir)
	}
	if st.WorkingDir != "" {
		fmt.Printf("Working Dir: %s\n", st.WorkingDir)
	}
	if len(st.Command) > 0 {
		fmt.Printf("Command:     %s\n", strings.Join(st.Command, " "))
	}
	if st.BuildStrategy != "" {
		fmt.Printf("Build:       %s\n", st.BuildStrategy)
	}
	if st.ServiceName != "" {
		fmt.Printf("Service:     %s\n", st.ServiceName)
	}
	if st.PlistPath != "" {
		fmt.Printf("Plist:       %s\n", st.PlistPath)
	}
	if st.InstallRoot != "" {
		fmt.Printf("Install:     %s\n", st.InstallRoot)
	}
	if st.InstallWorkDir != "" {
		fmt.Printf("Current:     %s\n", st.InstallWorkDir)
	}
	if st.BinDir != "" {
		fmt.Printf("Bin Dir:     %s\n", st.BinDir)
	}
	if st.ArtifactPath != "" {
		fmt.Printf("Artifact:    %s\n", st.ArtifactPath)
	}
	if st.ArtifactInstalled {
		fmt.Printf("Installed:   true\n")
	}
	if st.ArtifactStale {
		fmt.Printf("Stale:       true\n")
	}
	if st.ArtifactStaleReason != "" {
		fmt.Printf("Drift:       %s\n", st.ArtifactStaleReason)
	}
	if st.ArtifactSource != "" {
		fmt.Printf("Source:      %s\n", st.ArtifactSource)
	}
	if st.ArtifactSyncedAt != "" {
		fmt.Printf("Synced At:   %s\n", st.ArtifactSyncedAt)
	}
	if st.StdoutLogPath != "" {
		fmt.Printf("Stdout Log:  %s\n", st.StdoutLogPath)
	}
	if st.StderrLogPath != "" {
		fmt.Printf("Stderr Log:  %s\n", st.StderrLogPath)
	}
	if st.RecommendedAction != "" {
		fmt.Printf("Recommend:   %s\n", st.RecommendedAction)
	}
	if st.RecommendedReason != "" {
		fmt.Printf("Context:     %s\n", st.RecommendedReason)
	}
	if st.RecommendedNextStep != "" {
		fmt.Printf("Next Step:   %s\n", st.RecommendedNextStep)
	}
	if st.OperatorStopped {
		fmt.Printf("Stopped By:  operator stop (apply, deploy, or reload resumes dev_session auto-restart)\n")
	}
	if st.LaunchdLoaded {
		fmt.Printf("Loaded:      true\n")
	}
	if st.LaunchdState != "" {
		fmt.Printf("Launchd:     %s\n", st.LaunchdState)
	}
	if st.LaunchdPID > 0 {
		fmt.Printf("Launchd PID: %d\n", st.LaunchdPID)
	}
	if st.LaunchdLastExitCode != nil {
		fmt.Printf("Last Exit:   %d\n", *st.LaunchdLastExitCode)
	}
	if st.LaunchdThrottled {
		fmt.Printf("Throttled:   true\n")
	}
	if st.LaunchdReason != "" {
		fmt.Printf("Reason:      %s\n", st.LaunchdReason)
	}
	if st.LaunchdDiagnosis != "" {
		fmt.Printf("Diagnosis:   %s\n", st.LaunchdDiagnosis)
	}
	if len(st.LaunchdHighlights) > 0 {
		fmt.Printf("Highlights:  %s\n", strings.Join(st.LaunchdHighlights, " | "))
	}
	if st.LaunchdRaw != "" {
		fmt.Println("Launchctl:")
		fmt.Print(st.LaunchdRaw)
		if !strings.HasSuffix(st.LaunchdRaw, "\n") {
			fmt.Println()
		}
	}
}

func printResourceDoctor(out *cerbapi.ResourceDoctor) {
	fmt.Printf("Resource:    %s\n", out.ResourceID)
	if out.Status != "" {
		fmt.Printf("Status:      %s\n", out.Status)
	}
	fmt.Printf("Summary:     %s\n", out.Summary)
	if out.RecommendedAction != "" {
		fmt.Printf("Recommend:   %s\n", out.RecommendedAction)
	}
	if out.RecommendedReason != "" {
		fmt.Printf("Context:     %s\n", out.RecommendedReason)
	}
	if out.RecommendedNextStep != "" {
		fmt.Printf("Next Step:   %s\n", out.RecommendedNextStep)
	}
	if out.OperatorStopped {
		fmt.Printf("Stopped By:  operator stop\n")
	}
	fmt.Println("Checks:")
	for _, c := range out.Checks {
		fmt.Printf("  [%s] %s: %s\n", strings.ToUpper(c.Status), c.Name, c.Message)
	}
}

func renderResourceList(ctx context.Context, d resolveDiagnoser, list []cerbapi.ResourceInfo, format string) error {
	if format == outputFormatJSON {
		if list == nil {
			list = []cerbapi.ResourceInfo{}
		}
		// No notice on the JSON path: the contract is a bare array and
		// a machine reader that wants skips has /registry/diagnostics.
		return printJSON(list)
	}
	printResourceList(list)
	reportResolveNotice(ctx, d)
	return nil
}

func renderResourceRuntimeStatus(st *cerbapi.ResourceRuntimeStatus, format string) error {
	if format == outputFormatJSON {
		return printJSON(st)
	}
	printResourceRuntimeStatus(st)
	return nil
}

func printResourceList(list []cerbapi.ResourceInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tPROJECT\tCONNECTOR\tMODE\tSUPERVISOR\tRUN FROM\tSTATUS\tARTIFACT\tNEXT\tENV\tOWNER\tADMIN\tTAGS")
	fmt.Fprintln(w, "--\t----\t----\t-------\t---------\t----\t----------\t--------\t------\t--------\t----\t---\t-----\t-----\t----")
	for _, r := range list {
		tags := "-"
		if len(r.Tags) > 0 {
			tags = strings.Join(r.Tags, ", ")
		}
		status := valueOrDash(r.Status)
		artifact := "-"
		if r.ArtifactInstalled {
			artifact = "installed"
		}
		if r.ArtifactStale {
			artifact = "stale"
		}
		next := valueOrDash(r.RecommendedAction)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Name, r.Type, r.Project, r.Connector,
			valueOrDash(r.Mode), valueOrDash(r.Supervisor), valueOrDash(r.RunFrom),
			status, artifact, next, valueOrDash(r.Env), valueOrDash(r.Owner), valueOrDash(r.Admin), tags)
	}
	w.Flush() //nolint:errcheck
	if len(list) == 0 {
		fmt.Println("No resources found.")
	}
}

func valueOrDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func init() {
	resourceListCmd.Flags().StringVar(&resourceListProject, "project", "", "filter by project ID")
	addOutputFlag(resourceListCmd, &resourceListOutput)
	addOutputFlag(resourceStatusCmd, &resourceStatusOutput)
	resourceCmd.AddCommand(resourceListCmd)
	resourceCmd.AddCommand(resourceShowCmd)
	resourceCmd.AddCommand(resourceInspectCmd)
	resourceCmd.AddCommand(resourceDoctorCmd)
	resourceDeployCmd.Flags().BoolVar(&resourceDeployInstallAfterBuild, "install-after-build", true, "force-run `make install` after `make build` (overrides resource + global config)")
	resourceDeployCmd.Flags().BoolVar(&resourceDeployNoInstallAfterBuild, "no-install-after-build", false, "skip `make install` after `make build` for this invocation only")
	resourceCmd.AddCommand(resourceDeployCmd)
	resourceEnsureFreshCmd.Flags().BoolVar(&resourceEnsureFreshForce, "force", false, "always deploy (rebuild); use for dev_session resources, which have no staleness detection")
	resourceEnsureFreshCmd.Flags().Bool("install-after-build", true, "force-run `make install` after `make build` (overrides resource + global config)")
	resourceEnsureFreshCmd.Flags().Bool("no-install-after-build", false, "skip `make install` after `make build` for this invocation only")
	resourceCmd.AddCommand(resourceEnsureFreshCmd)
	resourceCmd.AddCommand(resourceApplyCmd)
	resourceCmd.AddCommand(resourceReloadCmd)
	resourceCmd.AddCommand(resourceStopCmd)
	resourceCmd.AddCommand(resourceStatusCmd)
	resourceLogsCmd.Flags().IntVarP(&resourceLogsLines, "lines", "n", 50, "number of log lines to return")
	resourceLogsCmd.Flags().StringVar(&resourceLogsStream, "stream", "stdout", "log stream to read: stdout or stderr")
	resourceCmd.AddCommand(resourceLogsCmd)
	resourceCmd.AddCommand(resourceSyncCmd)
	// Every resource mutation needs acknowledgment (Decisions 11 and 14):
	// deploy, apply, reload and stop are lifecycle, sync is a write and
	// remove is destructive. ensure-fresh passes it to whichever it runs.
	for _, c := range []*cobra.Command{resourceDeployCmd, resourceEnsureFreshCmd, resourceApplyCmd, resourceReloadCmd, resourceStopCmd, resourceSyncCmd, resourceRemoveCmd} {
		c.Flags().Bool("ack", false, "acknowledge the operation; every resource mutation requires it")
		c.Flags().String("approval", "", "run under this approved approval id")
	}
	resourceCmd.AddCommand(resourceRemoveCmd)
	resourcePlanCmd.Flags().Bool("ack", false, "plan the verb as it would be sent with --ack")
	resourceCmd.AddCommand(resourcePlanCmd)

}
