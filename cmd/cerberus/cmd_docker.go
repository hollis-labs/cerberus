package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var dockerCmd = &cobra.Command{
	Use:   "docker",
	Short: "Docker operations",
}

var dockerPSCmd = &cobra.Command{
	Use:   "ps",
	Short: "List running containers",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newDockerConnectorService(cmd)
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "docker",
			Operation: "list_containers",
			Config:    dockerTargetConfig(cmd, nil),
		})
		if err != nil {
			return err
		}
		containers, ok := result.Data.([]dockerconn.Container)
		if !ok {
			return fmt.Errorf("docker ps: unexpected result type %T", result.Data)
		}

		if len(containers) == 0 {
			fmt.Println("No running containers.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tIMAGE\tSTATUS\tPORTS")
		fmt.Fprintln(w, "--\t----\t-----\t------\t-----")
		for _, c := range containers {
			ports := ""
			if len(c.Ports) > 0 {
				ports = c.Ports[0]
				if len(c.Ports) > 1 {
					ports += fmt.Sprintf(" (+%d)", len(c.Ports)-1)
				}
			}
			fmt.Fprintf(w, "%.12s\t%s\t%s\t%s\t%s\n",
				c.ID, c.Name, c.Image, c.Status, ports)
		}
		return w.Flush()
	},
}

var dockerLogsLines int

var dockerLogsCmd = &cobra.Command{
	Use:   "logs <container>",
	Short: "Show container logs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newDockerConnectorService(cmd)
		if err != nil {
			return err
		}
		defer closeFn()

		cfg, res, err := dockerOperationConfig(cmd, args[0])
		if err != nil {
			return err
		}
		if res != nil && res.Config["container"] == nil && res.Config["compose_file"] != nil {
			// A resource that only names a compose file has no single log
			// stream — the stack's containers carry their own names.
			return fmt.Errorf("resource %q declares a compose stack and no single container; "+
				"run `cerberus docker ps` and pass a container name from the stack", args[0])
		}
		cfg["lines"] = dockerLogsLines

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "docker",
			Operation: "logs",
			Config:    cfg,
		})
		if err != nil {
			return err
		}
		logs, ok := result.Data.(string)
		if !ok {
			return fmt.Errorf("docker logs: unexpected result type %T", result.Data)
		}

		fmt.Print(logs)
		return nil
	},
}

// dockerAck is --ack on up and down: starting and stopping are lifecycle
// operations, which need acknowledgment (Decision 14).
var dockerAck bool

var dockerUpCmd = &cobra.Command{
	Use:   "up <resource-id>",
	Short: "Start container or compose stack",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newDockerConnectorService(cmd)
		if err != nil {
			return err
		}
		defer closeFn()

		resourceID := args[0]
		cfg, res, err := dockerOperationConfig(cmd, resourceID)
		if err != nil {
			return err
		}

		if _, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "docker",
			Operation:    "start",
			Config:       cfg,
			Acknowledged: dockerAck,
		}); err != nil {
			return err
		}

		if composeFile := dockerComposeFile(cfg, res); composeFile != "" {
			fmt.Printf("Compose stack started: %s\n", composeFile)
			return nil
		}
		fmt.Printf("Container started: %s\n", resourceID)
		return nil
	},
}

var dockerDownCmd = &cobra.Command{
	Use:   "down <resource-id>",
	Short: "Stop container or compose stack (does not remove it)",
	Long: `Stops a container (docker stop) or a compose stack (docker compose stop).
Nothing is removed: containers, networks and volumes are kept, and
'cerberus docker up' starts the same stack again.

Removal (docker rm, docker compose down) is the connector's destroy operation,
which requires acknowledgment. There is no 'docker' verb for it yet.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newDockerConnectorService(cmd)
		if err != nil {
			return err
		}
		defer closeFn()

		resourceID := args[0]
		cfg, res, err := dockerOperationConfig(cmd, resourceID)
		if err != nil {
			return err
		}

		if _, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector:    "docker",
			Operation:    "stop",
			Config:       cfg,
			Acknowledged: dockerAck,
		}); err != nil {
			return err
		}

		if composeFile := dockerComposeFile(cfg, res); composeFile != "" {
			fmt.Printf("Compose stack stopped: %s\n", composeFile)
			return nil
		}

		fmt.Printf("Container stopped: %s\n", resourceID)
		return nil
	},
}

// dockerTargetConfig folds the --host/--context flags into an operation's
// config. Host selection is a property of the call, not of the process, so it
// travels in the payload rather than in the CLI's own environment. Either
// flag makes the operation run in this process (newDockerConnectorService);
// the socket refuses them.
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

// dockerOperationConfig builds the operation's config for an argument that
// names either a declared docker resource or a literal container.
//
// A declared resource is sent by id — `resource: <id>` — and whoever runs the
// operation resolves it against its config, so its compose file, container
// and host come from the declaration and never travel as free-form fields.
// That is what makes a `type: container` resource worth declaring: one
// carrying `compose_file` comes up by id with no `-f`. The literal fallback
// keeps `cerberus docker logs <container>` working for the containers nobody
// declared, which is most of them.
//
// The ad-hoc flags (--host, --context, -f) are added on top and win; they are
// accepted only in-process, which newDockerConnectorService arranges.
func dockerOperationConfig(cmd *cobra.Command, id string) (map[string]any, *config.ResourceDef, error) {
	res, err := lookupDockerResource(cmd, id)
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

// adHocDockerFlags reports whether the command names a docker target of its
// own: a daemon (--host, --context) or a compose file (-f).
func adHocDockerFlags(cmd *cobra.Command) bool {
	for _, name := range []string{"host", "context", "file"} {
		if value, err := cmd.Flags().GetString(name); err == nil && value != "" {
			return true
		}
	}
	return false
}

// newDockerConnectorService runs an ad-hoc docker target in this process. The
// socket, the web console and MCP refuse --host, --context and -f targets —
// a compose file is code execution on whichever daemon runs it — so they are
// offered only from the operator's own shell. Everything else goes through
// the daemon as usual.
func newDockerConnectorService(cmd *cobra.Command) (connectorExecutor, func(), error) {
	if adHocDockerFlags(cmd) {
		return app.NewExternalConnectorService(cfgPath), func() {}, nil
	}
	return newExternalConnectorService(cmd.Context())
}

// lookupDockerResource finds a declared docker resource by id. A miss is not an
// error — that is the literal-container path. A resource declared against
// another connector is, because `cerberus docker up muctlvaig` on a server/ssh
// resource is a mistake worth naming rather than silently retrying as a
// container of that name.
func lookupDockerResource(cmd *cobra.Command, id string) (*config.ResourceDef, error) {
	v2, err := registry.ResolveConfig(cfgPath)
	if err != nil {
		// Docker operations worked without any config before resources were
		// resolvable, and an undeclared container must keep working now. Say
		// what happened rather than failing or hiding it.
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not read config (%v); treating %q as a container name\n", err, id)
		return nil, nil
	}
	for i := range v2.Resources {
		if v2.Resources[i].ID != id {
			continue
		}
		res := &v2.Resources[i]
		if res.Connector != "docker" {
			return nil, fmt.Errorf("resource %q is %s/%s, not a docker resource; try: %s",
				res.ID, res.Type, res.Connector, cerbapi.UnsupervisedNextStep(res.ID, res.Connector))
		}
		return res, nil
	}
	return nil, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// dockerComposeFile reports the compose file an operation ran against — the
// -f override, or the declared resource's — so the CLI can describe what it
// actually did rather than what was typed.
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

func init() {
	dockerLogsCmd.Flags().IntVar(&dockerLogsLines, "lines", 50, "number of log lines to show")
	dockerUpCmd.Flags().StringP("file", "f", "", "compose file path (for compose mode); runs in this shell, not through the daemon")
	dockerDownCmd.Flags().StringP("file", "f", "", "compose file path (for compose mode); runs in this shell, not through the daemon")
	dockerUpCmd.Flags().BoolVar(&dockerAck, "ack", false, "acknowledge the start operation (lifecycle)")
	dockerDownCmd.Flags().BoolVar(&dockerAck, "ack", false, "acknowledge the stop operation (lifecycle)")
	for _, sub := range []*cobra.Command{dockerPSCmd, dockerLogsCmd, dockerUpCmd, dockerDownCmd} {
		sub.Flags().StringP("host", "H", "", "Docker daemon to target as a DOCKER_HOST value (ssh://user@host, tcp://host:2376); runs in this shell, not through the daemon")
		sub.Flags().String("context", "", "Docker context name to target (mutually exclusive with --host); runs in this shell, not through the daemon")
		dockerCmd.AddCommand(sub)
	}
}
