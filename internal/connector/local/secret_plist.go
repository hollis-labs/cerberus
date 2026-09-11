package local

import (
	"encoding/xml"
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus/internal/secretref"
)

type plistNode struct {
	XMLName  xml.Name
	Text     string      `xml:",chardata"`
	Children []plistNode `xml:",any"`
}

func (n plistNode) value(key string) plistNode {
	for i := 0; i+1 < len(n.Children); i++ {
		if n.Children[i].XMLName.Local == "key" && n.Children[i].Text == key {
			return n.Children[i+1]
		}
	}
	return plistNode{}
}

// CheckSecretReferencePlist detects the stale-generator trap without resolving
// credentials: references must survive on disk and run-secrets must front exec.
// It checks the generated plist, not the live process's resolved environment.
func CheckSecretReferencePlist(path string, spec ProcessSpec) error {
	env, _ := launchdEnvironment(spec)
	if !secretref.EnvHasRefs(env) {
		return nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // managed plist path from the resource install layout
	if err != nil {
		return fmt.Errorf("read secret-reference plist: %w", err)
	}
	return validateSecretReferencePlist(data, env)
}

func validateSecretReferencePlist(data []byte, expected map[string]string) error {
	var root plistNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse secret-reference plist: %w", err)
	}
	var dict plistNode
	for _, child := range root.Children {
		if child.XMLName.Local == "dict" {
			dict = child
			break
		}
	}
	args := dict.value("ProgramArguments").Children
	if len(args) < 4 || args[0].XMLName.Local != "string" || args[0].Text == "" || args[1].Text != "run-secrets" || args[2].Text != "--" {
		return fmt.Errorf("secret references require ProgramArguments to start with cerberus run-secrets --; upgrade the serving daemon and re-apply the resource")
	}
	for _, arg := range args {
		if arg.XMLName.Local != "string" {
			return fmt.Errorf("ProgramArguments contains a non-string argument")
		}
	}
	env := dict.value("EnvironmentVariables")
	for key, value := range expected {
		if secretref.IsRef(value) && env.value(key).Text != value {
			return fmt.Errorf("plist environment %s must retain its configured secret reference; re-apply the resource", key)
		}
	}
	return nil
}
