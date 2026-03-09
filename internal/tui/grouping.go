package tui

import (
	"sort"
	"strings"

	"github.com/chrispian/cerberus/internal/service"
)

// ServiceGroup represents a collection of services grouped by project name.
type ServiceGroup struct {
	Name      string
	Services  []*service.Service
	Collapsed bool
}

// GroupByProject groups services by their Def.Project field.
// Groups are sorted alphabetically by name. Services within each group
// maintain the order they were passed in.
func GroupByProject(services []*service.Service) []ServiceGroup {
	groupMap := make(map[string][]*service.Service)
	for _, svc := range services {
		project := svc.Def.Project
		if project == "" {
			project = "other"
		}
		groupMap[project] = append(groupMap[project], svc)
	}

	groups := make([]ServiceGroup, 0, len(groupMap))
	for name, svcs := range groupMap {
		groups = append(groups, ServiceGroup{
			Name:     name,
			Services: svcs,
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})

	return groups
}

// collectUniqueTags returns a sorted list of all unique tags across all services.
func collectUniqueTags(services []*service.Service) []string {
	tagSet := make(map[string]struct{})
	for _, svc := range services {
		for _, tag := range svc.Def.Tags {
			tagSet[tag] = struct{}{}
		}
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// filterServicesByTag returns only services that have the given tag.
func filterServicesByTag(services []*service.Service, tag string) []*service.Service {
	if tag == "" {
		return services
	}
	tag = strings.ToLower(tag)
	var result []*service.Service
	for _, svc := range services {
		for _, t := range svc.Def.Tags {
			if strings.ToLower(t) == tag {
				result = append(result, svc)
				break
			}
		}
	}
	return result
}

// flatItem represents either a group header or a service in the flat cursor list.
type flatItem struct {
	isHeader   bool
	groupIndex int
	svcIndex   int // index within the group's Services slice; -1 for headers
}

// buildFlatItems builds a flat list of items from groups, respecting collapsed state,
// text filter, and tag filter.
func buildFlatItems(groups []ServiceGroup) []flatItem {
	var items []flatItem
	for gi, group := range groups {
		if len(group.Services) == 0 {
			continue
		}
		items = append(items, flatItem{isHeader: true, groupIndex: gi, svcIndex: -1})
		if !group.Collapsed {
			for si := range group.Services {
				items = append(items, flatItem{isHeader: false, groupIndex: gi, svcIndex: si})
			}
		}
	}
	return items
}
