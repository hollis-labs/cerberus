package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/service"
	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:   "logs <service>",
	Short: "Tail legacy service logs",
	Long:  "Tails the log file for a legacy v1 service defined under services:. Use -f to follow. This command does not yet expose v2 os_service logs.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		services, err := loadServices()
		if err != nil {
			return err
		}

		var target *service.ManagedService
		for _, svc := range services {
			if strings.EqualFold(svc.Def.ID, args[0]) {
				target = svc
				break
			}
		}
		if target == nil {
			return fmt.Errorf("service %q not found", args[0])
		}

		logPath := target.LogPath()
		if _, err := os.Stat(logPath); os.IsNotExist(err) { //nolint:govet
			return fmt.Errorf("no log file found at %s", logPath)
		}

		if !logsFollow {
			data, err := os.ReadFile(logPath) //nolint:gosec,govet
			if err != nil {
				return fmt.Errorf("reading log: %w", err)
			}
			fmt.Print(string(data))
			return nil
		}

		// Follow mode: read existing content then tail
		f, err := os.Open(logPath) //nolint:gosec
		if err != nil {
			return fmt.Errorf("opening log: %w", err)
		}
		defer f.Close() //nolint:errcheck

		// Print existing content
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return fmt.Errorf("reading log: %w", err)
		}

		// Follow new content
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

		for {
			select {
			case <-sig:
				return nil
			default:
				n, _ := io.Copy(os.Stdout, f)
				if n == 0 {
					time.Sleep(200 * time.Millisecond)
				}
			}
		}
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "follow log output")
}
