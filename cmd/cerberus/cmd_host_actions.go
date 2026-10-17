package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/pkg/outpost"
	"github.com/spf13/cobra"
)

func (l localHostClient) PlanHostAction(ctx context.Context, name string, args cerbapi.HostActionArgs) (*cerbapi.ConnectorPlan, error) {
	return l.svc.PlanAction(inProcessContext(ctx), name, args)
}
func (l localHostClient) ExecuteHostAction(ctx context.Context, name string, args cerbapi.HostActionArgs) (outpost.Result, error) {
	return l.svc.ExecuteAction(inProcessContext(ctx), name, args)
}

func init() {
	for _, planning := range []bool{true, false} {
		name := "act"
		if planning {
			name = "plan"
		}
		var ack bool
		var approval string
		var lines int
		cmd := &cobra.Command{Use: name + " <host> <units|logs|start|stop|restart> [unit]", Short: "Plan or execute a scoped Outpost unit operation through Cerberus policy", Args: cobra.RangeArgs(2, 3),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newHostClient(cmd)
				if err != nil {
					return err
				}
				c, ok := client.(cerbapi.HostActionClient)
				if !ok {
					return fmt.Errorf("serving daemon has no host actions")
				}
				request := outpost.Request{Verb: args[1], Lines: lines}
				if len(args) > 2 {
					request.Unit = args[2]
				}
				a := cerbapi.HostActionArgs{Request: request, Acknowledged: ack, ApprovalID: approval}
				var result any
				if planning {
					result, err = c.PlanHostAction(cmd.Context(), args[0], a)
				} else {
					result, err = c.ExecuteHostAction(cmd.Context(), args[0], a)
				}
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}}
		cmd.Flags().BoolVar(&ack, "ack", false, "acknowledge the requested operation")
		cmd.Flags().StringVar(&approval, "approval-id", "", "out-of-band approval bound to the plan")
		cmd.Flags().IntVar(&lines, "lines", 0, "log line limit (logs only; zero uses 100)")
		hostCmd.AddCommand(cmd)
	}
}
