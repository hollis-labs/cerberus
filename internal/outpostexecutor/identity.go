package outpostexecutor

import (
	"context"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

// Preserve case and template instances: systemd treats both as identity.
func serviceName(name string) (string, error) {
	if err := (outpost.Request{Verb: "stop", Unit: name}).Validate(); err != nil {
		return "", err
	}
	if !strings.HasSuffix(name, ".service") {
		// Only suffix-less short names are accepted. A dotted name must explicitly
		// say .service; never reinterpret a target, socket or unknown unit type.
		if strings.Contains(name, ".") {
			return "", fmt.Errorf("native actions require a service unit")
		}
		name += ".service"
	}
	stem := strings.TrimSuffix(name, ".service")
	if stem == "" || len(name) > 255 || strings.Count(stem, "@") > 1 || strings.HasSuffix(stem, "@") {
		return "", fmt.Errorf("native actions require a concrete service identity")
	}
	return name, nil
}

func protectedNames(platform string, extra []string) ([]string, error) {
	names := []string{"outpost.service", "tether-outpost.service", "com.hollis-labs.cerberus.outpost.outpost.service", "dev.hollis-labs.outpost", "com.hollis-labs.cerberus", "com.hollis-labs.cerberus.service", "com.fragments-engine.cerberus", "com.fragments-engine.cerberus.service"}
	if platform == "linux" {
		names = []string{"outpost.service", "tether-outpost.service", "com.hollis-labs.cerberus.outpost.outpost.service", "com.hollis-labs.cerberus.service", "com.fragments-engine.cerberus.service"}
	}
	for _, raw := range extra {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("empty protected unit")
		}
		var err error
		if platform == "linux" {
			name, err = serviceName(name)
		} else {
			err = (outpost.Request{Verb: "stop", Unit: name}).Validate()
		}
		if err != nil {
			return nil, fmt.Errorf("invalid protected unit: %w", err)
		}
		names = append(names, name)
	}
	return names, nil
}

func controlPlaneError() error { return fmt.Errorf("cannot mutate the request's control plane") }

// These service/target units can exit the user manager or change machine power
// state without naming an ordinary service. Dependencies cannot reach them either.
func managerUnit(name string) bool {
	switch name {
	case "exit.target", "shutdown.target", "default.target", "halt.target", "poweroff.target", "reboot.target", "kexec.target", "soft-reboot.target", "sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target", "suspend-then-hibernate.target", "systemd-exit.service", "systemd-halt.service", "systemd-poweroff.service", "systemd-reboot.service", "systemd-kexec.service", "systemd-soft-reboot.service", "systemd-suspend.service", "systemd-hibernate.service", "systemd-hybrid-sleep.service", "systemd-suspend-then-hibernate.service":
		return true
	}
	return false
}

// Query supervisor identities, not filesystem aliases. Names includes aliases
// and Id is the canonical dispatch identity. Missing/truncated metadata refuses.
const unitProperties = "Id,Names,LoadState,ActiveState,StopWhenUnneeded,Requires,Wants,Requisite,BindsTo,Upholds,Conflicts,ConflictedBy,RequiredBy,RequisiteOf,BoundBy,ConsistsOf,PropagatesStopTo,OnFailure,OnSuccess,FailureAction,SuccessAction,StartLimitAction,JobTimeoutAction,OnFailureJobMode,OnSuccessJobMode"

type systemdUnit map[string]string

func (p *Plugin) inspectUnit(ctx context.Context, name string) (systemdUnit, error) {
	if err := (outpost.Request{Verb: "stop", Unit: name}).Validate(); err != nil {
		return nil, err
	}
	result, err := p.Run(ctx, "systemctl", []string{"--user", "show", "--all", "--property=" + unitProperties, "--", name})
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return nil, fmt.Errorf("cannot inspect service identity")
	}
	props := systemdUnit{}
	for _, line := range strings.Split(strings.TrimSpace(result.Output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid supervisor metadata")
		}
		if _, duplicate := props[key]; duplicate {
			return nil, fmt.Errorf("ambiguous supervisor metadata")
		}
		props[key] = value
	}
	if props["Id"] == "" || props["LoadState"] == "" {
		return nil, fmt.Errorf("missing supervisor identity")
	}
	if props["LoadState"] == "not-found" {
		return props, nil
	}
	if props["LoadState"] != "loaded" || props["Names"] == "" {
		return nil, fmt.Errorf("service identity is not loaded")
	}
	// Require the core properties even on older systemd. Newer dependency kinds
	// (Upholds, OnSuccess, PropagatesStopTo) may be absent on older supervisors.
	for _, key := range []string{"ActiveState", "StopWhenUnneeded", "Requires", "Wants", "Requisite", "BindsTo", "Conflicts", "ConflictedBy", "RequiredBy", "RequisiteOf", "BoundBy", "ConsistsOf", "OnFailure", "FailureAction", "SuccessAction", "StartLimitAction", "JobTimeoutAction", "OnFailureJobMode"} {
		if _, ok := props[key]; !ok {
			return nil, fmt.Errorf("incomplete supervisor metadata: %s", key)
		}
	}
	return props, nil
}

func unitNames(u systemdUnit) []string { return append(strings.Fields(u["Names"]), u["Id"]) }

// guardSystemd follows possible lifecycle effects conservatively. It is not a
// policy engine: it only refuses operations that can remove the serving control
// plane, and refuses when that safety check cannot be completed. Unit definitions
// remain operator-trusted; arbitrary Exec* programs are not sandboxed here.
func (p *Plugin) guardSystemd(ctx context.Context, verb, name string, protected []string) (string, error) {
	serving := p.servingUnits
	if serving == nil {
		serving = servingSystemdUnits
	}
	own, err := serving()
	if err != nil {
		return "", err
	}
	if len(own) == 0 && len(p.Protected) == 0 {
		return "", fmt.Errorf("cannot identify serving unit; configure protected_units")
	}
	protected = append(append([]string(nil), protected...), own...)
	blocked := map[string]bool{}
	cache := map[string]systemdUnit{}
	inspect := func(name string) (systemdUnit, error) {
		if u, ok := cache[name]; ok {
			return u, nil
		}
		u, err := p.inspectUnit(ctx, name)
		if err == nil {
			for _, alias := range unitNames(u) {
				cache[alias] = u
			}
		}
		return u, err
	}
	servingVerified := false
	for _, name := range protected {
		canonical, err := serviceName(name)
		if err != nil {
			return "", err
		}
		blocked[canonical] = true
		u, err := inspect(canonical)
		if err != nil {
			return "", err
		}
		for _, alias := range unitNames(u) {
			blocked[alias] = true
		}
		if u["LoadState"] == "loaded" {
			for _, servingName := range own {
				if servingName == canonical {
					servingVerified = true
				}
			}
			for _, configured := range p.Protected {
				normalized, err := serviceName(strings.TrimSpace(configured))
				if err != nil {
					return "", err
				}
				if normalized == canonical {
					servingVerified = true
				}
			}
		}
	}
	if !servingVerified {
		return "", fmt.Errorf("serving unit is not loaded; configure protected_units")
	}
	type effect struct{ name, verb string }
	queue := []effect{{name, verb}}
	visited := map[effect]bool{}
	canonical := ""
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		e := queue[0]
		queue = queue[1:]
		if visited[e] {
			continue
		}
		visited[e] = true
		if len(visited) > 128 {
			return "", fmt.Errorf("service dependency inspection exceeds limit")
		}
		u, err := inspect(e.name)
		if err != nil {
			return "", err
		}
		if u["LoadState"] != "loaded" {
			return "", fmt.Errorf("service dependency is not loaded")
		}
		// Standard service dependencies conflict with shutdown.target. Stopping
		// an already inactive dependency has no lifecycle effect; do not treat
		// that ordinary edge as starting the manager's shutdown transaction.
		if canonical != "" && e.verb == "stop" && u["ActiveState"] == "inactive" {
			continue
		}
		for _, alias := range unitNames(u) {
			if blocked[alias] || managerUnit(alias) {
				return "", controlPlaneError()
			}
		}
		if canonical == "" {
			canonical, err = serviceName(u["Id"])
			if err != nil {
				return "", err
			}
		}
		for _, key := range []string{"FailureAction", "SuccessAction", "StartLimitAction", "JobTimeoutAction"} {
			if u[key] != "none" {
				return "", fmt.Errorf("service can invoke a manager action")
			}
		}
		for _, key := range []string{"OnFailureJobMode", "OnSuccessJobMode"} {
			if mode := u[key]; mode != "" && mode != "replace" && mode != "fail" {
				return "", fmt.Errorf("service can replace manager jobs")
			}
		}
		add := func(action string, keys ...string) {
			for _, key := range keys {
				for _, unit := range strings.Fields(u[key]) {
					queue = append(queue, effect{unit, action})
				}
			}
		}
		// Even stopping a service can activate OnSuccess/OnFailure handlers.
		add("start", "OnFailure", "OnSuccess")
		if e.verb != "stop" {
			add("start", "Requires", "Wants", "Requisite", "BindsTo", "Upholds")
			add("stop", "Conflicts", "ConflictedBy")
		}
		if e.verb != "start" {
			add(e.verb, "RequiredBy", "RequisiteOf", "BoundBy", "ConsistsOf", "PropagatesStopTo")
			// Losing a consumer can stop dependencies marked StopWhenUnneeded.
			for _, key := range []string{"Requires", "Wants", "Requisite", "BindsTo", "Upholds"} {
				for _, dependency := range strings.Fields(u[key]) {
					dep, err := inspect(dependency)
					if err != nil {
						return "", err
					}
					if dep["LoadState"] != "loaded" {
						return "", fmt.Errorf("service dependency is not loaded")
					}
					if dep["StopWhenUnneeded"] != "no" {
						queue = append(queue, effect{dependency, "stop"})
					}
				}
			}
		}
	}
	return canonical, nil
}
