package connector

import "testing"

func TestHintsFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   Operation
		want ToolHints
	}{
		{"read of a provider", Operation{Effect: EffectRead, LocalFS: LocalFSNone, Target: TargetDescriptor{Kind: "digitalocean.droplet"}},
			ToolHints{ReadOnly: true, Idempotent: true, OpenWorld: true}},
		{"read of Cerberus itself", Operation{Effect: EffectRead, LocalFS: LocalFSNone, Target: TargetDescriptor{Kind: "local.resource"}},
			ToolHints{ReadOnly: true, Idempotent: true}},
		{"a read that writes a local file", Operation{Effect: EffectReadSensitive, LocalFS: LocalFSWrites, Target: TargetDescriptor{Kind: "ssh.host"}},
			ToolHints{Destructive: true, OpenWorld: true}},
		{"a reversible lifecycle change", Operation{Effect: EffectLifecycle, Reversible: true, Target: TargetDescriptor{Kind: "local.resource"}},
			ToolHints{Destructive: true}},
		{"exec", Operation{Effect: EffectExec, Target: TargetDescriptor{Kind: "pipeline"}},
			ToolHints{Destructive: true}},
		{"a gap", Operation{Target: TargetDescriptor{Kind: "kubernetes.workload"}},
			ToolHints{Destructive: true, OpenWorld: true}},
	} {
		if got := HintsFor(tc.op); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
