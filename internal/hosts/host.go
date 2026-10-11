// Package hosts is the Host object (ADR 0004): a named admin-lane target
// Cerberus keeps a record of. It is not a supervised resource.
//
// A Host's declared data is operator config (hosts.yaml). Its derived data
// (probed specs, state, last_seen) is measured, kept apart in State, and
// reports its age. Cerberus reports facts only: nothing here ranks, reserves
// or places, and no behavior differs by a host's role or labels.
package hosts

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Size is a byte count that reads in config as a number of bytes or as a
// string such as "16GiB" or "512MB".
type Size int64

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
	{"kb", 1000}, {"mb", 1000 * 1000}, {"gb", 1000 * 1000 * 1000}, {"tb", 1000 * 1000 * 1000 * 1000},
	{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30}, {"t", 1 << 40},
	{"b", 1},
}

// ParseSize reads a size such as "16GiB", "512 MB" or "1048576".
func ParseSize(s string) (Size, error) {
	raw := strings.ToLower(strings.TrimSpace(s))
	if raw == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(raw, u.suffix) {
			mult = u.mult
			raw = strings.TrimSpace(strings.TrimSuffix(raw, u.suffix))
			break
		}
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a size (use bytes or a value such as 16GiB)", s)
	}
	return Size(n * float64(mult)), nil
}

// UnmarshalYAML accepts a number of bytes or a size string.
func (z *Size) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := ParseSize(node.Value)
	if err != nil {
		return err
	}
	*z = parsed
	return nil
}

// Specs are the facts a host is declared to have. All are optional.
type Specs struct {
	OS     string `yaml:"os,omitempty" json:"os,omitempty"`
	Arch   string `yaml:"arch,omitempty" json:"arch,omitempty"`
	CPU    int    `yaml:"cpu,omitempty" json:"cpu,omitempty"`       // nominal logical cores
	Memory Size   `yaml:"memory,omitempty" json:"memory,omitempty"` // bytes
	Disk   Size   `yaml:"disk,omitempty" json:"disk,omitempty"`     // bytes
	GPU    string `yaml:"gpu,omitempty" json:"gpu,omitempty"`
}

// Reach is how Cerberus gets to the host: a reference to an existing
// server/ssh resource. The credential, key and host-key trust stay on that
// resource; a Host never carries one.
type Reach struct {
	// SSH is the id of a configured server/ssh resource.
	SSH string `yaml:"ssh,omitempty" json:"ssh,omitempty"`
}

// Cost is a monthly estimate for a cloud host.
type Cost struct {
	Monthly  float64 `yaml:"monthly,omitempty" json:"monthly,omitempty"`
	Currency string  `yaml:"currency,omitempty" json:"currency,omitempty"`
}

// Host is the declared record, as written in hosts.yaml.
type Host struct {
	// Identity.
	Name        string            `yaml:"name" json:"name"`
	Hostname    string            `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	TailnetName string            `yaml:"tailnet_name,omitempty" json:"tailnet_name,omitempty"`
	TailnetIP   string            `yaml:"tailnet_ip,omitempty" json:"tailnet_ip,omitempty"`
	LanIP       string            `yaml:"lan_ip,omitempty" json:"lan_ip,omitempty"`
	Role        string            `yaml:"role,omitempty" json:"role,omitempty"`
	Provider    string            `yaml:"provider,omitempty" json:"provider,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`

	Reach Reach `yaml:"reach,omitempty" json:"reach"`

	// Declared is what the operator says the machine is. It is never
	// overwritten by a probe.
	Declared Specs `yaml:"declared,omitempty" json:"declared"`

	// Lifecycle.
	CreatedAt *time.Time `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	Owner     string     `yaml:"owner,omitempty" json:"owner,omitempty"`
	TTL       string     `yaml:"ttl,omitempty" json:"ttl,omitempty"` // a Go duration, e.g. 72h
	Cost      *Cost      `yaml:"cost,omitempty" json:"cost,omitempty"`

	// Pointers. Both are opaque to Cerberus.
	TetherEnvironmentID string   `yaml:"tether_environment_id,omitempty" json:"tether_environment_id,omitempty"`
	BackupRefs          []string `yaml:"backup_refs,omitempty" json:"backup_refs,omitempty"`
}

// File is the shape of hosts.yaml.
type File struct {
	Version int `yaml:"version"`
	// ExtraRoles and ExtraProviders extend the known vocabularies: a role or
	// provider outside the built-in set is accepted only when listed here.
	ExtraRoles     []string `yaml:"extra_roles,omitempty"`
	ExtraProviders []string `yaml:"extra_providers,omitempty"`
	Hosts          []Host   `yaml:"hosts"`
}

// FileVersion is the hosts.yaml schema version this build reads.
const FileVersion = 1

// KnownRoles are the descriptive roles of compute-hosting.md. Cerberus uses a
// role only for display and filtering.
var KnownRoles = []string{"hub", "operator", "worker", "builder", "service"}

// KnownProviders are the built-in providers; a provider plugin id is added
// through File.ExtraProviders.
var KnownProviders = []string{"homelab"}
