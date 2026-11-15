package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/hollis-labs/substrate/apierr"

	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// DataTokenHeader carries a data query's session token (slice 7d). The old build's route took
// it as the Authorization bearer, its only credential. Under /api the auth guard reads
// Authorization for the caller's own key, so the session token travels beside it, and the
// route needs both: a key holding cerberus.read, and a verified data session with its grant.
const DataTokenHeader = "X-Cerberus-Data-Token" //nolint:gosec // a header name, not a credential

// DataVersionHeader is old's version mark; a client refuses an answer without it.
const DataVersionHeader = "Cerberus-Data-Version"

// dataQuery is POST /api/data/v1/query, the old socket's POST /data/v1/query (docs/data-access.md):
// a strict JSON dataaccess.Request (at most 64 KiB, no unknown fields), the session token in
// DataTokenHeader, answered with the projection's columns and rows, or a refusal (403
// data_refused, 503 data_unavailable). A request with a browser Origin is refused, as old's
// console forwarder refused it: a browser session is never data authority. Unconfigured (no
// data-access.json, or observe mode) is 503. The service verifies the token, the grant, the
// revocation snapshot, and the brakes and policy, before execution and again before delivery.
func (a *API) dataQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(DataVersionHeader, "1")
	if a.o.Data == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "data_unavailable", "data access is not configured", nil)
		return
	}
	if r.Header.Get("Origin") != "" {
		apierr.Write(w, http.StatusForbidden, "data_refused", "browser-origin data requests are not supported", nil)
		return
	}
	token := strings.TrimSpace(r.Header.Get(DataTokenHeader))
	if token == "" {
		apierr.Write(w, http.StatusUnauthorized, "data_token_required", "data session token required in "+DataTokenHeader, nil)
		return
	}
	ctx, scope := redact.EnsureScope(r.Context())
	scope.Add("data session", token)
	var req dataaccess.Request
	dec := json.NewDecoder(io.LimitReader(r.Body, 65537))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		apierr.Write(w, http.StatusBadRequest, "invalid_args", "invalid data request", nil)
		return
	}
	result, err := a.o.Data.Query(ctx, token, req)
	if err != nil {
		status, code := http.StatusForbidden, "data_refused"
		if errors.Is(err, dataaccess.ErrUnavailable) {
			status, code = http.StatusServiceUnavailable, "data_unavailable"
		}
		apierr.Write(w, status, code, err.Error(), nil)
		return
	}
	b, err := json.Marshal(result)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, "internal", "the answer could not be encoded", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(scope.ReplaceValues(string(b))))
}
