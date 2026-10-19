package dataaccess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	auth "github.com/hollis-labs/libs/security/auth"
)

// Revocations adds a freshness ceiling to the shared deny-list contract. A
// failed refresh never clears revocations or advances the freshness clock.
type Revocations struct {
	URL    string
	Client *http.Client
	List   *auth.DenyList
	Now    func() time.Time
	mu     sync.Mutex
	last   time.Time
	etag   string
}

func NewRevocations(url string) *Revocations {
	return &Revocations{URL: url, Client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}, List: auth.NewDenyList(), Now: time.Now}
}
func (r *Revocations) Check(p auth.Principal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last.IsZero() || r.Now().Sub(r.last) >= 10*time.Second || r.Now().Before(r.last) || r.List.Denied(p) {
		return errors.New("data authorization unavailable or revoked")
	}
	return nil
}
func (r *Revocations) Poll(ctx context.Context) error {
	r.mu.Lock()
	etag := r.etag
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return errors.New("revocation refresh failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.last.IsZero() || etag == "" || etag != r.etag {
			return errors.New("revocation full snapshot required")
		}
		r.last = r.Now()
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("revocation refresh refused")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return errors.New("invalid revocation snapshot")
	}
	var doc auth.DenyListDoc
	// All three arrays must be present; '{}' must not clear a deny-list.
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields["jti"] == nil || fields["sub"] == nil || fields["sid"] == nil || json.Unmarshal(b, &doc) != nil {
		return errors.New("invalid revocation snapshot")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.List.Replace(doc)
	r.etag = resp.Header.Get("ETag")
	r.last = r.Now()
	return nil
}
func (r *Revocations) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = r.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
