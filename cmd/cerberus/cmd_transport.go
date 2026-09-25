package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/spf13/cobra"
)

type connectorExecutor interface {
	Execute(context.Context, cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error)
}

type socketConnectorExecutor struct{ client *cerbapi.SocketClient }

func (s socketConnectorExecutor) Execute(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return s.client.ExecuteTypedConnectorOperation(ctx, args)
}

// Choose a transport before executing anything. Once an operation is sent,
// an error must never trigger an in-process retry of a possible mutation.
func commandSocket(ctx context.Context) (*cerbapi.SocketClient, error) {
	// Foreground commands used to run without a wall-clock limit. Keep that
	// behavior while bounding the availability probe separately.
	client, err := newResourceSocketClient(cerbapi.WithClientTimeout(0), cerbapi.WithClientLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, err
	}
	probe, cancel := context.WithTimeout(ctx, cerbapi.DialTimeout)
	defer cancel()
	if err = client.Ping(probe); err == nil {
		return client, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var unreachable *cerbapi.DaemonUnreachableError
	if errors.As(err, &unreachable) {
		return nil, nil
	}
	return nil, err
}

func newExternalConnectorService(ctx context.Context) (connectorExecutor, func(), error) {
	// A resource id is resolved by whoever runs the operation. An explicit
	// --config names a config the daemon may not be serving, so it selects the
	// in-process lane, where the id resolves against that config.
	if !explicitConfig() {
		client, err := commandSocket(ctx)
		if err != nil {
			return nil, nil, err
		}
		if client != nil {
			return socketConnectorExecutor{client}, func() {}, nil
		}
	}
	return newLocalConnectorExecutor(), func() {}, nil
}

// localConnectorExecutor is the in-process lane: the operator's own shell.
// It is the one place the CLI marks its calls SurfaceInProcess, which is what
// lets local-only inputs (docker --host, --context, -f) through. Anything that
// reaches the service unmarked is treated as remote.
type localConnectorExecutor struct {
	svc *cerbapi.ExternalConnectorService
}

func (l localConnectorExecutor) Execute(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return l.svc.Execute(cerbapi.WithCallerSurface(ctx, cerbapi.SurfaceInProcess), args)
}

func newLocalConnectorExecutor() connectorExecutor {
	return localConnectorExecutor{svc: app.NewExternalConnectorService(cfgPath)}
}

// explicitConfig reports whether the operator passed --config.
func explicitConfig() bool {
	flag := rootCmd.PersistentFlags().Lookup("config")
	return flag != nil && flag.Changed
}

type pipelineClient interface {
	ListPipelines(context.Context) ([]cerbapi.PipelineInfo, error)
	GetPipeline(context.Context, string) (*cerbapi.PipelineDetail, error)
	RunPipeline(context.Context, string) (*cerbapi.PipelineRunResult, error)
}

func newPipelineClient(cmd *cobra.Command) (pipelineClient, error) {
	// An explicit config selects a standalone config, not the daemon's registry.
	if flag := cmd.Flag("config"); flag == nil || !flag.Changed {
		client, err := commandSocket(cmd.Context())
		if err != nil {
			return nil, err
		}
		if client != nil {
			return client, nil
		}
	}
	cfg, err := loadUnifiedForTools(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("init app: %w", err)
	}
	return cerbapi.NewResourceRuntimeService(cerbapi.WithResourceRuntimeConfigPath(cfgPath), cerbapi.WithResourceRuntimeConfigV2(cfg)), nil
}
