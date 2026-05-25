package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHomePath expands "~" and "~/" against the current user's home
// directory. Non-home-prefixed values are returned unchanged.
func ExpandHomePath(value string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return value
	}
	switch {
	case value == "~":
		return home
	case strings.HasPrefix(value, "~/"):
		return filepath.Join(home, value[2:])
	default:
		return value
	}
}

func expandHomeSlice(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = ExpandHomePath(value)
	}
	return out
}

func expandHomeMapValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = ExpandHomePath(value)
	}
	return out
}

func normalizeV2Config(cfg *ConfigV2) {
	if cfg == nil {
		return
	}
	if cfg.Build == nil {
		cfg.Build = &BuildConfig{}
	}
	if cfg.Build.InstallAfterBuild == nil {
		t := true
		cfg.Build.InstallAfterBuild = &t
	}
	for i := range cfg.Resources {
		res := &cfg.Resources[i]
		if res.Connector != "local" || res.Type != "process" || res.Config == nil {
			continue
		}
		normalizeLocalProcessResourceConfig(res.Config)
	}
	for i := range cfg.Pipelines {
		for j := range cfg.Pipelines[i].Stages {
			for k := range cfg.Pipelines[i].Stages[j].Actions {
				cfg.Pipelines[i].Stages[j].Actions[k].Dir = ExpandHomePath(cfg.Pipelines[i].Stages[j].Actions[k].Dir)
			}
		}
	}
}

func normalizeLocalProcessResourceConfig(cfg map[string]any) {
	for _, key := range []string{"dir", "env_file", "log_file", "artifact_path", "install_root", "install_work_dir"} {
		if raw, ok := cfg[key].(string); ok {
			cfg[key] = ExpandHomePath(raw)
		}
	}
	for _, key := range []string{"command"} {
		switch values := cfg[key].(type) {
		case []string:
			cfg[key] = expandHomeSlice(values)
		case []any:
			out := make([]any, 0, len(values))
			for _, value := range values {
				if str, ok := value.(string); ok {
					out = append(out, ExpandHomePath(str))
				} else {
					out = append(out, value)
				}
			}
			cfg[key] = out
		}
	}
	normalizeBuildStrategyConfig(cfg)
	switch values := cfg["env"].(type) {
	case map[string]string:
		cfg["env"] = expandHomeMapValues(values)
	case map[string]any:
		out := make(map[string]any, len(values))
		for key, value := range values {
			if str, ok := value.(string); ok {
				out[key] = ExpandHomePath(str)
			} else {
				out[key] = value
			}
		}
		cfg["env"] = out
	}
	if raw, ok := cfg["health_check"].(map[string]any); ok {
		switch values := raw["command"].(type) {
		case []string:
			raw["command"] = expandHomeSlice(values)
		case []any:
			out := make([]any, 0, len(values))
			for _, value := range values {
				if str, ok := value.(string); ok {
					out = append(out, ExpandHomePath(str))
				} else {
					out = append(out, value)
				}
			}
			raw["command"] = out
		}
		cfg["health_check"] = raw
	}
}

func normalizeBuildStrategyConfig(cfg map[string]any) {
	raw, ok := cfg["build_strategy"].(map[string]any)
	if !ok {
		return
	}
	for _, section := range []string{"source", "rules"} {
		m, ok := raw[section].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range m {
			switch typed := value.(type) {
			case string:
				m[key] = ExpandHomePath(typed)
			case []string:
				m[key] = expandHomeSlice(typed)
			case []any:
				out := make([]any, 0, len(typed))
				for _, item := range typed {
					if str, ok := item.(string); ok {
						out = append(out, ExpandHomePath(str))
					} else {
						out = append(out, item)
					}
				}
				m[key] = out
			}
		}
	}
}
