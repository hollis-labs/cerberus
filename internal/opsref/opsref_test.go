package opsref

import "testing"

func TestResolvePrecedencePerID(t *testing.T) {
	m := Mapping{
		Resources: map[string]Entry{"web": {OpsID: "res-ops"}},
		Projects:  map[string]Entry{"proj": {OpsID: "proj-ops", WorkstreamID: "workstream:PROJ"}},
	}
	env := Refs{OpsID: "env-ops", WorkstreamID: "workstream:ENV", Task: "CW-ENV"}
	flags := Refs{OpsID: "flag-ops"}

	cases := []struct {
		name       string
		flags, env Refs
		resource   string
		want       Refs
	}{
		{"flag beats env, mapping", flags, env, "web", Refs{"flag-ops", "workstream:ENV", "CW-ENV"}},
		{"env beats mapping", Refs{}, env, "web", Refs{"env-ops", "workstream:ENV", "CW-ENV"}},
		{"resource mapping beats project mapping; each id separately", Refs{}, Refs{}, "web", Refs{"res-ops", "workstream:PROJ", ""}},
		{"project mapping when the resource has none", Refs{}, Refs{}, "other", Refs{"proj-ops", "workstream:PROJ", ""}},
		{"none", Refs{}, Refs{}, "other-no-proj", Refs{"", "", ""}},
	}
	for _, c := range cases {
		project := "proj"
		if c.resource == "other-no-proj" {
			project = ""
		}
		if got := Resolve(c.flags, c.env, m, c.resource, project); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestFromEnvAndTrim(t *testing.T) {
	env := map[string]string{EnvOpsID: " ops ", EnvWorkstream: "workstream:X", EnvTask: "CW-1"}
	got := FromEnv(func(k string) string { return env[k] })
	if got != (Refs{"ops", "workstream:X", "CW-1"}) {
		t.Fatalf("got %+v", got)
	}
	if !(Refs{}).Empty() || got.Empty() {
		t.Fatal("Empty is wrong")
	}
}

func TestSettingsDefaults(t *testing.T) {
	s := Settings{}.WithDefaults()
	if s.Enabled || s.Endpoint != "http://127.0.0.1:8994/mcp" || s.Tool != "status_post" ||
		s.TokenFile != "/var/lib/fabric/tether/run/operator.token" || s.Actor != "msg://agent/agent-mux/agt_azos2rzing" ||
		s.TimeoutSeconds <= 0 || s.Retries == nil || *s.Retries != 2 {
		t.Fatalf("defaults: %+v", s)
	}
	zero := 0
	if got := (Settings{Retries: &zero}).WithDefaults(); *got.Retries != 0 {
		t.Fatal("an explicit retries: 0 must survive")
	}
}
