package audit

import "testing"

// A problem's key is its kind and position, not its wording: a cut reads two ways as records
// are written after it, and is one key; two breaks at different seqs are two keys.
func TestProblemKeyIsKindAndPosition(t *testing.T) {
	for problem, want := range map[string]string{
		"anchored seq 13 is past the end of 2026-11.jsonl: the log was cut":                          "anchor_cut 2026-11.jsonl 13",
		"anchored seq 13 is not where it was written in 2026-11.jsonl: the log was cut or rewritten": "anchor_cut 2026-11.jsonl 13",
		"2026-11.jsonl, which holds anchored seq 13, is missing":                                     "anchor_file_missing 2026-11.jsonl 13",
		"2026-11.jsonl: seq 4 hash does not match its content":                                       "hash_mismatch 2026-11.jsonl 4",
		"2026-11.jsonl: seq 6 hash does not match its content":                                       "hash_mismatch 2026-11.jsonl 6",
		"2026-11.jsonl: seq 7 follows 5":                                                             "seq_gap 2026-11.jsonl 7 5",
		"2026-11.jsonl: seq 7 does not chain to the previous record":                                 "unchained 2026-11.jsonl 7",
		"2026-11.jsonl: ends in a torn line":                                                         "torn_end 2026-11.jsonl",
		"2026-12.jsonl: does not open with file_start":                                               "no_file_start 2026-12.jsonl",
		"something new": "something new",
	} {
		if got := ProblemKey(problem); got != want {
			t.Errorf("%q: %q, want %q", problem, got, want)
		}
	}
}
