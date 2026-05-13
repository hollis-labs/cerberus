package domain

import "github.com/chrispian/cerberus/pkg/connector"

// Connector manages resources of a specific type via a specific provider.
// The local connector wraps OS process management; the DigitalOcean connector
// wraps the DO API; the Docker connector wraps the Docker daemon; etc.
type Connector = connector.Connector

// ConnectorCapabilities declares what operations a connector supports.
type ConnectorCapabilities = connector.Capabilities
