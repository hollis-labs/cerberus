package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hollis-labs/cerberus/internal/api"
)

// cerberus_host_capacity: main's handler over POST /api/hosts/capacity, the arguments sent as the
// query, the answer with main's notes (capacityText).

func init() { handlers["cerberus_host_capacity"] = hostCapacityTool }

func hostCapacityTool(ctx context.Context, b Backend, args map[string]any) (any, error) {
	var res api.CapacityResult
	if err := b.Do(ctx, http.MethodPost, "/hosts/capacity", args, &res); err != nil {
		return nil, err
	}
	notes := []string{fmt.Sprintf("%d matched, %d unknown, of %d declared hosts (probe age limit %s). Facts only: hosts are in name order, not ranked, and none is reserved.",
		len(res.Matched), len(res.Unknown), res.Total, res.MaxStaleAge)}
	for _, u := range res.Unknown {
		notes = append(notes, fmt.Sprintf("UNKNOWN %s (not guessed to fit): %s. %s", u.Name, strings.Join(u.Reasons, "; "), u.NextStep))
	}
	for _, x := range res.Excluded {
		notes = append(notes, fmt.Sprintf("EXCLUDED %s: %s", x.Name, strings.Join(x.Reasons, "; ")))
	}
	for _, v := range res.Matched {
		if note := hostNote(v); note != "" {
			notes = append(notes, "MATCHED, with a caveat: "+note)
		}
	}
	return withNotes(res, notes)
}
