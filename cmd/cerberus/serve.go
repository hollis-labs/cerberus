package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/substrate/module"
	"github.com/hollis-labs/substrate/modules/database"
	"github.com/hollis-labs/substrate/modules/httphost"
)

// cerberus is what the commands share: the started framework app. ready, when set, learns
// the bound address (tests listen on port 0).
type cerberus struct {
	app   *module.App
	ready func(net.Addr)
}

func (c *cerberus) serveCmd(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the Cerberus HTTP host",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return c.serve(ctx) },
	}
}

// serve runs the HTTP host until ctx ends (SIGINT or SIGTERM), then drains: Serve stops
// accepting, waits for in-flight handlers and returns; the deferred boot Stop in run then
// stops the modules in reverse order. Background work (supervision, probes, scheduler,
// event export) joins this drain in its own slice, in the stop order of
// docs/substrate-port-plan.md section 4.4.
func (c *cerberus) serve(ctx context.Context) error {
	app := c.app
	if err := requireLoopback(app); err != nil {
		return err
	}
	if database.From(app) == nil {
		return errors.New("the database module must be started")
	}
	handler, err := httphost.Handler(app, httphost.Options{})
	if err != nil {
		return err
	}
	return httphost.Serve(ctx, app, handler, httphost.ServeOptions{
		Ready: func(addr net.Addr) {
			slog.Info("cerberus listening", "addr", addr.String())
			if c.ready != nil {
				c.ready(addr)
			}
		},
	})
}

// requireLoopback keeps the host on the loopback interface until identity and the scope
// table land (slice 4); a remote client reaches it through a local TLS proxy.
func requireLoopback(app *module.App) error {
	cfg, _ := app.Config["httphost"].(map[string]any)
	addr, _ := cfg["addr"].(string)
	if addr == "" {
		return errors.New("httphost.addr is empty; set a loopback address such as 127.0.0.1:4786")
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("cerberus requires a loopback httphost.addr (for example 127.0.0.1:4786), got %q", addr)
}
