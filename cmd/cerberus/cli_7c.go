package main

import (
	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/registry"
)

// addFollowUpCommands adds slice 7c's verbs to root (the old CLI's verbs over lanes this branch
// serves, docs/conversion-notes.md "CLI verb parity"), then the pending ones, which join the
// groups already there.
func (c *cerberus) addFollowUpCommands(root *cobra.Command) error {
	client := func() (*apiClient, error) { return newAPIClient(c.app, c.home) }
	reg := func() (*registry.Registry, error) { return registryFor(c) }
	validate, configValidate := validateCommands(reg)
	root.AddCommand(c.approvalsCommand(), projectCommand(client), hostCommand(client), dockerCommand(client, reg), sshCommand(client), validate)
	if config := childNamed(root, "config"); config != nil {
		config.AddCommand(configValidate)
	}
	if connectors := childNamed(root, "connectors"); connectors != nil {
		connectors.AddCommand(prototypeCommand())
	}
	return addPendingCommands(root, pendingSpecs)
}

// childNamed is c's subcommand named name, or nil.
func childNamed(c *cobra.Command, name string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}
