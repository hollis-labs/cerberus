package config

import (
	"reflect"
	"sort"
)

// Diff describes the set of service-slug changes between two Config snapshots.
//
// Slugs are sorted alphabetically in each slice to keep log output stable.
type Diff struct {
	Added   []string
	Removed []string
	Changed []string
}

// IsEmpty returns true if there are no added, removed, or changed services.
func (d Diff) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// DiffConfigs compares two Config snapshots and returns the set of
// added / removed / changed service IDs.
//
// Comparison rules:
//   - A slug present only in new -> Added.
//   - A slug present only in old -> Removed.
//   - A slug in both whose ServiceDef differs (reflect.DeepEqual) -> Changed.
//
// DeepEqual is sufficient here because Load() canonicalises the structs:
// tilde expansion happens during parse, legacy Health maps into
// HealthCheckCfg.URL, and map/slice zero-values are nil rather than empty.
// That gives us stable equality without hand-rolled field comparisons.
func DiffConfigs(oldCfg, newCfg *Config) Diff {
	oldByID := indexServices(oldCfg)
	newByID := indexServices(newCfg)

	var added, removed, changed []string

	for id, newDef := range newByID {
		oldDef, ok := oldByID[id]
		if !ok {
			added = append(added, id)
			continue
		}
		if !reflect.DeepEqual(oldDef, newDef) {
			changed = append(changed, id)
		}
	}

	for id := range oldByID {
		if _, ok := newByID[id]; !ok {
			removed = append(removed, id)
		}
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)

	return Diff{Added: added, Removed: removed, Changed: changed}
}

// indexServices builds an id -> ServiceDef map. Nil configs are handled as
// empty so diffing against a pre-initial-load state works cleanly.
func indexServices(cfg *Config) map[string]ServiceDef {
	out := make(map[string]ServiceDef)
	if cfg == nil {
		return out
	}
	for _, svc := range cfg.Services {
		if svc.ID == "" {
			continue
		}
		out[svc.ID] = svc
	}
	return out
}
