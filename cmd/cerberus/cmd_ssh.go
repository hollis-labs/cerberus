package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh",
	Short: "SSH operations on remote hosts",
}

var sshAcknowledge bool
var sshDryRun bool

var sshExecCmd = &cobra.Command{
	Use:   "exec <resource-id> -- <command...>",
	Short: "Execute a command on a remote host",
	Long:  "Runs a command on the remote host defined by the given resource ID.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resourceID := args[0]

		// Find the "--" separator and extract the command after it.
		command := ""
		dashIdx := cmd.ArgsLenAtDash()
		if dashIdx >= 0 {
			command = strings.Join(args[dashIdx:], " ")
		} else if len(args) > 1 {
			command = strings.Join(args[1:], " ")
		}
		if command == "" {
			return fmt.Errorf("no command specified — use: cerberus ssh exec <resource-id> -- <command>")
		}

		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "ssh",
			Operation:    "exec",
			Config:       sshConfig(resourceID, command),
			DryRun:       sshDryRun,
			Acknowledged: sshAcknowledge,
		})
		if err != nil {
			return err
		}
		if sshDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		execResult, ok := result.Data.(*sshconn.ExecResult)
		if !ok {
			return fmt.Errorf("ssh exec: unexpected result type %T", result.Data)
		}

		if execResult.Stdout != "" {
			fmt.Println(execResult.Stdout)
		}
		if execResult.Stderr != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", execResult.Stderr)
		}
		if execResult.ExitCode != 0 {
			return fmt.Errorf("remote command exited with code %d", execResult.ExitCode)
		}
		return nil
	},
}

var sshStatusCmd = &cobra.Command{
	Use:   "status <resource-id>",
	Short: "Check SSH connectivity to a remote host",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resourceID := args[0]

		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "ssh",
			Operation: "status",
			Config:    sshConfig(resourceID, ""),
		})
		if err != nil {
			return err
		}

		if statusJSON, ok := result.Data.(string); ok {
			fmt.Println(statusJSON)
			return nil
		}
		return fmt.Errorf("ssh status: unexpected result type %T", result.Data)
	},
}

var sshStopCmd = &cobra.Command{
	Use:   "stop <resource-id>",
	Short: "Shut down a remote host over SSH",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "ssh",
			Operation:    "stop",
			Config:       sshConfig(args[0], ""),
			DryRun:       sshDryRun,
			Acknowledged: sshAcknowledge,
		})
		if err != nil {
			return err
		}
		if sshDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		return nil
	},
}

// sshTransferCmd builds the put and get commands, which differ only in
// argument order and which one needs an acknowledgment.
func sshTransferCmd(operation, use, short, long string, destructive bool) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {

			cfg := sshConfig(args[0], "")
			// put is <local> <remote>; get is <remote> <local>. Each reads in
			// the direction the transfer runs, which is why the order differs.
			if operation == "put" {
				cfg["local_path"], cfg["remote_path"] = localPath(args[1]), args[2]
			} else {
				cfg["remote_path"], cfg["local_path"] = args[1], localPath(args[2])
			}

			svc, closeFn, err := newExternalConnectorService(cmd.Context())
			if err != nil {
				return err
			}
			defer closeFn()

			result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
				Connector:    "ssh",
				Operation:    operation,
				Config:       cfg,
				DryRun:       destructive && sshDryRun,
				Acknowledged: sshAcknowledge,
			})
			if err != nil {
				return err
			}
			if destructive && sshDryRun {
				return writeJSON(cmd.OutOrStdout(), result.Data)
			}
			transfer, ok := result.Data.(*sshconn.TransferResult)
			if !ok {
				return fmt.Errorf("ssh %s: unexpected result type %T", operation, result.Data)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s → %s (%d bytes)\n",
				transfer.LocalPath, transfer.RemotePath, transfer.Bytes)
			return nil
		},
	}
}

var sshPutCmd = sshTransferCmd("put",
	"put <resource-id> <local-path> <remote-path>",
	"Upload a file to a remote host",
	"Uploads a local file to the remote host over SFTP. The write lands on a\n"+
		"temporary name and is renamed into place, so an interrupted transfer\n"+
		"leaves the previous file intact rather than a truncated one.",
	true)

var sshGetCmd = sshTransferCmd("get",
	"get <resource-id> <remote-path> <local-path>",
	"Download a file from a remote host",
	"Downloads a file from the remote host over SFTP. Read-only, so it needs no\n"+
		"acknowledgment.",
	false)

// sshDirTransferCmd builds put-dir and get-dir. They mirror put and get: same
// argument order, same config keys, and the same rule about which one needs an
// acknowledgment.
func sshDirTransferCmd(operation, use, short, long string, destructive bool) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {

			cfg := sshConfig(args[0], "")
			if operation == "put_dir" {
				cfg["local_path"], cfg["remote_path"] = localPath(args[1]), args[2]
			} else {
				cfg["remote_path"], cfg["local_path"] = args[1], localPath(args[2])
			}

			svc, closeFn, err := newExternalConnectorService(cmd.Context())
			if err != nil {
				return err
			}
			defer closeFn()

			result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
				Connector:    "ssh",
				Operation:    operation,
				Config:       cfg,
				DryRun:       destructive && sshDryRun,
				Acknowledged: sshAcknowledge,
			})
			if err != nil {
				return err
			}
			if destructive && sshDryRun {
				return writeJSON(cmd.OutOrStdout(), result.Data)
			}
			transfer, ok := result.Data.(*sshconn.DirTransferResult)
			if !ok {
				return fmt.Errorf("ssh %s: unexpected result type %T", operation, result.Data)
			}
			writeDirTransfer(cmd.OutOrStdout(), transfer)
			return nil
		},
	}
}

// writeDirTransfer prints the summary first and the per-path detail after, so
// a thousand-file sync still answers "what did it do" in the first line.
func writeDirTransfer(w io.Writer, t *sshconn.DirTransferResult) {
	src, dst := t.LocalPath, t.RemotePath
	if t.Direction == "download" {
		src, dst = t.RemotePath, t.LocalPath
	}
	fmt.Fprintf(w, "%s → %s: %s, %s", src, dst, countOf(t.Files, "file"), countOf(t.Dirs, "dir"))
	if t.Symlinks > 0 {
		fmt.Fprintf(w, ", %s", countOf(t.Symlinks, "symlink"))
	}
	if t.Skipped > 0 {
		fmt.Fprintf(w, ", %d skipped", t.Skipped)
	}
	fmt.Fprintf(w, ", %d bytes\n", t.Bytes)
	for _, e := range t.Entries {
		if e.Action == "skip" {
			fmt.Fprintf(w, "  skip %s (%s)\n", e.Path, e.Reason)
		}
	}
}

func countOf(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

var sshPutDirCmd = sshDirTransferCmd("put_dir",
	"put-dir <resource-id> <local-dir> <remote-dir>",
	"Upload a directory tree to a remote host",
	"Recursively uploads a local directory to the remote host over SFTP.\n\n"+
		"Permission bits are carried, so an uploaded script stays executable.\n"+
		"Each file lands on a temporary name and is renamed into place, so an\n"+
		"interrupted sync leaves the previous file intact. A symlink pointing\n"+
		"outside the tree is refused rather than followed.\n\n"+
		"Every byte is copied every time — there is no delta transfer. That fits\n"+
		"compose files, env files and config directories, not a large build tree.",
	true)

var sshGetDirCmd = sshDirTransferCmd("get_dir",
	"get-dir <resource-id> <remote-dir> <local-dir>",
	"Download a directory tree from a remote host",
	"Recursively downloads a remote directory over SFTP. It is the same walk as\n"+
		"put-dir with the ends exchanged, including the refusal to follow a\n"+
		"symlink out of the tree.\n\n"+
		"Like get, it writes only to the local machine under a path you named, so\n"+
		"it needs no acknowledgment.",
	false)

// localPath makes a local path absolute against this shell's working
// directory. The transfer usually runs in the daemon, whose working directory
// is not the operator's, so `./app.env` would otherwise name a different file.
func localPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// sshConfig names the target by resource id and nothing else. Whoever runs
// the operation — the daemon, or this process when there is none — resolves
// the id against its config; connection settings are never sent, and the
// socket refuses them.
func sshConfig(resourceID, command string) map[string]any {
	cfg := map[string]any{"id": resourceID}
	if command != "" {
		cfg["command"] = command
	}
	return cfg
}

func init() {
	sshExecCmd.Flags().BoolVar(&sshDryRun, "dry-run", false, "preview the remote command without executing it")
	sshExecCmd.Flags().BoolVar(&sshAcknowledge, "ack", false, "acknowledge destructive remote execution")
	sshStopCmd.Flags().BoolVar(&sshDryRun, "dry-run", false, "preview the remote shutdown without executing it")
	sshStopCmd.Flags().BoolVar(&sshAcknowledge, "ack", false, "acknowledge destructive remote execution")
	sshPutCmd.Flags().BoolVar(&sshDryRun, "dry-run", false, "preview the upload without transferring")
	sshPutCmd.Flags().BoolVar(&sshAcknowledge, "ack", false, "acknowledge overwriting the remote file")
	sshPutDirCmd.Flags().BoolVar(&sshDryRun, "dry-run", false, "preview the upload without transferring")
	sshPutDirCmd.Flags().BoolVar(&sshAcknowledge, "ack", false, "acknowledge overwriting remote files")
	sshCmd.AddCommand(sshExecCmd)
	sshCmd.AddCommand(sshStatusCmd)
	sshCmd.AddCommand(sshStopCmd)
	sshCmd.AddCommand(sshPutCmd)
	sshCmd.AddCommand(sshGetCmd)
	sshCmd.AddCommand(sshPutDirCmd)
	sshCmd.AddCommand(sshGetDirCmd)
}
