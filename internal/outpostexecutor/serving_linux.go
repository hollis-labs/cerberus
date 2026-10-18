//go:build linux

package outpostexecutor

import (
	"fmt"
	"os"
	"strings"
)

func servingSystemdUnits() ([]string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, fmt.Errorf("cannot identify serving control plane")
	}
	return systemdUnitsFromCgroup(string(b)), nil
}

func systemdUnitsFromCgroup(cgroup string) []string {
	units := []string{}
	for _, line := range strings.Split(cgroup, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || (parts[1] != "" && parts[1] != "name=systemd") {
			continue
		}
		// A plugin inherits the Outpost's cgroup, possibly in a delegated subgroup.
		// Protect all containing services, including the user manager itself.
		for _, part := range strings.Split(parts[2], "/") {
			if strings.HasSuffix(part, ".service") {
				units = append(units, part)
			}
		}
	}
	return units
}
