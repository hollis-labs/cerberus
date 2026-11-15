package supervise

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// CgroupPath is where the process's own cgroup is read on Linux.
const CgroupPath = "/proc/self/cgroup"

// UnitFromCgroup returns the systemd unit a process runs in, from its /proc/<pid>/cgroup
// content (the cgroup v2 line, "0::/user.slice/.../app.slice/<unit>.service"), or "" when
// it is not in a service unit (a shell, a scope, a test).
func UnitFromCgroup(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		// cgroup v2 is hierarchy 0 with no controllers; v1's name=systemd line carries the
		// same path.
		if parts[0] != "0" && parts[1] != "name=systemd" {
			continue
		}
		for p := parts[2]; p != "/" && p != "." && p != ""; p = path.Dir(p) {
			if base := path.Base(p); strings.HasSuffix(base, ".service") {
				return base, nil
			}
		}
		return "", nil
	}
	return "", sc.Err()
}

// SelfCheck is what the start learned about the process's own identity.
type SelfCheck struct {
	// Unit is the systemd unit the process runs in; empty outside one.
	Unit string
	// Expected is the unit self_resource_id's resource installs as.
	Expected string
}

// OutsideUnit decides a start outside any service unit with self_resource_id set. Observe mode
// starts, with a warning (nil here; the caller logs). Supervise mode refuses: a hand-run
// process beside the unit-run one would be a second writer, running its own monitor and
// serving mutations (plan section 4.2), and the database lock stops it only when both use the
// same data directory.
func (c SelfCheck) OutsideUnit(mode settings.Mode, selfID string) error {
	if selfID == "" || c.Unit != "" || mode != settings.ModeSupervise {
		return nil
	}
	return redact.Guidance("cerberus.mode supervise refuses to start outside its systemd unit: cerberus.self_resource_id is %q, which installs as %s, "+
		"and this process runs in no service unit. Start it through that unit, or run this copy with cerberus.mode: observe", selfID, c.Expected)
}

// CheckSelf compares the unit the process runs in (cgroup, read from the CgroupPath
// content) with the unit self_resource_id installs as. It refuses a start in a different
// unit, and a self_resource_id the registry does not list. A process outside any service
// unit (run from a shell) passes; the caller says so in the log. An empty selfID checks
// nothing.
func CheckSelf(cgroup io.Reader, selfID string, resources []config.ResourceDef, home string) (SelfCheck, error) {
	if selfID == "" {
		return SelfCheck{}, nil
	}
	var res *config.ResourceDef
	for i := range resources {
		if resources[i].ID == selfID {
			res = &resources[i]
		}
	}
	if res == nil {
		return SelfCheck{}, redact.Guidance("cerberus.self_resource_id is %q, but the registry lists no such resource; register its descriptor or correct the setting", selfID)
	}
	spec, err := localconn.SpecFromResourceConfig(res.Config)
	if err != nil {
		return SelfCheck{}, fmt.Errorf("cerberus.self_resource_id %q: %w", selfID, err)
	}
	layout, err := localconn.DefaultInstallLayout(home, ResourceToDomain(res), spec)
	if err != nil {
		return SelfCheck{}, fmt.Errorf("cerberus.self_resource_id %q: %w", selfID, err)
	}
	check := SelfCheck{Expected: layout.UnitName}
	if cgroup == nil {
		return check, nil
	}
	if check.Unit, err = UnitFromCgroup(cgroup); err != nil {
		return check, fmt.Errorf("read the process's cgroup: %w", err)
	}
	if check.Unit != "" && check.Unit != check.Expected {
		return check, redact.Guidance("this process runs in the systemd unit %s, but cerberus.self_resource_id %q installs as %s; a supervisor must know which unit is itself, so fix the setting or the unit",
			check.Unit, selfID, check.Expected)
	}
	return check, nil
}

// OpenCgroup opens CgroupPath; on a system without it, it returns nil and no error.
func OpenCgroup() (io.ReadCloser, error) {
	f, err := os.Open(CgroupPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f, err
}
