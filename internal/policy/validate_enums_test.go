package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every enum in a policy file is checked, so a typo fails validation rather
// than being read as the default, which for an approval channel or a match
// is weaker than what was written: a fail-open.
func TestPolicyEnumsAreValidated(t *testing.T) {
	approve := func(a Approval) File {
		return File{Version: FileVersion, Targets: []TargetBlock{{Rules: []Rule{{Decision: Approve, Approval: &a}}}}}
	}
	for name, tc := range map[string]struct {
		file File
		want string
	}{
		"a channel typo":          {approve(Approval{Channel: "out-of-band"}), `approval channel "out-of-band" is not tty_confirm or out_of_band`},
		"elicit, not yet":         {approve(Approval{Channel: "elicit"}), "approval channel elicit is not available yet"},
		"negative approvers":      {approve(Approval{Channel: "out_of_band", Approvers: -1}), "approvers -1 is negative"},
		"negative ttl":            {approve(Approval{TTL: -1}), "is negative"},
		"approval on a deny rule": {File{Version: FileVersion, Targets: []TargetBlock{{Rules: []Rule{{Decision: Deny, Approval: &Approval{Channel: "out_of_band"}}}}}}, "they apply only to approve"},
		"a target env typo":       {File{Version: FileVersion, Targets: []TargetBlock{{Match: TargetMatch{Env: "production"}, Rules: []Rule{{Decision: Deny}}}}}, `targets[0].match.env "production" is not an env`},
		"a negated env typo":      {File{Version: FileVersion, Targets: []TargetBlock{{Match: TargetMatch{Env: "!prd"}, Rules: []Rule{{Decision: Deny}}}}}, `"!prd" is not an env`},
		"a target admin typo":     {File{Version: FileVersion, Targets: []TargetBlock{{Match: TargetMatch{Admin: "shard"}, Rules: []Rule{{Decision: Deny}}}}}, `targets[0].match.admin "shard"`},
		"a posture rule env typo": {File{Version: FileVersion, PostureRules: []PostureRule{{Match: TargetMatch{Env: "lab1"}, Posture: PosturePermissive}}}, `posture_rules[0].match.env "lab1"`},
	} {
		problems := strings.Join(tc.file.Validate(), "\n")
		if !strings.Contains(problems, tc.want) {
			t.Errorf("%s: want %q in:\n%s", name, tc.want, problems)
		}
	}

	ok := File{Version: FileVersion,
		PostureRules: []PostureRule{{Match: TargetMatch{Env: "dev", Admin: "self"}, Posture: PosturePermissive}},
		Targets: []TargetBlock{{Match: TargetMatch{Env: "!prod", Admin: "unknown"}, Rules: []Rule{
			{Decision: Approve, Approval: &Approval{Channel: "tty_confirm", Approvers: 2}},
			{Decision: Approve, Approval: &Approval{Channel: "out_of_band", Scope: ScopeWindow, TTL: 30}},
			{Decision: Allow},
		}}}}
	if problems := ok.Validate(); len(problems) != 0 {
		t.Fatalf("valid values refused: %v", problems)
	}
}

// A key the file format does not have is an error, not silently dropped: a
// dropped `ops` on an allow rule would make it allow every operation.
func TestPolicyFilesAreDecodedStrictly(t *testing.T) {
	for name, doc := range map[string]string{
		"a misspelled ops":      "version: 1\ntargets:\n  - match: { env: dev }\n    rules:\n      - { opps: [reload], decision: allow }\n",
		"a misspelled approval": "version: 1\ntargets:\n  - rules:\n      - { decision: approve, approvall: { channel: out_of_band } }\n",
		"an unknown top level":  "version: 1\nenforcment: { mode: enforce }\n",
	} {
		if _, err := decodeFile([]byte(doc)); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f, err := decodeFile(nil); err != nil || f.Version != 0 {
		t.Fatalf("an empty file: %+v %v", f, err)
	}
	// The applied snapshot is read strictly too: one it cannot read is the
	// baseline, which is the strict reading.
	s := Store{Dir: t.TempDir()}
	bad := []byte("version: 1\ntargets:\n  - rules:\n      - { opps: [reload], decision: allow }\n")
	if err := os.WriteFile(filepath.Join(s.Dir, appliedName), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, hashName), []byte(Hash(bad)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, status := s.Load(); status.Snapshot != SnapshotMismatch || !strings.Contains(status.Problem, "opps") {
		t.Fatalf("a snapshot with an unknown key loaded: %+v", status)
	}
}

// The examples in docs/policy.md parse strictly and validate, so the docs
// cannot drift into something the loader refuses.
func TestPolicyDocExamplesValidate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "policy.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(string(data), -1)
	if len(blocks) == 0 {
		t.Fatal("no yaml examples found")
	}
	for i, b := range blocks {
		text := dedent(b[1])
		f, err := decodeFile([]byte(text))
		if err != nil {
			t.Errorf("example %d: %v\n%s", i+1, err, text)
			continue
		}
		f.Version = FileVersion // fragments leave it out
		if problems := f.Validate(); len(problems) != 0 {
			t.Errorf("example %d: %v", i+1, problems)
		}
	}
}

func dedent(s string) string {
	lines := strings.Split(s, "\n")
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " ")); indent < 0 || n < indent {
			indent = n
		}
	}
	for i, l := range lines {
		if len(l) >= indent && indent > 0 {
			lines[i] = l[indent:]
		}
	}
	return strings.Join(lines, "\n")
}
