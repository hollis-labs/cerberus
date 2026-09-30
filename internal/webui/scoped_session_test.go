package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// twoApprovals is a daemon holding the approval a link is for and another.
type twoApprovals struct{ *approvalsDaemon }

func (d twoApprovals) ListApprovals(context.Context) (cerbapi.ApprovalList, error) {
	other := d.a
	other.ID = "apr_other"
	return cerbapi.ApprovalList{Approvals: []approval.Approval{d.a, other}, Problems: []string{"a problem the full console shows"}}, nil
}

// approvalLogin spends an approval link for id against h, returning the
// cookie and the redirect.
func approvalLogin(t *testing.T, srv *Server, h http.Handler, id, scopeParam string) (*http.Cookie, string, int) {
	t.Helper()
	token, err := mintScopedLoginToken(srv.sessions.key, srv.sessions.now(), srv.sessions.loginTTL, id)
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, newTestRequest(http.MethodGet, "/login?approval="+scopeParam+"&token="+token, nil))
	for _, c := range rec.Result().Cookies() {
		if isSessionCookie(c) {
			rememberSessionKey(c, rec.Header().Get("Location"))
			return c, rec.Header().Get("Location"), rec.Code
		}
	}
	return nil, rec.Header().Get("Location"), rec.Code
}

// The link an MCP client is handed opens one approval and nothing else: it
// is not a console login (M7).
func TestAnApprovalLinkIsScopedToItsApproval(t *testing.T) {
	d := &approvalsDaemon{fakeClient: &fakeClient{}, a: approval.Approval{ID: "apr_1", Status: approval.Pending, Connector: "ssh", Operation: "exec", Channel: approval.ChannelOutOfBand}}
	srv := mustNew(t, twoApprovals{d})
	h := srv.Handler(testGuard())
	cookie, loc, code := approvalLogin(t, srv, h, "apr_1", "apr_1")
	if code != http.StatusSeeOther || cookie == nil || !strings.HasPrefix(loc, "/approvals?id=apr_1#"+sessionKeyFragment+"=") {
		t.Fatalf("login = %d %q", code, loc)
	}
	get := func(path string) (int, string) {
		rec := serve(h, withCookie(newTestRequest(http.MethodGet, path, nil), cookie))
		return rec.Code, rec.Body.String()
	}
	code, body := get("/api/session")
	var info struct {
		ActionToken string `json:"action_token"`
		Scope       string `json:"scope"`
	}
	_ = json.Unmarshal([]byte(body), &info)
	if code != http.StatusOK || info.Scope != "apr_1" || info.ActionToken == "" {
		t.Fatalf("session = %d %s", code, body)
	}
	if code, body = get("/api/approvals"); code != http.StatusOK || !strings.Contains(body, `"apr_1"`) || strings.Contains(body, "apr_other") || strings.Contains(body, "a problem") {
		t.Fatalf("list = %d %s", code, body)
	}
	if code, _ = get("/api/approvals/apr_1"); code != http.StatusOK {
		t.Fatalf("its approval = %d", code)
	}
	for _, path := range []string{"/api/resources", "/api/approvals/apr_other", "/api/approvals/keys", "/api/settings", "/api/brakes"} {
		if code, body = get(path); code != http.StatusForbidden || !strings.Contains(body, "cerberus web open") {
			t.Errorf("%s = %d %s", path, code, body)
		}
	}
	post := func(path, body string) int {
		req := withCookie(newTestRequest(http.MethodPost, path, strings.NewReader(body)), cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cerberus-Web-Token", info.ActionToken)
		return serve(h, req).Code
	}
	// Approving needs a passkey here, even for a typed-target approval.
	if code := post("/api/approvals/apr_1/decide", `{"approve":true,"typed":""}`); code != http.StatusForbidden || len(d.decided) != 0 {
		t.Fatalf("approve without a passkey = %d, decided %v", code, d.decided)
	}
	if code := post("/api/approvals/apr_1/decide", `{"approve":true,"typed":"","assertion":{"ceremony":"c1"}}`); code == http.StatusForbidden {
		t.Fatalf("approve with a passkey was refused by the scope")
	}
	if code := post("/api/approvals/apr_1/decide", `{"approve":false}`); code != http.StatusOK {
		t.Fatalf("deny = %d", code)
	}
	for _, path := range []string{"/api/approvals/apr_1/revoke", "/api/lockdown", "/api/approvals/apr_other/decide"} {
		if code := post(path, `{}`); code != http.StatusForbidden {
			t.Errorf("POST %s = %d", path, code)
		}
	}
}

// A scoped token is not a full one, nor one for another approval, and a full
// token is not a scoped one: the scope is under the MAC.
func TestAnApprovalLinkCannotBeExchanged(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	if c, _, code := approvalLogin(t, srv, h, "apr_1", ""); c != nil || code != http.StatusUnauthorized {
		t.Fatalf("a scoped token redeemed as a full session: %d", code)
	}
	if c, _, code := approvalLogin(t, srv, h, "apr_1", "apr_2"); c != nil || code != http.StatusUnauthorized {
		t.Fatalf("a scoped token redeemed for another approval: %d", code)
	}
	full := loginPath(t, srv)
	if rec := serve(h, newTestRequest(http.MethodGet, full+"&approval=apr_1", nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a full token redeemed as a scoped one: %d", rec.Code)
	}
}

// MintApprovalURL makes the scoped link from the running console's key.
func TestMintApprovalURL(t *testing.T) {
	srv := mustNew(t, &fakeClient{})
	h := srv.Handler(testGuard())
	path := filepath.Join(t.TempDir(), "login.key")
	remove, err := srv.WriteLoginKey(path, "http://localhost:9090")
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	link, err := MintApprovalURL(path, "apr_1")
	if err != nil || !strings.HasPrefix(link, "http://localhost:9090/login?approval=apr_1&token=") {
		t.Fatalf("link = %q, %v", link, err)
	}
	rec := serve(h, newTestRequest(http.MethodGet, strings.TrimPrefix(link, "http://localhost:9090"), nil))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/approvals?id=apr_1#") {
		t.Fatalf("redeem = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := MintApprovalURL(filepath.Join(t.TempDir(), "none.key"), "apr_1"); err == nil || !os.IsNotExist(err) && !strings.Contains(err.Error(), "cerberus web") {
		t.Fatalf("no console: %v", err)
	}
}
