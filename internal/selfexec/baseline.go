package selfexec

import "sync"

// baselineRegistry holds first-seen fingerprints keyed by exe path. Used
// only by CheckAndExitIfStale (the goroutine-free one-shot variant) so
// repeated calls have something to compare against.
var (
	baselineMu sync.RWMutex
	baselines  = map[string]Fingerprint{}
)

func loadBaseline(path string) (Fingerprint, bool) {
	baselineMu.RLock()
	defer baselineMu.RUnlock()
	fp, ok := baselines[path]
	return fp, ok
}

func storeBaseline(fp Fingerprint) {
	baselineMu.Lock()
	defer baselineMu.Unlock()
	baselines[fp.Path] = fp
}

// resetBaselines clears the registry. Test-only helper.
func resetBaselines() {
	baselineMu.Lock()
	defer baselineMu.Unlock()
	baselines = map[string]Fingerprint{}
}
