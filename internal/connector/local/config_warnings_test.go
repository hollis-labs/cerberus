package local

import (
	"strings"
	"testing"
)

func TestProcessConfigTyposWarnWithoutLeakingValues(t *testing.T) {
	cfg := map[string]any{"command": []string{"/bin/true"}, "ENV": map[string]any{"API_KEY": "secret-sentinel"}, "future_knob": true,
		"build_strategy": map[string]any{"kind": "make_standard", "env_prefix": []string{"mise", "exec", "--"}, "rules": map[string]any{"custom_rule": true}},
		"health_check":   map[string]any{"URL": "http://localhost"},
	}
	spec, err := SpecFromResourceConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Env) != 0 {
		t.Fatal("uppercase ENV unexpectedly applied")
	}
	warnings := strings.Join(ProcessConfigWarnings(cfg), "\n")
	for _, want := range []string{"config.ENV", "use \"env\"", "config.future_knob", "config.health_check.URL"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("missing %q in %s", want, warnings)
		}
	}
	for _, forbidden := range []string{"secret-sentinel", "custom_rule", "env_prefix"} {
		if strings.Contains(warnings, forbidden) {
			t.Fatalf("value leaked or valid dynamic field rejected: %s", warnings)
		}
	}
}
