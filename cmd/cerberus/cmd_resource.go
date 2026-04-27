package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/spf13/cobra"
)

var resourceCmd = &cobra.Command{
	Use:   "resource",
	Short: "Resource management",
}

var resourceListProject string
var resourceLogsLines int
var resourceLogsStream string

var resourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List resources",
	Long:  "Lists all resources defined in the config. Use --project to filter by project.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if client, err := newResourceSocketClient(); err == nil {
			list, listErr := client.ListResources(cmd.Context(), cerbapi.ResourceListArgs{ProjectID: resourceListProject})
			if listErr == nil {
				printResourceList(list)
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(listErr, &dErr) {
				return listErr
			}
		}

		v2, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		var list []cerbapi.ResourceInfo
		for _, r := range v2.Resources {
			if resourceListProject != "" && r.Project != resourceListProject {
				continue
			}
			mode, supervisor, runFrom := "-", "-", "-"
			if r.Type == string(domain.ResourceProcess) && r.Connector == "local" {
				spec, err := localconn.SpecFromResourceConfig(r.Config)
				if err == nil {
					mode = string(spec.Mode)
					if mode == "" {
						mode = string(localconn.ProcessModeDevSession)
					}
					supervisor = string(spec.Supervisor)
					if supervisor == "" {
						supervisor = string(localconn.ProcessSupervisorAuto)
					}
					runFrom = string(spec.RunFrom)
					if runFrom == "" {
						runFrom = string(localconn.ProcessRunFromWorkspace)
					}
				}
			}
			list = append(list, cerbapi.ResourceInfo{
				ID:         r.ID,
				Name:       r.Name,
				Type:       r.Type,
				Project:    r.Project,
				Connector:  r.Connector,
				Mode:       mode,
				Supervisor: supervisor,
				RunFrom:    runFrom,
				Tags:       append([]string(nil), r.Tags...),
			})
		}
		printResourceList(list)
		return nil
	},
}

var resourceShowCmd = &cobra.Command{
	Use:   "show <resource-id>",
	Short: "Show resource details",
	Long:  "Shows detailed information about a specific resource.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := config.LoadUnified(cfgPath)
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
					keys := make([]string, 0, len(r.Config))
					for k := range r.Config {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						v := r.Config[k]
						fmt.Printf("  %s: %v\n", k, v)
					}
				}
				return nil
			}
		}

		return fmt.Errorf("resource %q not found", id)
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
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			return fmt.Errorf("resource %q is %s/%s; inspect currently supports local process resources only", res.ID, res.Type, res.Connector)
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

		v2, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		out, err := cerbapi.NewInProcessClient(nil, cerbapi.WithConfigV2(v2)).GetResourceInspect(cmd.Context(), res.ID)
		if err != nil {
			return err
		}
		printResourceInspect(out)
		return nil
	},
}

var resourceApplyCmd = &cobra.Command{
	Use:   "apply <resource-id>",
	Short: "Apply a local process resource",
	Long:  "Applies a local process resource using its configured runtime backend. For os_service resources on macOS, this syncs the installed artifact and updates the launch agent.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			return fmt.Errorf("resource %q is %s/%s; apply currently supports local process resources only", res.ID, res.Type, res.Connector)
		}

		if client, err := newResourceSocketClient(); err == nil {
			out, err := client.ApplyResource(cmd.Context(), res.ID)
			if err == nil {
				if out.Message != "" {
					fmt.Println(out.Message)
				} else {
					fmt.Printf("Applied resource %s\n", res.ID)
				}
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(err, &dErr) {
				return err
			}
		}

		conn := localconn.New()
		spec, err := localconn.SpecFromResourceConfig(res.Config)
		if err != nil {
			return err
		}
		applyRes, err := conn.Apply(cmd.Context(), toDomainResource(res))
		if err != nil {
			return err
		}
		fmt.Println(localconn.FormatApplyResultMessage(res.ID, spec, applyRes))
		return nil
	},
}

var resourceStatusCmd = &cobra.Command{
	Use:   "status <resource-id>",
	Short: "Show the runtime status of a local process resource",
	Long:  "Resolves runtime status for a local process resource through its configured backend.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			return fmt.Errorf("resource %q is %s/%s; status currently supports local process resources only", res.ID, res.Type, res.Connector)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			st, statusErr := client.GetResourceRuntime(cmd.Context(), res.ID)
			if statusErr == nil {
				printResourceRuntimeStatus(st)
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(statusErr, &dErr) {
				return statusErr
			}
		}

		conn := localconn.New()
		state, err := conn.Status(context.Background(), toDomainResource(res))
		if err != nil {
			return err
		}
		fmt.Printf("Resource: %s\n", res.ID)
		fmt.Printf("Status:   %s\n", state)
		return nil
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
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			return fmt.Errorf("resource %q is %s/%s; logs currently support local process resources only", res.ID, res.Type, res.Connector)
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

		v2, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		out, err := cerbapi.NewInProcessClient(nil, cerbapi.WithConfigV2(v2)).ResourceLogs(cmd.Context(), res.ID, resourceLogsLines, resourceLogsStream)
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
	Long:  "Syncs installed runtime artifacts without applying the runtime backend. This is primarily useful for os_service resources using run_from=artifact.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			return fmt.Errorf("resource %q is %s/%s; sync currently supports local process resources only", res.ID, res.Type, res.Connector)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			out, syncErr := client.SyncResource(cmd.Context(), res.ID)
			if syncErr == nil {
				if out.Message != "" {
					fmt.Println(out.Message)
				} else {
					fmt.Printf("Synced resource %s\n", res.ID)
				}
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(syncErr, &dErr) {
				return syncErr
			}
		}

		spec, err := localconn.SpecFromResourceConfig(res.Config)
		if err != nil {
			return err
		}
		if spec.RunFrom != localconn.ProcessRunFromArtifact {
			fmt.Printf("Resource %s does not use artifact mode; nothing to sync\n", res.ID)
			return nil
		}
		_, syncRes, err := localconn.SyncArtifactInstall(toDomainResource(res), spec)
		if err != nil {
			return err
		}
		if syncRes.Changed {
			fmt.Printf("Resource %s artifact synced\n", res.ID)
		} else {
			fmt.Printf("Resource %s artifact already current\n", res.ID)
		}
		return nil
	},
}

var resourceRemoveCmd = &cobra.Command{
	Use:   "remove <resource-id>",
	Short: "Remove a local process resource from its runtime backend",
	Long:  "Removes a local process resource from its configured runtime backend. For os_service resources on macOS, this unloads the launch agent and removes the installed artifact tree.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := loadResource(args[0])
		if err != nil {
			return err
		}
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			return fmt.Errorf("resource %q is %s/%s; remove currently supports local process resources only", res.ID, res.Type, res.Connector)
		}

		if client, sockErr := newResourceSocketClient(); sockErr == nil {
			out, removeErr := client.RemoveResource(cmd.Context(), res.ID)
			if removeErr == nil {
				if out.Message != "" {
					fmt.Println(out.Message)
				} else {
					fmt.Printf("Removed resource %s\n", res.ID)
				}
				return nil
			}
			var dErr *cerbapi.DaemonUnreachableError
			if !errors.As(removeErr, &dErr) {
				return removeErr
			}
		}

		conn := localconn.New()
		if err := conn.Destroy(cmd.Context(), toDomainResource(res)); err != nil {
			return err
		}
		fmt.Printf("Removed resource %s\n", res.ID)
		return nil
	},
}

func loadResource(id string) (*config.ResourceDef, error) {
	v2, err := config.LoadUnified(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	for i := range v2.Resources {
		if v2.Resources[i].ID == id {
			return &v2.Resources[i], nil
		}
	}
	return nil, fmt.Errorf("resource %q not found", id)
}

func toDomainResource(r *config.ResourceDef) *domain.Resource {
	return &domain.Resource{
		ID:        r.ID,
		Name:      r.Name,
		Type:      domain.ResourceType(r.Type),
		ProjectID: r.Project,
		Connector: r.Connector,
		Config:    r.Config,
		Tags:      append([]string(nil), r.Tags...),
		DependsOn: append([]string(nil), r.DependsOn...),
	}
}

func newResourceSocketClient() (*cerbapi.SocketClient, error) {
	path, err := cerbapi.SocketPath()
	if err != nil {
		return nil, err
	}
	return cerbapi.NewSocketClient(path), nil
}

func printResourceRuntimeStatus(st *cerbapi.ResourceRuntimeStatus) {
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
		fmt.Printf("Why:         %s\n", st.RecommendedReason)
	}
}

func printResourceInspect(st *cerbapi.ResourceInspect) {
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
	if len(st.Build) > 0 {
		fmt.Printf("Build:       %s\n", strings.Join(st.Build, " "))
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
		fmt.Printf("Why:         %s\n", st.RecommendedReason)
	}
}

func printResourceList(list []cerbapi.ResourceInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tPROJECT\tCONNECTOR\tMODE\tSUPERVISOR\tRUN FROM\tSTATUS\tARTIFACT\tNEXT\tTAGS")
	fmt.Fprintln(w, "--\t----\t----\t-------\t---------\t----\t----------\t--------\t------\t--------\t----\t----")
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Name, r.Type, r.Project, r.Connector,
			valueOrDash(r.Mode), valueOrDash(r.Supervisor), valueOrDash(r.RunFrom),
			status, artifact, next, tags)
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
	resourceCmd.AddCommand(resourceListCmd)
	resourceCmd.AddCommand(resourceShowCmd)
	resourceCmd.AddCommand(resourceInspectCmd)
	resourceCmd.AddCommand(resourceApplyCmd)
	resourceCmd.AddCommand(resourceStatusCmd)
	resourceLogsCmd.Flags().IntVarP(&resourceLogsLines, "lines", "n", 50, "number of log lines to return")
	resourceLogsCmd.Flags().StringVar(&resourceLogsStream, "stream", "stdout", "log stream to read: stdout or stderr")
	resourceCmd.AddCommand(resourceLogsCmd)
	resourceCmd.AddCommand(resourceSyncCmd)
	resourceCmd.AddCommand(resourceRemoveCmd)
}
