package webui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/presence"
)

// passkeysDaemon is the console's client with the daemon's approvals and
// passkeys; it records the origins ceremonies were begun for.
type passkeysDaemon struct {
	*approvalsDaemon
	st      presence.Status
	origins []string
	finish  []cerbapi.EnrollFinishArgs
}

func (d *passkeysDaemon) PasskeyStatus(context.Context) (presence.Status, error) { return d.st, nil }
func (d *passkeysDaemon) PasskeyEnrollBegin(_ context.Context, args cerbapi.EnrollBeginArgs) (cerbapi.EnrollCeremony, error) {
	d.origins = append(d.origins, args.Origin)
	return cerbapi.EnrollCeremony{Ceremony: "c1", Creation: "e30"}, nil
}
func (d *passkeysDaemon) PasskeyEnrollFinish(_ context.Context, args cerbapi.EnrollFinishArgs) (presence.KeyInfo, error) {
	d.finish = append(d.finish, args)
	return presence.KeyInfo{Fingerprint: "aa"}, nil
}
func (d *passkeysDaemon) PasskeyRemoveBegin(_ context.Context, args cerbapi.RemoveBeginArgs) (cerbapi.PasskeyCeremony, error) {
	d.origins = append(d.origins, args.Origin)
	return cerbapi.PasskeyCeremony{Ceremony: "c2"}, nil
}
func (d *passkeysDaemon) PasskeyRemoveFinish(context.Context, cerbapi.RemoveFinishArgs) error {
	return nil
}
func (d *passkeysDaemon) ApprovalChallenge(_ context.Context, _ string, args cerbapi.ApprovalChallengeArgs) (cerbapi.PasskeyCeremony, error) {
	d.origins = append(d.origins, args.Origin)
	return cerbapi.PasskeyCeremony{Ceremony: "c3", Options: "e30"}, nil
}

func passkeysConsole(t *testing.T) (*passkeysDaemon, http.Handler, string) {
	t.Helper()
	ad, _, _ := approvalsConsole(t)
	ad.a.Channel = approval.ChannelOutOfBand
	d := &passkeysDaemon{approvalsDaemon: ad}
	srv := mustNew(t, d)
	h := srv.Handler(testGuard())
	cookie := signIn(t, srv, h)
	_, token := sessionOf(t, h, cookie)
	return d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
	}), token
}

// Every passkey ceremony passes the state-change guard first, is begun for
// the page's own origin, and an out-of-band approve carries its assertion
// through to the daemon.
func TestConsolePasskeyCeremonies(t *testing.T) {
	d, h, token := passkeysConsole(t)
	withOrigin := func(tok, path, body, origin string) int {
		req := newTestRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", tok)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return serve(h, req).Code
	}
	const origin = "http://127.0.0.1:9090"
	for _, path := range []string{"/api/approvals/keys/register/begin", "/api/approvals/keys/remove/begin", "/api/approvals/keys/register/finish", "/api/approvals/apr_1/challenge"} {
		if code := withOrigin("", path, `{}`, origin); code != http.StatusForbidden {
			t.Errorf("%s without the action token: %d", path, code)
		}
		if code := withOrigin(token, path, `{}`, "http://evil.example"); code != http.StatusForbidden {
			t.Errorf("%s from another origin: %d", path, code)
		}
	}
	if len(d.origins) != 0 || len(d.finish) != 0 {
		t.Fatalf("a refused ceremony reached the daemon: %v", d.origins)
	}
	if code := withOrigin(token, "/api/approvals/keys/register/begin", `{"token":"t"}`, ""); code != http.StatusBadRequest {
		t.Fatalf("a ceremony without an Origin: %d", code)
	}
	for _, path := range []string{"/api/approvals/keys/register/begin", "/api/approvals/keys/remove/begin", "/api/approvals/apr_1/challenge"} {
		if code := withOrigin(token, path, `{"token":"t","fingerprint":"aa"}`, origin); code != http.StatusOK {
			t.Fatalf("%s: %d", path, code)
		}
	}
	if strings.Join(d.origins, ",") != strings.Repeat(origin+",", 2)+origin {
		t.Fatalf("origins = %v", d.origins)
	}
	if code := withOrigin(token, "/api/approvals/keys/register/finish", `{"ceremony":"c1","credential":{"id":"x"}}`, origin); code != http.StatusOK || string(d.finish[0].Attestation) != `{"id":"x"}` {
		t.Fatalf("finish: %d %+v", code, d.finish)
	}
	if code := withOrigin(token, "/api/approvals/apr_1/decide", `{"approve":true,"typed":"web","assertion":{"ceremony":"c3"}}`, origin); code != http.StatusOK ||
		string(d.decided[0].Assertion) != `{"ceremony":"c3"}` {
		t.Fatalf("decide: %d %+v", code, d.decided)
	}
}

// The header carries the passkeys alert with the session.
func TestSessionCarriesThePasskeysAlert(t *testing.T) {
	d, h, _ := passkeysConsole(t)
	d.st = presence.Status{State: presence.StateNotSetUp}
	rec := serve(h, newTestRequest(http.MethodGet, "/api/session", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cerberus approvals enroll") || !strings.Contains(rec.Body.String(), `"alert":true`) {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
}
