package registry

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// unknownFieldName is the ValidationIssue.Field value carried by every
// unrecognised-field finding. Author-time callers select on it; see
// ValidationResult.UnknownFieldIssues.
const unknownFieldName = "unknown-field"

// unknownFieldMarker is the fragment yaml.v3 puts in a KnownFields
// complaint ("line 4: field foo not found in type registry.ProjectConfig").
// Matching on it keeps genuine type errors out of the unknown-field
// report — those already came back from the caller's lenient decode.
const unknownFieldMarker = "not found in type"

// unknownFields re-decodes data strictly for the sole purpose of naming
// the fields the lenient decode ignored.
//
// This is the forward-compatibility seam. The runtime readers parse
// leniently so a field written by a newer writer can never drop a whole
// project — the failure mode that made 17 of 18 projects invisible on
// 2026-05-25, when a `registry_urn` field appeared. Author-time callers
// still need to catch a typo, so the strict pass runs anyway and its
// complaints become data instead of a hard parse failure.
//
// It never returns an error: anything that would break a real parse has
// already been surfaced by the caller's lenient decode, and a
// strict-only complaint is information, not a failure.
//
// into must be a pointer to a fresh zero value of the type being
// probed; the decoded result is discarded.
func unknownFields(data []byte, into any) []string {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(into)
	if err == nil {
		return nil
	}

	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return nil
	}

	var out []string
	for _, msg := range typeErr.Errors {
		if strings.Contains(msg, unknownFieldMarker) {
			out = append(out, msg)
		}
	}
	return out
}

// unknownFieldError is the author-time refusal shared by register and
// validate. It names both plausible causes, because the reader cannot
// tell them apart from the file alone: a typo, or a field belonging to
// a Cerberus newer than the binary reading it.
func unknownFieldError(path string, issues []ValidationIssue) error {
	return fmt.Errorf(
		"%s has %d unrecognised field(s), first: %s — fix the typo, or upgrade cerberus if the field is newer than this binary",
		path, len(issues), issues[0].Message)
}
