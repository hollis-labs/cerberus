package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// approvalsDaemon is the console's client with the daemon's approvals.
type approvalsDaemon struct {
	*fakeClient
	a       approval.Approval
	decided []cerbapi.ApprovalDecisionArgs
	err     error
}

func (d *approvalsDaemon) ListApprovals(context.Context) (cerbapi.ApprovalList, error) {
	return cerbapi.ApprovalList{Approvals: []approval.Approval{d.a}}, nil
}
func (d *approvalsDaemon) GetApproval(context.Context, string) (approval.Approval, error) {
	return d.a, nil
}
func (d *approvalsDaemon) DecideApproval(_ context.Context, _ string, args cerbapi.ApprovalDecisionArgs) (approval.Approval, error) {
	if d.err != nil {
		return approval.Approval{}, d.err
	}
	d.decided = append(d.decided, args)
	return d.a, nil
}
func (d *approvalsDaemon) RevokeApproval(context.Context, string, cerbapi.ApprovalRevokeArgs) (approval.Approval, error) {
	return d.a, d.err
}

func approvalsConsole(t *testing.T) (*approvalsDaemon, http.Handler, string) {
	t.Helper()
	d := &approvalsDaemon{fakeClient: &fakeClient{}, a: approval.Approval{ID: "apr_1", Status: approval.Pending, Connector: "docker", Operation: "stop",
		Target: audit.Target{Kind: "docker.container", Resource: "web"}, Channel: approval.ChannelTTYConfirm, PlanHash: "sha256:plan",
		Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio", Client: "claude-code"}}}
	srv := mustNew(t, d)
	h := srv.Handler(testGuard())
	cookie := signIn(t, srv, h)
	_, token := sessionOf(t, h, cookie)
	return d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
	}), token
}

func consolePost(h http.Handler, token, path, body string) *httptest.ResponseRecorder {
	req := newTestRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cerberus-Web-Token", token)
	return serve(h, req)
}

// The page lists and shows approvals with who asked and the plan; an approve
// needs the target typed, and reaches the daemon only then.
func TestConsoleApprovals(t *testing.T) {
	d, h, token := approvalsConsole(t)
	rec := serve(h, newTestRequest(http.MethodGet, "/api/approvals", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"plan_hash":"sha256:plan"`) || !strings.Contains(rec.Body.String(), `"via":"mcp_stdio"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec = consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":true,"typed":"wrong"}`); rec.Code != http.StatusBadRequest || len(d.decided) != 0 {
		t.Fatalf("a wrong target approved: %d %s", rec.Code, rec.Body.String())
	}
	if rec = consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":true,"typed":"web","reason":"ok"}`); rec.Code != http.StatusOK || len(d.decided) != 1 || !d.decided[0].Approve {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec = consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":false}`); rec.Code != http.StatusOK || len(d.decided) != 2 || d.decided[1].Approve {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}

	// The daemon's refusal — out of band with no passkey — reaches the page
	// with its status and what to do.
	d.err = &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalRequired, Connector: "approvals", Operation: "decide", Err: errors.New("enroll a passkey")}
	if rec = consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":true,"typed":"web"}`); rec.Code < 400 || !strings.Contains(rec.Body.String(), "enroll a passkey") {
		t.Fatalf("a refused approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec = consolePost(h, "", "/api/approvals/apr_1/decide", `{"approve":false}`); rec.Code != http.StatusForbidden {
		t.Fatalf("no action token: %d", rec.Code)
	}
}

// Passkeys work on localhost, not on an IP: the console names itself
// localhost, a sign-in can land on an approval's page, and a visit to the
// approvals page on 127.0.0.1 moves to localhost.
func TestConsoleIsLocalhostForPasskeys(t *testing.T) {
	for listen, want := range map[string]string{"127.0.0.1:4783": "http://localhost:4783", "[::1]:4783": "http://localhost:4783",
		"localhost:9000": "http://localhost:9000", "127.0.0.2:4783": "http://127.0.0.2:4783"} {
		if got := ConsoleBaseURL(listen); got != want {
			t.Errorf("ConsoleBaseURL(%s) = %s, want %s", listen, got, want)
		}
	}
	for next, want := range map[string]string{"/approvals?id=a": "/approvals?id=a", "": "/", "https://evil.example/": "/", "//evil.example": "/", "/\\evil": "/", "approvals": "/"} {
		if got := safeNext(next); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", next, got, want)
		}
	}

	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	link := loginPath(t, srv) + "&next=%2Fapprovals%3Fid%3Dapr_1"
	if rec := serve(h, newTestRequest(http.MethodGet, link, nil)); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/approvals?id=apr_1" {
		t.Fatalf("login next: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec := serve(h, newTestRequest(http.MethodGet, "/approvals?id=apr_1", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "http://localhost:9090/approvals?id=apr_1" {
		t.Fatalf("127.0.0.1 approvals page: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	local := newTestRequest(http.MethodGet, "/approvals", nil)
	local.Host = "localhost:9090"
	if rec := serve(h, local); rec.Code != http.StatusOK {
		t.Fatalf("localhost approvals page: %d", rec.Code)
	}
	// The P0 guards still turn away every other name.
	evil := newTestRequest(http.MethodGet, "/approvals", nil)
	evil.Host = "evil.example:9090"
	if rec := serve(h, evil); rec.Code != http.StatusForbidden {
		t.Fatalf("a foreign Host: %d", rec.Code)
	}
	if !testGuard().OriginAllowed("http://localhost:9090") || testGuard().OriginAllowed("http://localhost:1234") {
		t.Fatal("the Origin check must accept exactly localhost on the listen port")
	}
}

// The console answers a daemon refusal with the daemon's status, for every
// class, and a connector refusal whose code has no status of its own falls
// back to the daemon's rather than to 500.
func TestConsolePassesOnTheDaemonsStatus(t *testing.T) {
	d, h, token := approvalsConsole(t)
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusLocked,
		http.StatusInternalServerError, http.StatusServiceUnavailable} {
		d.err = &cerbapi.DaemonStatusError{Status: status, Err: errors.New("daemon: refused")}
		if rec := consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":false}`); rec.Code != status || !strings.Contains(rec.Body.String(), "daemon: refused") {
			t.Errorf("%d: console answered %d %s", status, rec.Code, rec.Body.String())
		}
	}
	d.err = &cerbapi.DaemonStatusError{Status: http.StatusConflict, Err: &cerbapi.ExternalConnectorError{Code: "some_new_code", Connector: "approvals", Operation: "decide", Err: errors.New("no")}}
	if rec := consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":false}`); rec.Code != http.StatusConflict {
		t.Errorf("an unmapped connector code: %d", rec.Code)
	}
	d.err = errors.New("not from the daemon")
	if rec := consolePost(h, token, "/api/approvals/apr_1/decide", `{"approve":false}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("a local error: %d", rec.Code)
	}
}
