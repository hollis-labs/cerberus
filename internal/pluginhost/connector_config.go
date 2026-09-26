package pluginhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"gopkg.in/yaml.v3"
)

// ConnectorConfigFilename is the operator-owned file of plugin settings,
// beside connector-secrets.yaml:
//
//	<plugin id>:
//	  fields: {address: http://127.0.0.1:14444, sandbox: true}
//	  mcp:
//	    expose: [list_zones, list_dns_records]
//
// fields carries the plugin's declared, non-secret config fields. Some of them
// pick the target a plugin acts on (a gateway address, an API server, a
// kubeconfig), so they come from a file the operator edits and never from a
// caller: Cerberus has no socket, web or MCP path that writes it. mcp.expose
// names the operations whose generated MCP tools are served; nothing is
// exposed by default.
const ConnectorConfigFilename = "connector-config.yaml"

// PluginSettings is one plugin's entry in the file.
type PluginSettings struct {
	Fields map[string]any `yaml:"fields"`
	MCP    MCPSettings    `yaml:"mcp"`
}

// MCPSettings is the plugin's MCP exposure: the operations, by name, whose
// generated tools are served.
type MCPSettings struct {
	Expose []string `yaml:"expose"`
}

// ConnectorConfig is the parsed file. The zero value is an absent file: no
// fields, nothing exposed.
type ConnectorConfig struct {
	Path string
	// SHA256 fingerprints the file's bytes, so an operator can see which
	// version of it a loaded plugin read. Empty when the file is absent.
	SHA256   string
	Entries  map[string]PluginSettings
	Warnings []string
}

// LoadConnectorConfig reads the file at path. An absent file is not an error:
// it means no plugin has settings. A file that does not parse is an error,
// and callers fail closed on it — a broken file may have been meant to point
// a plugin somewhere other than its default.
func LoadConnectorConfig(path string) (ConnectorConfig, error) {
	cfg := ConnectorConfig{Path: path, Entries: map[string]PluginSettings{}}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // an operator-owned path beside the global config
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	cfg.SHA256 = hex.EncodeToString(sum[:])

	if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o022 != 0 {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
			"%s is group- or world-writable (mode %04o); it chooses the systems plugins act on, so restrict it (chmod 644 or 600)",
			path, info.Mode().Perm()))
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// An empty file is a valid file with no entries; yaml.v3 reports io.EOF.
	if err := decoder.Decode(&cfg.Entries); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Entries == nil {
		cfg.Entries = map[string]PluginSettings{}
	}
	return cfg, nil
}

// ResolvedSettings is one plugin's settings, checked against its manifest.
type ResolvedSettings struct {
	// Config is what the plugin receives in Init beside its secrets: each
	// declared field's value, rendered as the string the SDK's ConfigReader
	// parses.
	Config map[string]string
	// Fields names the fields delivered. Names only.
	Fields []string
	// Expose names the operations whose MCP tools are served.
	Expose []string
	// ExposeDeclared is true when connector-config.yaml has an mcp.expose
	// list for the plugin, even an empty one. Without one the posture
	// decides: nothing under secure, every declared operation under
	// permissive (ExposableOperations).
	ExposeDeclared bool
	// Problems are why the settings were refused. Any problem refuses the
	// plugin's load, because a field that was meant to choose a target and
	// was dropped would leave the plugin acting on its default instead.
	Problems []string
	SHA256   string
	Warnings []string
}

// ForPlugin checks the plugin's entry against its manifest: only declared
// fields, each of its declared type, no secret reference in any of them, and
// only declared operations in mcp.expose.
func (c ConnectorConfig) ForPlugin(plugin InstalledPlugin) ResolvedSettings {
	out := ResolvedSettings{Config: map[string]string{}, Fields: []string{}, Expose: []string{}, Problems: []string{}, SHA256: c.SHA256, Warnings: append([]string(nil), c.Warnings...)}
	entry, ok := c.Entries[plugin.ID]
	if !ok {
		return out
	}

	declared := map[string]contract.ConfigField{}
	var declaredNames []string
	for _, field := range plugin.Manifest.Config.Fields {
		declared[field.Name] = field
		declaredNames = append(declaredNames, field.Name)
	}
	sort.Strings(declaredNames)

	names := make([]string, 0, len(entry.Fields))
	for name := range entry.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field, ok := declared[name]
		if !ok {
			out.Problems = append(out.Problems, fmt.Sprintf("field %q is not declared by the plugin (declared: %s)", name, joinOrNone(declaredNames)))
			continue
		}
		value := entry.Fields[name]
		if ref := referenceIn(value); ref != "" {
			out.Problems = append(out.Problems, fmt.Sprintf("field %q holds a secret reference (%s); references belong in connector-secrets.yaml, and this file takes literal values only", name, ref))
			continue
		}
		rendered, err := renderField(field.Type, value)
		if err != nil {
			out.Problems = append(out.Problems, fmt.Sprintf("field %q: %v", name, err))
			continue
		}
		out.Config[name] = rendered
		out.Fields = append(out.Fields, name)
	}

	ops := map[string]bool{}
	var opNames []string
	for _, op := range plugin.Manifest.Operations {
		ops[op.Name] = true
		opNames = append(opNames, op.Name)
	}
	sort.Strings(opNames)
	// A plugin may narrow itself: an operation it declares CLI-only never
	// reaches MCP, whatever this file says. It is not a problem — the
	// plugin still loads — but the operator is told why the tool is absent.
	cliOnly := map[string]bool{}
	for _, name := range plugin.Spec.Cerberus.Surfaces.CLIOnly {
		cliOnly[name] = true
	}
	out.ExposeDeclared = entry.MCP.Expose != nil
	seen := map[string]bool{}
	for _, name := range entry.MCP.Expose {
		switch {
		case !ops[name]:
			out.Problems = append(out.Problems, fmt.Sprintf("mcp.expose names %q, which the plugin does not declare (operations: %s)", name, joinOrNone(opNames)))
		case cliOnly[name]:
			out.Warnings = append(out.Warnings, fmt.Sprintf("plugin %q declares %q CLI-only, so it is not exposed to MCP although %s lists it", plugin.ID, name, ConnectorConfigFilename))
		case !seen[name]:
			seen[name] = true
			out.Expose = append(out.Expose, name)
		}
	}
	sort.Strings(out.Expose)
	return out
}

// ExposableOperations are every operation the plugin declares that it does
// not keep CLI-only: what the permissive posture exposes to MCP when
// connector-config.yaml says nothing (section 13).
func ExposableOperations(plugin InstalledPlugin) []string {
	cliOnly := map[string]bool{}
	for _, name := range plugin.Spec.Cerberus.Surfaces.CLIOnly {
		cliOnly[name] = true
	}
	out := []string{}
	for _, op := range plugin.Manifest.Operations {
		if !cliOnly[op.Name] {
			out = append(out, op.Name)
		}
	}
	sort.Strings(out)
	return out
}

// referenceIn returns the scheme of the first secret reference in value, at
// any depth, or "".
func referenceIn(value any) string {
	switch v := value.(type) {
	case string:
		for _, scheme := range []string{"keychain://", "helper://", "op://"} {
			if strings.HasPrefix(strings.TrimSpace(v), scheme) {
				return scheme
			}
		}
	case []any:
		for _, item := range v {
			if ref := referenceIn(item); ref != "" {
				return ref
			}
		}
	case map[string]any:
		for _, item := range v {
			if ref := referenceIn(item); ref != "" {
				return ref
			}
		}
	}
	return ""
}

// renderField checks value against the field's declared type and renders it
// as the string the plugin reads: ConfigReader.String, .Bool and .Int parse
// exactly these forms. A structured value is rendered as JSON.
func renderField(fieldType string, value any) (string, error) {
	switch fieldType {
	case "string":
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("want a string, got %T", value)
		}
		return s, nil
	case "boolean", "bool":
		b, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("want true or false, got %T", value)
		}
		return strconv.FormatBool(b), nil
	case "integer", "int":
		switch n := value.(type) {
		case int:
			return strconv.Itoa(n), nil
		case float64:
			if n == math.Trunc(n) {
				return strconv.FormatInt(int64(n), 10), nil
			}
		}
		return "", fmt.Errorf("want a whole number, got %v", value)
	case "number":
		switch n := value.(type) {
		case int:
			return strconv.Itoa(n), nil
		case float64:
			return strconv.FormatFloat(n, 'f', -1, 64), nil
		}
		return "", fmt.Errorf("want a number, got %T", value)
	case "array":
		if _, ok := value.([]any); !ok {
			return "", fmt.Errorf("want a list, got %T", value)
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return "", fmt.Errorf("want a mapping, got %T", value)
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("cannot render: %w", err)
	}
	return string(data), nil
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
