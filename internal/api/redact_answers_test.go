package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// The answers of L7/L8 and the brake routes go through the request's redaction scope too, as
// main's socket did: a secret-shaped value in a mutation's message or a lockdown's reason is
// redacted.
func TestMutationAndBrakeAnswersAreRedacted(t *testing.T) {
	secret := "ghp" + "_" + strings.Repeat("Zq7x", 9)
	h := mutatingAPI(t, &recordingMutator{out: &supervise.OpResult{Success: true, ServiceID: "web", Message: "reloaded with Bearer " + secret}})
	code, body := post(t, h, "/api/resources/web/reload", strings.NewReader(`{"acknowledged":true}`))
	if code != 200 || strings.Contains(body, secret) || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("L7 answer: %d %s", code, body)
	}

	w := gatetest.New(t, gatetest.Options{})
	w.Engage(t, brake.State{Lockdown: &brake.Lockdown{ID: "ldn_1", EngagedAt: time.Now(), Reason: "pasted Bearer " + secret}})
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return fixture(), nil }, Home: t.TempDir(), Gate: w.Gate})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/brakes", nil)
	req = req.WithContext(libauth.WithPrincipal(req.Context(), apiKey))
	rec := httptest.NewRecorder()
	a.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), secret) || !strings.Contains(rec.Body.String(), "[REDACTED]") {
		t.Fatalf("GET /api/brakes: %d %s", rec.Code, rec.Body.String())
	}
}
