package outpostexecutor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/pkg/outpost"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

func unitFixture(id string) systemdUnit {
	u := systemdUnit{}
	for _, key := range strings.Split(unitProperties, ",") {
		u[key] = ""
	}
	u["Id"], u["Names"], u["LoadState"] = id, id, "loaded"
	u["FailureAction"], u["SuccessAction"], u["StartLimitAction"], u["JobTimeoutAction"] = "none", "none", "none", "none"
	u["ActiveState"], u["StopWhenUnneeded"] = "active", "no"
	u["OnFailureJobMode"], u["OnSuccessJobMode"] = "replace", "fail"
	return u
}

func fakeSupervisor(t *testing.T, units map[string]systemdUnit) (*Plugin, *[]string) {
	t.Helper()
	mutations := []string{}
	if _, ok := units["actual-outpost.service"]; !ok {
		units["actual-outpost.service"] = unitFixture("actual-outpost.service")
	}
	p := &Plugin{servingUnits: func() ([]string, error) { return []string{"actual-outpost.service"}, nil }}
	p.Run = func(_ context.Context, command string, args []string) (outpost.Result, error) {
		if command != "systemctl" || len(args) < 3 || args[0] != "--user" {
			t.Fatalf("unexpected native command: %s %q", command, args)
		}
		name := args[len(args)-1]
		if args[1] != "show" {
			mutations = append(mutations, args[1]+" "+name)
			return outpost.Result{}, nil
		}
		u, ok := units[name]
		if !ok {
			u = systemdUnit{"Id": name, "LoadState": "not-found"}
		}
		var output strings.Builder
		for _, key := range strings.Split(unitProperties, ",") {
			if value, ok := u[key]; ok {
				fmt.Fprintf(&output, "%s=%s\n", key, value)
			}
		}
		return outpost.Result{Output: output.String()}, nil
	}
	return p, &mutations
}

func TestServiceTypesRefusedBeforeRunner(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	for _, verb := range []string{"start", "stop", "restart"} {
		for _, unit := range []string{"exit.target", "shutdown.target", "default.target", "dbus.socket", "example.timer", "example.path", "example.mount", "example.scope", "example.slice", "example.unknown", "example@.service", "example@@one.service", "systemd-exit.service"} {
			t.Run(verb+"/"+unit, func(t *testing.T) {
				p := &Plugin{Run: func(context.Context, string, []string) (outpost.Result, error) {
					t.Fatal("invalid service reached runner")
					return outpost.Result{}, nil
				}}
				if _, err := p.Execute(context.Background(), outpost.Request{Verb: verb, Unit: unit}); err == nil {
					t.Fatal("accepted non-service/manager operation")
				}
			})
		}
	}
}

func TestProtectedSupervisorIdentities(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	for _, verb := range []string{"start", "stop", "restart"} {
		for _, tc := range []struct{ name, config, request string }{
			{"short-config", "custom-outpost", "custom-outpost.service"},
			{"canonical-config", "custom-outpost.service", "custom-outpost"},
			{"comma-space", "spare.service, custom-outpost.service ", "custom-outpost.service"},
			{"request-alias", "custom-outpost", "alias.service"},
			{"config-alias", "alias.service", "custom-outpost"},
			{"serving-unit", "", "actual-outpost"},
			{"serving-alias", "", "serving-alias.service"},
			{"installed-outpost", "", "com.hollis-labs.cerberus.outpost.outpost.service"},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				custom := unitFixture("custom-outpost.service")
				custom["Names"] += " alias.service"
				actual := unitFixture("actual-outpost.service")
				actual["Names"] += " serving-alias.service"
				p, mutations := fakeSupervisor(t, map[string]systemdUnit{"custom-outpost.service": custom, "alias.service": custom, "actual-outpost.service": actual, "serving-alias.service": actual})
				if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{"protected_units": tc.config}}); err != nil {
					t.Fatal(err)
				}
				if _, err := p.Execute(context.Background(), outpost.Request{Verb: verb, Unit: tc.request}); err == nil || !strings.Contains(err.Error(), "control plane") {
					t.Fatalf("expected identity refusal, got %v", err)
				}
				if len(*mutations) != 0 {
					t.Fatalf("mutated protected identity: %v", *mutations)
				}
			})
		}
	}
}

func TestProtectedConfigValidation(t *testing.T) {
	for _, config := range []string{"x,", "x,,y", " x y ", "../x", "--all"} {
		if _, err := (&Plugin{}).Init(context.Background(), subprocess.InitParams{Config: map[string]string{"protected_units": config}}); err == nil {
			t.Errorf("accepted %q", config)
		}
	}
}

func TestServiceIdentityPreservesCaseAndInstances(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	for _, unit := range []string{"Example.service", "worker@two.service", "ordinary"} {
		canonical, err := serviceName(unit)
		if err != nil {
			t.Fatal(err)
		}
		p, mutations := fakeSupervisor(t, map[string]systemdUnit{canonical: unitFixture(canonical)})
		p.Protected = []string{"example", "worker@one.service"}
		if _, err := p.Execute(context.Background(), outpost.Request{Verb: "restart", Unit: unit}); err != nil {
			t.Fatal(err)
		}
		if len(*mutations) != 1 || (*mutations)[0] != "restart "+canonical {
			t.Fatalf("identity changed: %v", *mutations)
		}
	}
}

func TestSystemdIndirectControlPlaneEffects(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	for _, tc := range []struct{ verb, key, value string }{
		{"start", "Conflicts", "actual-outpost.service"},
		{"start", "ConflictedBy", "actual-outpost.service"},
		{"start", "JobTimeoutAction", "exit"},
		{"restart", "Conflicts", "alias.service"},
		{"stop", "ConsistsOf", "actual-outpost.service"},
		{"stop", "BoundBy", "actual-outpost.service"},
		{"stop", "RequiredBy", "actual-outpost.service"},
		{"stop", "PropagatesStopTo", "actual-outpost.service"},
		{"start", "Wants", "exit.target"},
		{"restart", "OnFailure", "systemd-exit.service"},
		{"stop", "OnSuccess", "systemd-exit.service"},
		{"start", "FailureAction", "exit"},
		{"start", "SuccessAction", "exit-force"},
		{"restart", "StartLimitAction", "reboot"},
		{"start", "OnFailureJobMode", "isolate"},
	} {
		t.Run(tc.verb+"/"+tc.key, func(t *testing.T) {
			u := unitFixture("ordinary.service")
			u[tc.key] = tc.value
			actual := unitFixture("actual-outpost.service")
			actual["Names"] += " alias.service"
			p, mutations := fakeSupervisor(t, map[string]systemdUnit{"ordinary.service": u, "actual-outpost.service": actual, "alias.service": actual})
			if _, err := p.Execute(context.Background(), outpost.Request{Verb: tc.verb, Unit: "ordinary"}); err == nil {
				t.Fatal("accepted indirect effect")
			}
			if len(*mutations) != 0 {
				t.Fatalf("indirect mutation: %v", *mutations)
			}
		})
	}
	// A transitive conflict must also refuse, with a cycle terminating safely.
	first := unitFixture("first.service")
	first["Wants"] = "second.service"
	second := unitFixture("second.service")
	second["Wants"] = "first.service"
	second["Conflicts"] = "actual-outpost.service"
	p, mutations := fakeSupervisor(t, map[string]systemdUnit{"first.service": first, "second.service": second})
	if _, err := p.Execute(context.Background(), outpost.Request{Verb: "start", Unit: "first"}); err == nil || len(*mutations) != 0 {
		t.Fatalf("transitive effect passed: %v %v", err, *mutations)
	}
}

func TestSystemdMetadataFailureClosesGate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	for _, result := range []outpost.Result{{}, {ExitCode: 1}, {Truncated: true}, {Output: "Id=x.service\nLoadState=loaded\nNames=x.service\n"}} {
		p := &Plugin{servingUnits: func() ([]string, error) { return []string{"actual.service"}, nil }, Run: func(_ context.Context, _ string, args []string) (outpost.Result, error) {
			if args[1] != "show" {
				t.Fatal("failed inspection dispatched mutation")
			}
			return result, nil
		}}
		if _, err := p.Execute(context.Background(), outpost.Request{Verb: "restart", Unit: "ordinary"}); err == nil {
			t.Fatal("accepted incomplete inspection")
		}
	}
}

func TestLaunchdServingIdentityAndPlistAlias(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	agents := filepath.Join(dir, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{Run: func(_ context.Context, command string, args []string) (outpost.Result, error) {
		if command != "launchctl" || strings.Join(args, " ") != "list" {
			t.Fatal("unexpected mutation")
		}
		return outpost.Result{Output: fmt.Sprintf("%d 0 actual-outpost\n", os.Getppid())}, nil
	}}
	for _, verb := range []string{"start", "stop", "restart"} {
		if err := p.guardLaunchd(context.Background(), outpost.Request{Verb: verb, Unit: "actual-outpost"}); err == nil {
			t.Fatalf("accepted %s on serving job", verb)
		}
	}
	path := filepath.Join(agents, "alias.plist")
	for _, label := range []string{"dev.hollis-labs.outpost", "alias"} {
		if err := os.WriteFile(path, []byte(`<plist><dict><key>Label</key><string>`+label+`</string></dict></plist>`), 0600); err != nil {
			t.Fatal(err)
		}
		err := p.guardLaunchd(context.Background(), outpost.Request{Verb: "start", Unit: "alias"})
		if (err == nil) != (label == "alias") {
			t.Fatalf("label %s: %v", label, err)
		}
	}
	names, err := protectedNames("darwin", []string{" custom.outpost "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names, ","), "dev.hollis-labs.outpost") || names[len(names)-1] != "custom.outpost" {
		t.Fatalf("launchd protections %v", names)
	}
}

func TestSystemdOrdinaryDependenciesAndGarbageCollection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	// DefaultDependencies creates a conflict with the normally inactive shutdown
	// target. Ordinary services with these edges must still be actionable.
	for _, verb := range []string{"start", "stop", "restart"} {
		first := unitFixture("ordinary.service")
		first["Requires"] = "basic.target"
		first["Conflicts"] = "shutdown.target"
		basic := unitFixture("basic.target")
		shutdown := unitFixture("shutdown.target")
		shutdown["ActiveState"] = "inactive"
		p, mutations := fakeSupervisor(t, map[string]systemdUnit{"ordinary.service": first, "basic.target": basic, "shutdown.target": shutdown})
		if _, err := p.Execute(context.Background(), outpost.Request{Verb: verb, Unit: "ordinary"}); err != nil {
			t.Fatalf("ordinary %s: %v", verb, err)
		}
		if len(*mutations) != 1 {
			t.Fatalf("did not execute %s: %v", verb, *mutations)
		}
	}
	first := unitFixture("consumer.service")
	first["Wants"] = "actual-outpost.service"
	actual := unitFixture("actual-outpost.service")
	actual["StopWhenUnneeded"] = "yes"
	p, mutations := fakeSupervisor(t, map[string]systemdUnit{"consumer.service": first, "actual-outpost.service": actual})
	if _, err := p.Execute(context.Background(), outpost.Request{Verb: "stop", Unit: "consumer"}); err == nil || len(*mutations) != 0 {
		t.Fatalf("unneeded control plane could stop: %v %v", err, *mutations)
	}
}
