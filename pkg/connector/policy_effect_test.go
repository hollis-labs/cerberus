package connector

import "testing"

// A read that touches local files is authorized as a write (H2); nothing
// else changes.
func TestPolicyEffect(t *testing.T) {
	for _, c := range []struct {
		effect Effect
		fs     LocalFS
		want   Effect
	}{
		{EffectReadSensitive, LocalFSWrites, EffectWrite},
		{EffectRead, LocalFSReads, EffectWrite},
		{EffectRead, LocalFSNone, EffectRead},
		{EffectReadSensitive, "", EffectReadSensitive},
		{EffectDestructive, LocalFSWrites, EffectDestructive},
		{EffectExec, LocalFSReads, EffectExec},
	} {
		if got := (Operation{Effect: c.effect, LocalFS: c.fs}).PolicyEffect(); got != c.want {
			t.Errorf("%s with local_fs %q: %s, want %s", c.effect, c.fs, got, c.want)
		}
	}
}
