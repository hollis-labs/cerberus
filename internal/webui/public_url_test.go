package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/loopback"
)

// Exercise login, session and CSRF protection through real HTTP serialization
// and an HTTPS reverse proxy whose upstream remains HTTP on loopback.
func TestPublicConsoleThroughHTTPSProxy(t *testing.T) {
	const base = "https://cerberus.example"
	srv := mustNew(t, &fakeClient{})
	if checkErr := srv.SetPublicURL(base); checkErr != nil {
		t.Fatal(checkErr)
	}
	backend := httptest.NewServer(srv.Handler(loopback.NewGuard("127.0.0.1", "4783")))
	defer backend.Close()
	upstream, _ := url.Parse(backend.URL)
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.Out.Host = "127.0.0.1:4783"
	}})
	defer proxy.Close()
	client := proxy.Client()
	client.Jar, _ = cookiejar.New(nil)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request := func(method, path, origin, key, action string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, proxy.URL+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "cerberus.example"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set(sessionKeyHeader, key)
		req.Header.Set("X-Cerberus-Web-Token", action)
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		if checkErr := resp.Body.Close(); checkErr != nil {
			t.Fatal(checkErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		return resp, body
	}
	home := t.TempDir()
	path := LoginKeyPath(home, "127.0.0.1:4783")
	remove, err := srv.WriteLoginKey(path, base)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if origins := ConsoleOrigins(home); len(origins) != 1 || origins[0] != base {
		t.Fatalf("origins = %v", origins)
	}
	link, err := MintLoginURL(path)
	if err != nil || !strings.HasPrefix(link, base+"/login?token=") {
		t.Fatalf("login URL %q: %v", link, err)
	}
	resp, _ := request(http.MethodGet, "/api/session", base, "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", resp.StatusCode)
	}
	loginPath := strings.TrimPrefix(link, base)
	resp, _ = request(http.MethodGet, loginPath, "", "", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Domain != "" {
		t.Fatalf("cookie = %+v", cookies)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	fragment, _ := url.ParseQuery(location.Fragment)
	key := fragment.Get(sessionKeyFragment)
	if key == "" {
		t.Fatal("missing session key")
	}
	resp, _ = request(http.MethodGet, loginPath, "", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused login: %d", resp.StatusCode)
	}
	resp, body := request(http.MethodGet, "/api/session", base, key, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session: %d %s", resp.StatusCode, body)
	}
	var session struct {
		ActionToken string `json:"action_token"`
	}
	if checkErr := json.Unmarshal(body, &session); checkErr != nil {
		t.Fatal(checkErr)
	}
	if session.ActionToken == "" {
		t.Fatal("missing action token")
	}
	resp, _ = request(http.MethodPost, "/api/logout", "https://evil.example", key, session.ActionToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/logout", "http://cerberus.example", key, session.ActionToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("insecure origin: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/logout", base, key, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing action token: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/logout", base, key, session.ActionToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if cookies := resp.Cookies(); len(cookies) != 1 || !cookies[0].Secure || cookies[0].MaxAge != -1 {
		t.Fatalf("logout cookies: %+v", cookies)
	}
	resp, _ = request(http.MethodGet, "/api/session", base, key, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", resp.StatusCode)
	}

	scoped, err := MintApprovalURL(path, "apr_public")
	if err != nil || !strings.HasPrefix(scoped, base+"/login?approval=apr_public&token=") {
		t.Fatalf("approval URL %q: %v", scoped, err)
	}
	resp, _ = request(http.MethodGet, strings.TrimPrefix(scoped, base), "", "", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approval login: %d", resp.StatusCode)
	}
	location, _ = url.Parse(resp.Header.Get("Location"))
	if location.Path != "/approvals" || location.Query().Get("id") != "apr_public" {
		t.Fatalf("approval redirect: %s", location)
	}
	resp, _ = request(http.MethodGet, location.RequestURI(), "", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy rewrote Host and approval page redirected away: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	fragment, _ = url.ParseQuery(location.Fragment)
	resp, _ = request(http.MethodGet, "/api/resources", base, fragment.Get(sessionKeyFragment), "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped session read resources: %d", resp.StatusCode)
	}
}
