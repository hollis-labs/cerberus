package main

import (
	"fmt"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/spf13/cobra"
)

const (
	outputFormatText = "text"
	outputFormatJSON = "json"
)

// addOutputFlag adds the standard -o/--output flag to a command. The flag
// value is bound to target; valid values are "text" (default) and "json".
// Commands that support JSON output should call this in their init() block
// and branch on the value in their RunE.
func addOutputFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVarP(target, "output", "o", outputFormatText, "output format: text|json")
}

// printJSON marshals v as indented JSON and writes it to stdout with a
// trailing newline. Use for --output json paths in list/status commands.
func printJSON(v any) error {
	data, err := redact.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	_, err = fmt.Println(string(data))
	return err
}
