package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/config"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// The docker and ssh connectors' own verbs: the old `docker ps|logs|up|down|destroy` and
// `ssh exec|status|stop|put|get|put-dir|get-dir` (cmd/cerberus cmd_docker.go and cmd_ssh.go on
// main), with the old flags and output, over the admin lane's
// POST /api/connectors/{docker,ssh}/operations/{op} with the CLI's key, the route
// `connectors exec docker|ssh <op>` uses. The gate decides them there, as old's daemon did.
//
// Old ran docker's ad-hoc targets (--host, --context, -f) in its own process, because the
// socket refused them. This build has no in-process connector path: they are sent, and the admin
// lane refuses them, recorded (a compose file is code execution on whichever daemon runs it).
// Where policy asks for an approval, the owner approves it on the console, and the same command
// with --approval <id> runs it.

// connectorOp runs one operation over the admin lane and answers its data.
func connectorOp(cmd *cobra.Command, client func() (*apiClient, error), args admin.OperationArgs) (json.RawMessage, error) {
	cl, err := client()
	if err != nil {
		return nil, err
	}
	var res struct {
		Data json.RawMessage `json:"data"`
	}
	path := "/connectors/" + url.PathEscape(args.Connector) + "/operations/" + url.PathEscape(args.Operation)
	if err = cl.post(cmd.Context(), path, args, &res); err != nil {
		return nil, pendingHint(err, args.ApprovalID)
	}
	return res.Data, nil
}

// decodeData decodes an operation's data into out, naming the operation when it is not the
// shape old printed.
func decodeData(data json.RawMessage, out any, what string) error {
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: unexpected result: %w", what, err)
	}
	return nil
}

// printData is old's writeJSON of an operation's data (a dry run's preview).
func printData(w io.Writer, data json.RawMessage) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return printJSON(w, v)
}

// dockerCommand is `docker` against the server client returns, resolving resource ids in the
// registry reg returns.
func dockerCommand(client func() (*apiClient, error), reg func() (*registry.Registry, error)) *cobra.Command {
	group := &cobra.Command{Use: "docker", Short: "Docker operations"}
	var ack bool
	var approvalID string

	ps := &cobra.Command{
		Use:   "ps",
		Short: "List running containers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := connectorOp(cmd, client, admin.OperationArgs{Connector: "docker", Operation: "list_containers", Config: dockerTargetConfig(cmd, nil)})
			if err != nil {
				return err
			}
			var containers []dockerconn.Container
			if err = decodeData(data, &containers, "docker ps"); err != nil {
				return err
			}
			return printContainers(cmd.OutOrStdout(), containers)
		},
	}

	var lines int
	logs := &cobra.Command{
		Use:   "logs <container>",
		Short: "Show container logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, res, err := dockerOperationConfig(cmd, reg, args[0])
			if err != nil {
				return err
			}
			if res != nil && res.Config["container"] == nil && res.Config["compose_file"] != nil {
				// A resource that only names a compose file has no single log stream.
				return fmt.Errorf("resource %q declares a compose stack and no single container; "+
					"run `cerberus docker ps` and pass a container name from the stack", args[0])
			}
			cfg["lines"] = lines
			data, err := connectorOp(cmd, client, admin.OperationArgs{Connector: "docker", Operation: "logs", Config: cfg})
			if err != nil {
				return err
			}
			var text string
			if err = decodeData(data, &text, "docker logs"); err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), text)
			return err
		},
	}
	logs.Flags().IntVar(&lines, "lines", 50, "number of log lines to show")

	lifecycle := func(use, short, long, operation, composeDone, containerDone string) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Long:  long,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id := args[0]
				cfg, res, err := dockerOperationConfig(cmd, reg, id)
				if err != nil {
					return err
				}
				if _, err = connectorOp(cmd, client, admin.OperationArgs{Connector: "docker", Operation: operation, Config: cfg,
					Acknowledged: ack, ApprovalID: approvalID}); err != nil {
					return err
				}
				if composeFile := dockerComposeFile(cfg, res); composeFile != "" {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", composeDone, composeFile)
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", containerDone, id)
				return err
			},
		}
	}
	up := lifecycle("up <resource-id>", "Start container or compose stack", "", "start", "Compose stack started", "Container started")
	down := lifecycle("down <resource-id>", "Stop container or compose stack (does not remove it)",
		`Stops a container (docker stop) or a compose stack (docker compose stop).
Nothing is removed: containers, networks and volumes are kept, and
'cerberus docker up' starts the same stack again.

Removal (docker rm, docker compose down) is 'cerberus docker destroy'.`, "stop", "Compose stack stopped", "Container stopped")
	destroy := lifecycle("destroy <resource-id>", "Remove a container or tear down a compose stack (docker rm / docker compose down)",
		`Removes a container (docker rm) or tears down a compose stack (docker compose
down, which removes its containers and networks). This is the connector's
destroy operation: destructive, and not undone by 'cerberus docker up'.
It needs --ack.

'cerberus docker down' stops without removing anything.`, "destroy", "Compose stack torn down", "Container removed")
	const adHoc = "; old ran it in its own shell, and the serving Cerberus refuses an ad-hoc target"
	for _, c := range []*cobra.Command{up, down, destroy} {
		c.Flags().StringP("file", "f", "", "compose file path (for compose mode)"+adHoc)
		c.Flags().StringVar(&approvalID, "approval", "", "run under this approval, once the owner has approved it on the console")
	}
	up.Flags().BoolVar(&ack, "ack", false, "acknowledge the start operation (lifecycle)")
	down.Flags().BoolVar(&ack, "ack", false, "acknowledge the stop operation (lifecycle)")
	destroy.Flags().BoolVar(&ack, "ack", false, "acknowledge the destroy operation (destructive)")
	for _, c := range []*cobra.Command{ps, logs, up, down, destroy} {
		c.Flags().StringP("host", "H", "", "Docker daemon to target as a DOCKER_HOST value (ssh://user@host, tcp://host:2376)"+adHoc)
		c.Flags().String("context", "", "Docker context name to target (mutually exclusive with --host)"+adHoc)
		group.AddCommand(c)
	}
	return group
}

func printContainers(w io.Writer, containers []dockerconn.Container) error {
	if len(containers) == 0 {
		_, err := fmt.Fprintln(w, "No running containers.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tIMAGE\tSTATUS\tPORTS")
	_, _ = fmt.Fprintln(tw, "--\t----\t-----\t------\t-----")
	for _, c := range containers {
		ports := ""
		if len(c.Ports) > 0 {
			ports = c.Ports[0]
			if len(c.Ports) > 1 {
				ports += fmt.Sprintf(" (+%d)", len(c.Ports)-1)
			}
		}
		_, _ = fmt.Fprintf(tw, "%.12s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Image, c.Status, ports)
	}
	return tw.Flush()
}

// dockerTargetConfig folds --host and --context into an operation's config (old's).
func dockerTargetConfig(cmd *cobra.Command, cfg map[string]any) map[string]any {
	host, _ := cmd.Flags().GetString("host")
	dockerContext, _ := cmd.Flags().GetString("context")
	if host == "" && dockerContext == "" {
		return cfg
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if host != "" {
		cfg["host"] = host
	}
	if dockerContext != "" {
		cfg["context"] = dockerContext
	}
	return cfg
}

// dockerOperationConfig is old's: a declared docker resource is sent by id, and the server
// resolves it; anything else is a literal container name.
func dockerOperationConfig(cmd *cobra.Command, reg func() (*registry.Registry, error), id string) (map[string]any, *config.ResourceDef, error) {
	res, err := lookupDockerResource(cmd, reg, id)
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{"id": id, "name": id, "container": id}
	if res != nil {
		cfg = map[string]any{"resource": res.ID}
	}
	if composeFile, flagErr := cmd.Flags().GetString("file"); flagErr == nil && composeFile != "" {
		cfg["compose_file"] = composeFile
	}
	return dockerTargetConfig(cmd, cfg), res, nil
}

// lookupDockerResource finds a declared docker resource by id in the registry. A miss is the
// literal-container path; a resource of another connector is old's error.
func lookupDockerResource(cmd *cobra.Command, reg func() (*registry.Registry, error), id string) (*config.ResourceDef, error) {
	r, err := reg()
	var resolved *registry.ResolvedConfig
	if err == nil {
		resolved, err = r.Resolve()
	}
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not read config (%v); treating %q as a container name\n", err, id)
		return nil, nil
	}
	for i := range resolved.Config.Resources {
		res := &resolved.Config.Resources[i]
		if res.ID != id {
			continue
		}
		if res.Connector != "docker" {
			return nil, fmt.Errorf("resource %q is %s/%s, not a docker resource; try: %s",
				res.ID, res.Type, res.Connector, unsupervisedNextStep(res.ID, res.Connector))
		}
		return res, nil
	}
	return nil, nil
}

// unsupervisedNextStep is old's cerbapi.UnsupervisedNextStep: the commands that do administer
// the resource.
func unsupervisedNextStep(id, connector string) string {
	switch connector {
	case "docker":
		return fmt.Sprintf("cerberus docker up %s | cerberus docker down %s | cerberus docker logs %s", id, id, id)
	case "ssh":
		return fmt.Sprintf("cerberus ssh status %s | cerberus ssh exec %s -- <command>", id, id)
	default:
		return fmt.Sprintf("cerberus connectors describe %s", connector)
	}
}

// dockerComposeFile is the compose file an operation ran against: -f, or the resource's.
func dockerComposeFile(cfg map[string]any, res *config.ResourceDef) string {
	if value, _ := cfg["compose_file"].(string); value != "" {
		return value
	}
	if res != nil {
		value, _ := res.Config["compose_file"].(string)
		return value
	}
	return ""
}

// sshCommand is `ssh` against the server client returns.
func sshCommand(client func() (*apiClient, error)) *cobra.Command {
	group := &cobra.Command{Use: "ssh", Short: "SSH operations on remote hosts"}
	var ack, dryRun bool
	var approvalID string
	run := func(cmd *cobra.Command, operation string, cfg map[string]any, dry bool) (json.RawMessage, error) {
		return connectorOp(cmd, client, admin.OperationArgs{Connector: "ssh", Operation: operation, Config: cfg, DryRun: dry,
			Acknowledged: ack, ApprovalID: approvalID})
	}

	exec := &cobra.Command{
		Use:   "exec <resource-id> -- <command...>",
		Short: "Execute a command on a remote host",
		Long:  "Runs a command on the remote host defined by the given resource ID.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := ""
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				command = strings.Join(args[dash:], " ")
			} else if len(args) > 1 {
				command = strings.Join(args[1:], " ")
			}
			if command == "" {
				return fmt.Errorf("no command specified — use: cerberus ssh exec <resource-id> -- <command>")
			}
			data, err := run(cmd, "exec", sshConfig(args[0], command), dryRun)
			if err != nil {
				return err
			}
			if dryRun {
				return printData(cmd.OutOrStdout(), data)
			}
			var res sshconn.ExecResult
			if err = decodeData(data, &res, "ssh exec"); err != nil {
				return err
			}
			if res.Stdout != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), res.Stdout)
			}
			if res.Stderr != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", res.Stderr)
			}
			if res.ExitCode != 0 {
				return fmt.Errorf("remote command exited with code %d", res.ExitCode)
			}
			return nil
		},
	}

	status := &cobra.Command{
		Use:   "status <resource-id>",
		Short: "Check SSH connectivity to a remote host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := connectorOp(cmd, client, admin.OperationArgs{Connector: "ssh", Operation: "status", Config: sshConfig(args[0], "")})
			if err != nil {
				return err
			}
			var text string
			if err = decodeData(data, &text, "ssh status"); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), text)
			return err
		},
	}

	stop := &cobra.Command{
		Use:   "stop <resource-id>",
		Short: "Shut down a remote host over SSH",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := run(cmd, "stop", sshConfig(args[0], ""), dryRun)
			if err != nil {
				return err
			}
			if dryRun {
				return printData(cmd.OutOrStdout(), data)
			}
			return nil
		},
	}

	// transfer is put, get, put-dir and get-dir: put is <local> <remote>, get <remote> <local>;
	// only the uploads take --dry-run, as old's did.
	transfer := func(operation, use, short, long string, upload, dir bool) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Long:  long,
			Args:  cobra.ExactArgs(3),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg := sshConfig(args[0], "")
				if upload {
					cfg["local_path"], cfg["remote_path"] = localPath(args[1]), args[2]
				} else {
					cfg["remote_path"], cfg["local_path"] = args[1], localPath(args[2])
				}
				dry := upload && dryRun
				data, err := run(cmd, operation, cfg, dry)
				if err != nil {
					return err
				}
				if dry {
					return printData(cmd.OutOrStdout(), data)
				}
				if dir {
					var t sshconn.DirTransferResult
					if err = decodeData(data, &t, "ssh "+operation); err != nil {
						return err
					}
					writeDirTransfer(cmd.OutOrStdout(), &t)
					return nil
				}
				var t sshconn.TransferResult
				if err = decodeData(data, &t, "ssh "+operation); err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s → %s (%d bytes)\n", t.LocalPath, t.RemotePath, t.Bytes)
				return err
			},
		}
	}
	put := transfer("put", "put <resource-id> <local-path> <remote-path>", "Upload a file to a remote host",
		"Uploads a local file to the remote host over SFTP. The write lands on a\n"+
			"temporary name and is renamed into place, so an interrupted transfer\n"+
			"leaves the previous file intact rather than a truncated one.", true, false)
	get := transfer("get", "get <resource-id> <remote-path> <local-path>", "Download a file from a remote host",
		"Downloads a file from the remote host over SFTP, overwriting the local path\n"+
			"if it exists. Writing to the local filesystem needs --ack.", false, false)
	putDir := transfer("put_dir", "put-dir <resource-id> <local-dir> <remote-dir>", "Upload a directory tree to a remote host",
		"Recursively uploads a local directory to the remote host over SFTP.\n\n"+
			"Permission bits are carried, so an uploaded script stays executable.\n"+
			"Each file lands on a temporary name and is renamed into place, so an\n"+
			"interrupted sync leaves the previous file intact. A symlink pointing\n"+
			"outside the tree is refused rather than followed.\n\n"+
			"Every byte is copied every time — there is no delta transfer. That fits\n"+
			"compose files, env files and config directories, not a large build tree.", true, true)
	getDir := transfer("get_dir", "get-dir <resource-id> <remote-dir> <local-dir>", "Download a directory tree from a remote host",
		"Recursively downloads a remote directory over SFTP. It is the same walk as\n"+
			"put-dir with the ends exchanged, including the refusal to follow a\n"+
			"symlink out of the tree.\n\n"+
			"Like get, it overwrites local files under the path you named, so it needs\n"+
			"--ack.", false, true)

	for _, c := range []struct {
		cmd      *cobra.Command
		dry, ack string
	}{
		{exec, "preview the remote command without executing it", "acknowledge destructive remote execution"},
		{stop, "preview the remote shutdown without executing it", "acknowledge destructive remote execution"},
		{put, "preview the upload without transferring", "acknowledge overwriting the remote file"},
		{putDir, "preview the upload without transferring", "acknowledge overwriting remote files"},
		{get, "", "acknowledge overwriting the local file"},
		{getDir, "", "acknowledge overwriting local files"},
	} {
		if c.dry != "" {
			c.cmd.Flags().BoolVar(&dryRun, "dry-run", false, c.dry)
		}
		c.cmd.Flags().BoolVar(&ack, "ack", false, c.ack)
		c.cmd.Flags().StringVar(&approvalID, "approval", "", "run under this approval, once the owner has approved it on the console")
	}
	group.AddCommand(exec, status, stop, put, get, putDir, getDir)
	return group
}

// writeDirTransfer is old's: the summary first, the per-path detail after.
func writeDirTransfer(w io.Writer, t *sshconn.DirTransferResult) {
	src, dst := t.LocalPath, t.RemotePath
	if t.Direction == "download" {
		src, dst = t.RemotePath, t.LocalPath
	}
	_, _ = fmt.Fprintf(w, "%s → %s: %s, %s", src, dst, countOf(t.Files, "file"), countOf(t.Dirs, "dir"))
	if t.Symlinks > 0 {
		_, _ = fmt.Fprintf(w, ", %s", countOf(t.Symlinks, "symlink"))
	}
	if t.Skipped > 0 {
		_, _ = fmt.Fprintf(w, ", %d skipped", t.Skipped)
	}
	_, _ = fmt.Fprintf(w, ", %d bytes\n", t.Bytes)
	for _, e := range t.Entries {
		if e.Action == "skip" {
			_, _ = fmt.Fprintf(w, "  skip %s (%s)\n", e.Path, e.Reason)
		}
	}
}

func countOf(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// localPath makes a local path absolute against this shell's working directory: the transfer
// runs in the serving Cerberus, whose working directory is not the operator's.
func localPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// sshConfig names the target by resource id and nothing else; the server resolves it.
func sshConfig(resourceID, command string) map[string]any {
	cfg := map[string]any{"id": resourceID}
	if command != "" {
		cfg["command"] = command
	}
	return cfg
}
