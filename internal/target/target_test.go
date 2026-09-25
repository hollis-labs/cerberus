package target

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAdminParsesBothForms(t *testing.T) {
	var doc struct {
		A Admin `yaml:"a"`
		B Admin `yaml:"b"`
		C Admin `yaml:"c"`
	}
	src := "a: owner\nb: { default: owner, docker: self, software: self }\nc: [nonsense]\n"
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("a malformed admin failed the whole load: %v", err)
	}
	if doc.A.Default != AdminOwner || len(doc.A.ByKind) != 0 {
		t.Fatalf("scalar: %+v", doc.A)
	}
	if doc.B.Default != AdminOwner || doc.B.ByKind["docker"] != AdminSelf || doc.B.String() != "default=owner,docker=self,software=self" {
		t.Fatalf("map: %+v %q", doc.B, doc.B.String())
	}
	if problems := (Labels{Admin: doc.C}).Validate(); len(problems) != 1 {
		t.Fatalf("malformed admin not reported: %v", problems)
	}
	out, err := yaml.Marshal(struct {
		A Admin `yaml:"a"`
		B Admin `yaml:"b,omitempty"`
	}{A: doc.A, B: doc.B})
	if err != nil || !strings.Contains(string(out), "a: owner") || !strings.Contains(string(out), "docker: self") {
		t.Fatalf("marshal: %s %v", out, err)
	}
}

// Decision 18: admin is scopable per sub-target kind, matched on the
// target kind, then the connector, then the default.
func TestAdminForSubTargetKind(t *testing.T) {
	a := Admin{Default: AdminOwner, ByKind: map[string]string{"docker": AdminSelf, "kubernetes.workload": AdminShared}}
	for _, c := range []struct{ kind, connector, want string }{
		{"docker.container", "docker", AdminSelf},
		{"kubernetes.workload", "kubernetes", AdminShared},
		{"kubernetes.node", "kubernetes", AdminOwner},
		{"ssh.host", "ssh", AdminOwner},
	} {
		if got := a.For(c.kind, c.connector); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.connector, c.kind, got, c.want)
		}
	}
	if got := (Admin{}).For("x", "y"); got != AdminUnknown {
		t.Errorf("undeclared = %s", got)
	}
}

// Decision 17: an unlabeled target is unknown, and so is a value outside
// the vocabulary, never a guess.
func TestUnlabeledAndInvalidReadAsUnknown(t *testing.T) {
	got := Resolve("docker", "docker.container", "web", nil, false)
	if got.Env != EnvUnknown || got.Owner != OwnerUnknown || got.AdminFor != AdminUnknown || got.Resource != "" {
		t.Fatalf("no resource: %+v", got)
	}
	bad := Labels{Env: "production", Owner: " infra ", Admin: Admin{Default: "us", ByKind: map[string]string{"docker": "self", "k8s": "them"}}}
	if problems := bad.Validate(); len(problems) != 4 {
		t.Fatalf("problems %v", problems)
	}
	clean := bad.Sanitized()
	if clean.Env != "" || clean.Owner != "infra" || clean.Admin.Default != "" || clean.Admin.ByKind["docker"] != AdminSelf || clean.Admin.ByKind["k8s"] != "" {
		t.Fatalf("sanitized %+v", clean)
	}
	if r := clean.Resolved(); r.Env != EnvUnknown {
		t.Fatalf("resolved %+v", r)
	}
	if !(Labels{}).Unlabeled() || (Labels{Env: EnvDev}).Unlabeled() {
		t.Fatal("Unlabeled")
	}
}

// A sub-target inherits its resource's labels: a container on a host, a
// namespace in its cluster.
func TestSubTargetsInherit(t *testing.T) {
	host := &ResourceLabels{ID: "toolbox-stage", Labels: Labels{Env: EnvStaging, Owner: "aws-team",
		Admin: Admin{Default: AdminOwner, ByKind: map[string]string{"docker": AdminSelf}}, Tags: []string{"poc"}}}
	got := Resolve("docker", "docker.container", "api", host, false)
	if got.Resource != "toolbox-stage" || got.ID != "api" || got.Env != EnvStaging || got.Owner != "aws-team" || got.AdminFor != AdminSelf || got.Tags[0] != "poc" {
		t.Fatalf("inherited %+v", got)
	}
	if ssh := Resolve("ssh", "ssh.host", "", host, false); ssh.AdminFor != AdminOwner || ssh.ID != "toolbox-stage" {
		t.Fatalf("ssh %+v", ssh)
	}
	if adhoc := Resolve("docker", "docker.container", "api", nil, true); !adhoc.Adhoc || adhoc.Env != EnvUnknown {
		t.Fatalf("adhoc %+v", adhoc)
	}
}

func TestDefaultAdhocGrant(t *testing.T) {
	if !DefaultAdhocGrant("human") || DefaultAdhocGrant("agent") || DefaultAdhocGrant("automation") || DefaultAdhocGrant("") {
		t.Fatal("only a human holds adhoc_targets by default")
	}
}
