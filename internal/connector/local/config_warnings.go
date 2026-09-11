package local

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/chrispian/cerberus/internal/config"
)

// ProcessConfigWarnings uses the typed contracts' YAML field names. Dynamic
// maps (environment variables and strategy-specific rules) remain open. Values
// are never included: an unknown ENV key may itself contain credentials.
func ProcessConfigWarnings(cfg map[string]any) []string {
	warnings := unknownConfigKeys(cfg, reflect.TypeOf(ProcessSpec{}), "config", "build")
	if strategy, ok := cfg["build_strategy"].(map[string]any); ok {
		warnings = append(warnings, unknownConfigKeys(strategy, reflect.TypeOf(BuildStrategyConfig{}), "config.build_strategy")...)
	}
	if health, ok := cfg["health_check"].(map[string]any); ok {
		warnings = append(warnings, unknownConfigKeys(health, reflect.TypeOf(config.HealthCheck{}), "config.health_check")...)
	}
	sort.Strings(warnings)
	return warnings
}

func unknownConfigKeys(values map[string]any, schema reflect.Type, path string, extras ...string) []string {
	known := make(map[string]bool)
	for i := 0; i < schema.NumField(); i++ {
		name := strings.Split(schema.Field(i).Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			known[name] = true
		}
	}
	for _, key := range extras {
		known[key] = true
	}
	var warnings []string
	for key := range values {
		if known[key] {
			continue
		}
		msg := fmt.Sprintf("unrecognized local process key %s.%s; this value is ignored", path, key)
		for candidate := range known {
			if strings.EqualFold(key, candidate) {
				msg += fmt.Sprintf("; use %q (case-sensitive)", candidate)
				break
			}
		}
		warnings = append(warnings, msg)
	}
	return warnings
}
