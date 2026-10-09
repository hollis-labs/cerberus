package pluginhost

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// A split plugin's preview of a write runs in its read instance, which holds
// only the read-bound credentials: a preview can't write, however the plugin
// behaves (M8).
func TestAPreviewRunsInTheReadInstance(t *testing.T) {
	m := newTestManager(t, nil, fakeLauncher{}, "test")
	lp := &loadedPlugin{access: secrets.AccessRead, writer: &writerState{}}
	for _, effect := range []contract.Effect{contract.EffectWrite, contract.EffectDestructive, contract.EffectExec} {
		got, err := m.instanceFor(context.Background(), "p", lp, effect, true)
		if err != nil || got != lp {
			t.Fatalf("a %s preview ran in %p (%v), not the read instance %p", effect, got, err, lp)
		}
	}
}
