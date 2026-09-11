package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chrispian/cerberus/internal/config"
)

const duplicatePortField = "duplicate-port"

type PortConflict struct {
	Port      int64
	Resources []config.ResourceDef
}

func (c PortConflict) String() string {
	names := make([]string, 0, len(c.Resources))
	for _, r := range c.Resources {
		names = append(names, fmt.Sprintf("%q (project %q)", r.ID, r.Project))
	}
	return fmt.Sprintf("TCP port %d is assigned to multiple local resources: %s; assign distinct ports before activation", c.Port, strings.Join(names, ", "))
}

// PortConflicts checks local TCP listeners only. Omitted/invalid ports and
// remote connectors do not compete for this machine's port namespace.
func PortConflicts(resources []config.ResourceDef) []PortConflict {
	ports := map[int64]map[string]config.ResourceDef{}
	for _, r := range resources {
		if r.Connector != "local" || r.Type != "process" {
			continue
		}
		port, ok := resourcePort(r)
		if !ok || port <= 0 {
			continue
		}
		if ports[port] == nil {
			ports[port] = map[string]config.ResourceDef{}
		}
		ports[port][r.ID] = r
	}
	var conflicts []PortConflict
	for port, group := range ports {
		if len(group) < 2 {
			continue
		}
		conflict := PortConflict{Port: port}
		for _, r := range group {
			conflict.Resources = append(conflict.Resources, r)
		}
		sort.Slice(conflict.Resources, func(i, j int) bool { return conflict.Resources[i].ID < conflict.Resources[j].ID })
		conflicts = append(conflicts, conflict)
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Port < conflicts[j].Port })
	return conflicts
}

// PortConflictIssues are fatal when authoring, but stay warnings during
// resolution so an existing collision remains inspectable and stoppable.
func (r ValidationResult) PortConflictIssues() []ValidationIssue {
	var issues []ValidationIssue
	for _, issue := range r.Issues {
		if issue.Field == duplicatePortField {
			issues = append(issues, issue)
		}
	}
	return issues
}
