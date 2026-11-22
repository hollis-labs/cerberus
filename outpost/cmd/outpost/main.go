package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/hollis-labs/tools/outpost/internal/service"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "outpost:", err)
		os.Exit(1)
	}
}

func run() error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("outpost", flag.ContinueOnError)
	config := flags.String("config", filepath.Join(base, "outpost", "outpost.json"), "machine plugin inventory")
	id := flags.String("id", "", "pairing/controller name")
	plugin := flags.String("plugin", "", "plugin permitted by this pairing")
	output := flags.String("output", "", "new private credential file for the controller")
	if err = flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: outpost [--config path] [--id name --plugin id --output file] run|pair|revoke|version")
	}
	if flags.Arg(0) == "version" {
		fmt.Println(version)
		return nil
	}
	cfg, err := service.ReadConfig(*config)
	if err != nil {
		return err
	}
	switch flags.Arg(0) {
	case "pair":
		return service.Pair(cfg, *id, *plugin, *output)
	case "revoke":
		return service.Revoke(cfg, *id)
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return service.Run(ctx, cfg, version)
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}
