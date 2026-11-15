package mcp

import (
	"context"
	"errors"
	"sort"

	gmcp "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/hollis-labs/substrate/module"
	"github.com/hollis-labs/substrate/modules/auth"
	smcp "github.com/hollis-labs/substrate/modules/mcp"
)

// Register adds every tool of the old catalog to the app's MCP server, each with the scope it
// needs in the app's scope table and a per-call Authorize, so a principal without the scope is
// refused before the tool runs (over HTTP and over stdio alike; approve is no tool's scope).
// Tools without a backend in this build answer that they are not available, naming the slice.
func Register(app *module.App, b Backend) error {
	srv := smcp.Server(app)
	if srv == nil {
		return errors.New("mcp: the mcp module is not started")
	}
	defs, err := Catalog()
	if err != nil {
		return err
	}
	table := auth.ScopeTable(app)
	for _, d := range defs {
		name, scope := d.Name, scopeOf(d)
		if table != nil {
			table.Tool(name, scope)
		}
		run, ok := handlers[name]
		if !ok {
			run = notYet(name, ownerOf(name))
		}
		srv.RegisterTool(gmcp.Tool{
			Name: name, Title: d.Title, Description: d.Description, InputSchema: d.InputSchema, OutputSchema: d.OutputSchema,
			ReadOnlyHint: d.ReadOnlyHint, DestructiveHint: d.DestructiveHint, IdempotentHint: d.IdempotentHint, OpenWorldHint: d.OpenWorldHint,
			Handler: withRequestScope(func(ctx context.Context, args map[string]any) (any, error) {
				if err := smcp.Authorize(ctx, app, name); err != nil {
					return nil, err
				}
				return run(ctx, b, args)
			}),
		})
	}
	return nil
}

// Pending is every catalog tool this build does not serve yet, with the slice that brings it,
// sorted by name. The scheduler's tools are not in it: they are served and answer that the
// scheduler is off (SchedulerOff).
func Pending() []PendingTool {
	return listBy(func(owner string) bool { return owner != OwnerHere && owner != OwnerSchedulerOff })
}

// SchedulerOff is the tools that answer that the scheduler is off.
func SchedulerOff() []PendingTool {
	return listBy(func(owner string) bool { return owner == OwnerSchedulerOff })
}

// PendingTool is a tool and the slice that brings its backend.
type PendingTool struct{ Name, Owner string }

func listBy(keep func(string) bool) []PendingTool {
	defs, _ := Catalog()
	var out []PendingTool
	for _, d := range defs {
		if _, served := handlers[d.Name]; served {
			continue
		}
		if o := ownerOf(d.Name); keep(o) {
			out = append(out, PendingTool{Name: d.Name, Owner: o})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
