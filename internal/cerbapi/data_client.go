package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/dataaccess"
	auth "github.com/hollis-labs/libs/security/auth"
)

// QueryData has no in-process or generic connector fallback. The daemon must
// understand the versioned route and verify the original session token itself.
func (c *SocketClient) QueryData(ctx context.Context, token string, q dataaccess.Request) (dataaccess.Result, error) {
	var out dataaccess.Result
	b, err := json.Marshal(q)
	if err != nil {
		return out, errors.New("invalid data request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/data/v1/query", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return out, errors.New("data daemon unavailable; no fallback was attempted")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cerberus-Data-Version") != "1" {
		return out, errors.New("data request refused or versioned data service unavailable")
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return out, errors.New("invalid data response")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || dec.Decode(new(any)) != io.EOF {
		return out, errors.New("invalid data response")
	}
	return out, nil
}

// DataProxy forwards only a typed data request and the original bearer. It
// never translates a web session or a self-reported principal into data access.
func DataProxy(client interface {
	QueryData(context.Context, string, dataaccess.Request) (dataaccess.Result, error)
}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/data/v1/query" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if client == nil {
			http.Error(w, "data service unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser-origin data requests are not supported", http.StatusForbidden)
			return
		}
		token, state := auth.ParseBearer(r.Header.Values("Authorization"))
		if state != "" {
			http.Error(w, "data session token required", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		var q dataaccess.Request
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&q) != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid data request", http.StatusBadRequest)
			return
		}
		out, err := client.QueryData(r.Context(), token, q)
		if err != nil {
			http.Error(w, "data request refused or unavailable", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cerberus-Data-Version", "1")
		_ = json.NewEncoder(w).Encode(out)
	})
}
