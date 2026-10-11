package hosts

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Problem is one validation finding, addressed to the field that caused it.
type Problem struct {
	Host    string `json:"host,omitempty"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Host == "" {
		return fmt.Sprintf("%s: %s", p.Field, p.Message)
	}
	return fmt.Sprintf("host %q: %s: %s", p.Host, p.Field, p.Message)
}

// ValidationError carries every problem found, so an operator fixes the file
// once.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return "invalid hosts config:\n  " + strings.Join(lines, "\n  ")
}

var (
	nameRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	labelKeyRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._/-]{0,61}[a-z0-9])?$`)
	identRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

const (
	maxLabels     = 32
	maxLabelValue = 63
)

var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Validate checks a parsed hosts file. It returns nil or a *ValidationError.
func Validate(f *File) error {
	var probs []Problem
	add := func(host, field, format string, args ...any) {
		probs = append(probs, Problem{Host: host, Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if f.Version != FileVersion {
		add("", "version", "must be %d (got %d)", FileVersion, f.Version)
	}
	roles := slices.Clone(KnownRoles)
	for i, r := range f.ExtraRoles {
		if !identRE.MatchString(r) {
			add("", fmt.Sprintf("extra_roles[%d]", i), "%q is not a valid role name (lowercase letters, digits, '.', '_', '-')", r)
			continue
		}
		roles = append(roles, r)
	}
	providers := slices.Clone(KnownProviders)
	for i, p := range f.ExtraProviders {
		if !identRE.MatchString(p) {
			add("", fmt.Sprintf("extra_providers[%d]", i), "%q is not a valid provider id", p)
			continue
		}
		providers = append(providers, p)
	}

	seen := map[string]int{}
	for i, h := range f.Hosts {
		name := h.Name
		if name == "" {
			add("", fmt.Sprintf("hosts[%d].name", i), "missing")
		} else if !nameRE.MatchString(name) {
			add(name, "name", "must be lowercase letters, digits, '.', '_' or '-', starting with a letter or digit, at most 63 characters")
		} else if first, dup := seen[name]; dup {
			add(name, "name", "duplicate: also hosts[%d]", first)
		} else {
			seen[name] = i
		}

		if h.Hostname != "" && (len(h.Hostname) > 253 || !hostnameRE.MatchString(h.Hostname)) {
			add(name, "hostname", "%q is not a valid hostname", h.Hostname)
		}
		if h.TailnetName != "" && (len(h.TailnetName) > 253 || !hostnameRE.MatchString(h.TailnetName)) {
			add(name, "tailnet_name", "%q is not a valid name", h.TailnetName)
		}
		if h.TailnetIP != "" {
			ip, err := netip.ParseAddr(h.TailnetIP)
			switch {
			case err != nil:
				add(name, "tailnet_ip", "%q is not an IP address", h.TailnetIP)
			case !tailnetV4.Contains(ip) && !tailnetV6.Contains(ip):
				add(name, "tailnet_ip", "%s is outside the tailnet range 100.64.0.0/10 (or fd7a:115c:a1e0::/48)", ip)
			}
		}
		if h.LanIP != "" {
			if _, err := netip.ParseAddr(h.LanIP); err != nil {
				add(name, "lan_ip", "%q is not an IP address", h.LanIP)
			}
		}
		if h.Role != "" && !slices.Contains(roles, h.Role) {
			add(name, "role", "unknown role %q; known: %s (extend with extra_roles)", h.Role, strings.Join(roles, ", "))
		}
		if h.Provider != "" && !slices.Contains(providers, h.Provider) {
			add(name, "provider", "unknown provider %q; known: %s (extend with extra_providers)", h.Provider, strings.Join(providers, ", "))
		}
		validateLabels(h.Labels, func(field, msg string) { add(name, field, "%s", msg) })

		if h.TTL != "" {
			if d, err := time.ParseDuration(h.TTL); err != nil || d <= 0 {
				add(name, "ttl", "%q is not a positive duration such as 72h", h.TTL)
			}
		}
		if h.Cost != nil && (h.Cost.Monthly < 0) {
			add(name, "cost.monthly", "must not be negative")
		}
		if h.Declared.CPU < 0 || h.Declared.Memory < 0 || h.Declared.Disk < 0 {
			add(name, "declared", "cpu, memory and disk must not be negative")
		}
		if strings.ContainsAny(h.Reach.SSH, " \t\r\n") {
			add(name, "reach.ssh", "must be a resource id")
		}
		if h.TetherEnvironmentID != "" && len(h.TetherEnvironmentID) > 200 {
			add(name, "tether_environment_id", "too long")
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return &ValidationError{Problems: probs}
}

func validateLabels(labels map[string]string, add func(field, msg string)) {
	if len(labels) > maxLabels {
		add("labels", fmt.Sprintf("at most %d labels", maxLabels))
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := labels[k]
		if !labelKeyRE.MatchString(k) {
			add("labels."+k, "key must be lowercase letters, digits, '.', '_', '-' or '/', at most 63 characters")
		}
		if len(v) > maxLabelValue {
			add("labels."+k, fmt.Sprintf("value longer than %d characters", maxLabelValue))
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f {
				add("labels."+k, "value must not contain control characters")
				break
			}
		}
	}
}
