package audit

import (
	"regexp"
	"strings"
)

// problemForms are Check's problems as kinds with their positions (file, seq), most specific
// first: the wording is for a person, the key for what an acknowledgment is bound to.
var problemForms = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"anchor_cut", regexp.MustCompile(`^anchored seq (\d+) is (?:past the end of|not where it was written in) (\S+?):`)},
	{"anchor_file_missing", regexp.MustCompile(`^(\S+), which holds anchored seq (\d+), is missing$`)},
	{"anchor_unreadable", regexp.MustCompile(`^the chain's anchor could not be read`)},
	{"hash_mismatch", regexp.MustCompile(`^(\S+): seq (\d+) hash does not match its content$`)},
	{"unchained", regexp.MustCompile(`^(\S+): seq (\d+) does not chain to the previous record$`)},
	{"seq_gap", regexp.MustCompile(`^(\S+): seq (\d+) follows (\d+)$`)},
	{"torn_unbroken", regexp.MustCompile(`^(\S+): a torn line before seq (\d+) is not followed by chain_break$`)},
	{"torn_end", regexp.MustCompile(`^(\S+): ends in a torn line$`)},
	{"unpruned_file_start", regexp.MustCompile(`^(\S+): opens with file_start after (\S+),`)},
	{"no_chain_start", regexp.MustCompile(`^(\S+): the first file does not open with chain_start$`)},
	{"no_file_start", regexp.MustCompile(`^(\S+): does not open with file_start$`)},
}

// ProblemKey is a Check problem as its kind and positions ("hash_mismatch 2026-11.jsonl 13"),
// without its wording. A cut reads "past the end" until records are written after it and "not
// where it was written" after; both are the same anchor_cut at the same seq and file. A
// problem of no known form is its own text.
func ProblemKey(problem string) string {
	for _, f := range problemForms {
		if m := f.re.FindStringSubmatch(problem); m != nil {
			parts := append([]string{f.kind}, m[1:]...)
			if f.kind == "anchor_cut" {
				parts = []string{f.kind, m[2], m[1]}
			}
			return strings.Join(parts, " ")
		}
	}
	return problem
}
