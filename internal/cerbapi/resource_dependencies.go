package cerbapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
)

// resourceStartupOrder orders resources already selected by the caller. It
// never expands the set of services authorized for automatic startup.
func resourceStartupOrder(resources []config.ResourceDef, roots []string) ([]config.ResourceDef, []string) {
	byID := make(map[string]config.ResourceDef)
	for _, res := range resources {
		byID[res.ID] = res
	}
	if roots == nil {
		for _, res := range resources {
			roots = append(roots, res.ID)
		}
	}
	state := make(map[string]int)
	var ordered []config.ResourceDef
	var warnings []string
	var stack []string
	var visit func(string)
	visit = func(id string) {
		if state[id] == 2 {
			return
		}
		if state[id] == 1 {
			warnings = append(warnings, "dependency cycle: "+strings.Join(append(append([]string(nil), stack...), id), " -> "))
			return
		}
		res, ok := byID[id]
		if !ok {
			return
		}
		state[id] = 1
		stack = append(stack, id)
		for _, dep := range res.DependsOn {
			visit(dep)
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
		ordered = append(ordered, res)
	}
	for _, root := range roots {
		visit(root)
	}
	return ordered, warnings
}

// Dependencies remain advisory: an explicit start is allowed, with the
// unavailable dependency named. Cerberus does not silently start more services.
func (s *ResourceRuntimeService) dependencyWarnings(ctx context.Context, res *config.ResourceDef, cfg *config.ConfigV2) []string {
	if cfg == nil || len(res.DependsOn) == 0 {
		return nil
	}
	_, warnings := resourceStartupOrder(cfg.Resources, []string{res.ID})
	for _, id := range res.DependsOn {
		dep := findResourceDef(cfg, id)
		if dep == nil {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q, which is not configured", res.ID, id))
			continue
		}
		if dep.Connector != "local" || dep.Type != "process" {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q (%s/%s); availability was not checked", res.ID, id, dep.Connector, dep.Type))
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, resourceStatusProbeTimeout)
		state, err := s.localConnector().Status(probeCtx, resourceDefToDomain(dep))
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q; availability check failed: %v", res.ID, id, err))
			continue
		}
		if state != domain.StateRunning && state != domain.StateHealthy {
			warnings = append(warnings, fmt.Sprintf("resource %q requires dependency %q, currently %s; start/repair the dependency first", res.ID, id, state))
		}
	}
	return warnings
}
