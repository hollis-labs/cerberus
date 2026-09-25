package docker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The connector reads config only through the key table in config_keys.go.
// A literal key read anywhere else — a new alias written inline — fails here,
// because the admin lane's socket/web allow-list is built from that table and
// would not know the alias exists.
func TestConnectorReadsConfigOnlyThroughTheKeyTable(t *testing.T) {
	// Literal key reads, and an inline alias list ranged over to index config
	// (`for _, key := range []string{"compose_file", ...} { … Config[key] …`),
	// which is how the readers were written before the table existed.
	literalRead := regexp.MustCompile(`(?:Config|cfg)\[\s*"(\w+)"\s*\]|trimmedString\(cfg,\s*"(\w+)"\)|range \[\]string\{[^}]*\}\s*\{[^}]*(?:Config|cfg)\[`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "config_keys.go" {
			continue
		}
		src, err := os.ReadFile(file) //nolint:gosec // this package's own source files
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range literalRead.FindAllString(string(src), -1) {
			t.Errorf("%s reads config key literally (%s); add it to config_keys.go", file, m)
		}
	}
}
