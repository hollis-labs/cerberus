package webui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// The console's login (Decision 20). `cerberus web` and `cerberus web open`
// hand the operator a one-time URL; visiting it exchanges the token for a
// session cookie, and every /api/ request needs that session.
//
// A login token is expiry || nonce || HMAC(key, expiry || nonce), under a key
// this process generated at start and wrote only to a 0600 file in the
// operator's home. `cerberus web open` mints one from that file, so asking
// for a login needs no request to the server, and proves the same thing the
// file does: that the asker is the operator's own account. The server
// checks the MAC, the expiry and that the nonce is unused.
//
// Nothing outlives the process: the key, the used nonces and the sessions
// live in memory, so restarting `cerberus web` signs everyone out and
// invalidates every link it handed out.

const (
	// sessionCookie is the session cookie's name before the port: the
	// console names its cookie for the port it listens on (cookieName).
	sessionCookie = "cerberus_session"

	// sessionKeyHeader carries the key a session is bound to. The console
	// page holds it in localStorage, which a browser scopes to the origin,
	// port included; a cookie is sent to every port on localhost. A cookie
	// that reaches another local server (any port, any account) is not a
	// session without it (H6).
	sessionKeyHeader = "X-Cerberus-Session-Key"
	// sessionKeyFragment is the fragment parameter the key reaches the page
	// in, after sign-in: a fragment is never sent to a server, and the page
	// removes it from the address bar.
	sessionKeyFragment = "cerberus-key"

	// DefaultLoginTTL is how long a one-time URL is good for.
	DefaultLoginTTL = 2 * time.Minute
	// DefaultSessionIdle is how long a session lasts without a request.
	DefaultSessionIdle = 30 * time.Minute
	// DefaultSessionMax is how long a session lasts at all.
	DefaultSessionMax = 12 * time.Hour

	loginKeyBytes   = 32
	loginNonceBytes = 16
)

// session is one signed-in browser. The cookie carries secret; the store is
// keyed by its hash. ID is the public name the audit record uses — never the
// cookie's value.
type session struct {
	ID          string
	actionToken string
	// bindHash is sha256 of the key the signing-in page was given.
	bindHash string
	// scope is the one approval a session from an approval link may act
	// on (M7); "" is the full console.
	scope    string
	created  time.Time
	lastSeen time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	key      []byte
	used     map[string]time.Time // nonce -> expiry, pruned as they lapse
	sessions map[string]*session  // sha256(cookie) -> session
	loginTTL time.Duration
	idle     time.Duration
	max      time.Duration
	now      func() time.Time
}

func newSessionStore() (*sessionStore, error) {
	key, err := randomBytes(loginKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("generate web login key: %w", err)
	}
	return &sessionStore{
		key: key, used: map[string]time.Time{}, sessions: map[string]*session{},
		loginTTL: DefaultLoginTTL, idle: DefaultSessionIdle, max: DefaultSessionMax, now: time.Now,
	}, nil
}

// mintLoginToken is a one-time token under key, good until now+ttl.
func mintLoginToken(key []byte, now time.Time, ttl time.Duration) (string, error) {
	return mintScopedLoginToken(key, now, ttl, "")
}

// mintScopedLoginToken is a one-time token for a session limited to one
// approval (M7), or, with scope "", a full one. The scope is inside the
// MAC: a scoped token redeemed as a full one, or a full one as scoped, or
// either for another approval, fails the check.
func mintScopedLoginToken(key []byte, now time.Time, ttl time.Duration, scope string) (string, error) {
	nonce, err := randomBytes(loginNonceBytes)
	if err != nil {
		return "", fmt.Errorf("generate web login token: %w", err)
	}
	payload := make([]byte, 8, 8+loginNonceBytes+sha256.Size)
	binary.BigEndian.PutUint64(payload, uint64(now.Add(ttl).Unix())) //nolint:gosec // a Unix time after 1970
	payload = append(payload, nonce...)
	return base64.RawURLEncoding.EncodeToString(append(payload, loginMAC(key, payload, scope)...)), nil
}

// loginMAC is a token's MAC: over its payload alone for a full session, and
// over the payload and the approval it is scoped to otherwise.
func loginMAC(key, payload []byte, scope string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if scope != "" {
		mac.Write([]byte("\x00approval:" + scope))
	}
	return mac.Sum(nil)
}

var errLoginToken = errors.New("sign-in link expired, already used, or not from this console")

// redeem checks a login token and, if it is good, spends it and starts a
// session, returning the cookie value and the key the session is bound to.
func (st *sessionStore) redeem(token, scope string) (string, string, *session, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 8+loginNonceBytes+sha256.Size {
		return "", "", nil, errLoginToken
	}
	payload, sum := raw[:8+loginNonceBytes], raw[8+loginNonceBytes:]
	if !hmac.Equal(sum, loginMAC(st.key, payload, scope)) {
		return "", "", nil, errLoginToken
	}
	expiry := time.Unix(int64(binary.BigEndian.Uint64(payload[:8])), 0) //nolint:gosec // written by mintLoginToken
	nonce := hex.EncodeToString(payload[8:])

	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	for n, exp := range st.used {
		if now.After(exp) {
			delete(st.used, n)
		}
	}
	if now.After(expiry) {
		return "", "", nil, errLoginToken
	}
	if _, spent := st.used[nonce]; spent {
		return "", "", nil, errLoginToken
	}
	st.used[nonce] = expiry

	cookie, err := randomToken()
	if err != nil {
		return "", "", nil, err
	}
	action, err := randomToken()
	if err != nil {
		return "", "", nil, err
	}
	id, err := randomBytes(8)
	if err != nil {
		return "", "", nil, err
	}
	bind, err := randomToken()
	if err != nil {
		return "", "", nil, err
	}
	s := &session{ID: hex.EncodeToString(id), actionToken: action, bindHash: cookieKey(bind), scope: scope, created: now, lastSeen: now}
	st.sessions[cookieKey(cookie)] = s
	return cookie, bind, s, nil
}

// lookup returns the live session for a cookie value and the key it is
// bound to, and marks it used. An idle or over-age session is ended here. A
// cookie without its key is not a session: it is what another server on
// localhost receives.
func (st *sessionStore) lookup(cookie, key string) *session {
	if cookie == "" || key == "" {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.sessions[cookieKey(cookie)]
	if !ok {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(cookieKey(key)), []byte(s.bindHash)) != 1 {
		return nil
	}
	now := st.now()
	if now.Sub(s.lastSeen) > st.idle || now.Sub(s.created) > st.max {
		delete(st.sessions, cookieKey(cookie))
		return nil
	}
	s.lastSeen = now
	return s
}

func (st *sessionStore) end(cookie string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, cookieKey(cookie))
}

func cookieKey(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

type sessionKey struct{}

// webSession is the signed-in session a console request carries, or nil.
func webSession(ctx context.Context) *session {
	s, _ := ctx.Value(sessionKey{}).(*session)
	return s
}

// loginRequired is the answer to an /api/ request with no live session. It
// names the command that signs in.
const loginRequired = "sign in required: run `cerberus web open` in a terminal on the Cerberus server for a one-time sign-in link"

// requireSession lets an /api/ request through only with a live session,
// which it puts on the request's context. Everything else — the SPA's
// assets and the /login exchange — needs none: the assets are the same for
// everyone, and /login is how a session starts.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, _ := r.Cookie(s.cookieName())
		var value string
		if cookie != nil {
			value = cookie.Value
		}
		sess := s.sessions.lookup(value, r.Header.Get(sessionKeyHeader))
		if sess == nil {
			writeErrorBody(w, http.StatusUnauthorized, loginRequired, "login_required")
			return
		}
		if sess.scope != "" && !scopedRouteAllowed(r, sess.scope) {
			writeErrorBody(w, http.StatusForbidden, fmt.Sprintf("this sign-in is for approval %s only; run `cerberus web open` in a terminal for the full console", sess.scope), "scoped_session")
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, sess)
		ctx = cerbapi.WithPrincipal(ctx, cerbapi.WebSessionPrincipal(sess.ID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// handleLogin exchanges a one-time token for a session cookie, then sends
// the browser to the console with the token gone from its address bar and
// its history.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	scope := r.URL.Query().Get("approval")
	cookie, bind, sess, err := s.sessions.redeem(r.URL.Query().Get("token"), scope)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, "%s. Run `cerberus web open` in a terminal on the Cerberus server for a new one.\n", errLoginToken)
		return
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure for HTTPS proxy mode; local-only default uses HTTP
		Name: s.cookieName(), Value: cookie, Path: "/",
		HttpOnly: true, Secure: s.publicURL != "", SameSite: http.SameSiteStrictMode,
		MaxAge: int(s.sessions.max / time.Second),
	})
	s.logger.Info("webui.session.started", "session", sess.ID, "scope", sess.scope)
	// The key goes to the page in the fragment, which no server sees; the
	// page keeps it in its origin's localStorage and sends it on every
	// request.
	next, _, _ := strings.Cut(safeNext(r.URL.Query().Get("next")), "#")
	if scope != "" {
		// A scoped session goes to its approval and nowhere else.
		next = "/approvals?id=" + url.QueryEscape(scope)
	}
	http.Redirect(w, r, next+"#"+sessionKeyFragment+"="+bind, http.StatusSeeOther)
}

// handleLogout ends the session and clears its cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	if cookie, err := r.Cookie(s.cookieName()); err == nil {
		s.sessions.end(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.publicURL != "", SameSite: http.SameSiteStrictMode, MaxAge: -1}) //nolint:gosec // Secure for HTTPS proxy mode; local-only default uses HTTP
	if sess := webSession(r.Context()); sess != nil {
		s.logger.Info("webui.session.ended", "session", sess.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// cookieName is the session cookie's name for the port this console
// listens on. A browser sends every localhost cookie to every port, so two
// consoles on two ports would otherwise overwrite each other's session, and
// a cookie another local server set could stand in for this one's.
func (s *Server) cookieName() string {
	if s.guard == nil || s.guard.Port() == "" {
		return sessionCookie
	}
	return sessionCookie + "_" + s.guard.Port()
}

// LoginURL mints a one-time sign-in URL for base, the console's own address.
func (s *Server) LoginURL(base string) (string, error) {
	token, err := mintLoginToken(s.sessions.key, s.sessions.now(), s.sessions.loginTTL)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + "/login?token=" + token, nil
}

// ApprovalURL is a one-time sign-in link at base for a session limited to
// one approval (M7), as MintApprovalURL mints from the key file.
func (s *Server) ApprovalURL(base, approvalID string) (string, error) {
	if approvalID == "" {
		return "", errors.New("an approval link needs an approval id")
	}
	token, err := mintScopedLoginToken(s.sessions.key, s.sessions.now(), s.sessions.loginTTL, approvalID)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + "/login?approval=" + url.QueryEscape(approvalID) + "&token=" + token, nil
}

// loginKeyFile is what `cerberus web` leaves for `cerberus web open`: the
// console's address and the key its sign-in links are made with.
type loginKeyFile struct {
	URL string `json:"url"`
	Key string `json:"key"`
	TTL string `json:"ttl"`
}

// LoginKeyPath is where the console listening on listenAddr keeps its key.
func LoginKeyPath(home, listenAddr string) string {
	name := strings.NewReplacer(":", "_", "[", "", "]", "", "/", "_").Replace(listenAddr)
	return filepath.Join(home, ".cerberus", "web", "login-"+name+".key")
}

// WriteLoginKey writes the key file for `cerberus web open`: 0600 in a 0700
// directory, replaced atomically, so no other account can mint a link. The
// returned func removes it; the server calls it on shutdown.
func (s *Server) WriteLoginKey(path, base string) (func(), error) {
	data, err := json.Marshal(loginKeyFile{URL: base, Key: base64.RawURLEncoding.EncodeToString(s.sessions.key), TTL: s.sessions.loginTTL.String()})
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".login-*.key")
	if err != nil {
		return nil, fmt.Errorf("write web login key: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err = tmp.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return nil, fmt.Errorf("install web login key: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// ConsoleBaseURL is the address the console hands out for listenAddr. A
// console on 127.0.0.1 or [::1] is named http://localhost:<port>: a passkey
// (WebAuthn) cannot be used on an IP address, only on a name, and the
// session cookie belongs to the host it was set on, so every link — sign-in
// and approval alike — has to use the same one. Any other loopback address
// keeps its literal form.
func ConsoleBaseURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "http://" + listenAddr
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return "http://" + net.JoinHostPort("localhost", port)
	}
	return "http://" + listenAddr
}

// toLocalhost sends a visit to a passkey page made on 127.0.0.1 or [::1] to
// the same path on localhost, where WebAuthn works. The pages it covers are
// the approvals and enrollment pages; everything else is served where it is.
func toLocalhost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/approvals" || strings.HasPrefix(r.URL.Path, "/approvals/")) {
			host, port, err := net.SplitHostPort(r.Host)
			if err == nil && (host == "127.0.0.1" || host == "::1") {
				target := "http://" + net.JoinHostPort("localhost", port) + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// safeNext is where a sign-in lands: a path on this console, or "/". An
// absolute URL, a scheme-relative "//host" and a backslash are refused, so a
// sign-in link can never send the browser anywhere else.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

// MintLoginURL is `cerberus web open`: a one-time sign-in URL for the console
// whose key file is at path.
func MintLoginURL(path string) (string, error) { return MintLoginURLTo(path, "") }

// MintLoginURLTo is MintLoginURL landing on next, a path on the console such
// as an approval's page, after the sign-in.
func MintLoginURLTo(path, next string) (string, error) {
	link, err := mintLoginURL(path)
	if err != nil || next == "" {
		return link, err
	}
	return link + "&next=" + url.QueryEscape(safeNext(next)), nil
}

// MintApprovalURL mints a one-time link to one approval's page on the
// console at path's key, for a session that can see and decide that
// approval and nothing else (M7). Approving there needs a passkey. It is the
// link Cerberus hands an MCP client in a URL elicitation: a link down the
// agent's channel must not be a console login.
func MintApprovalURL(path, approvalID string) (string, error) {
	if approvalID == "" {
		return "", errors.New("an approval link needs an approval id")
	}
	file, key, ttl, err := readLoginKey(path)
	if err != nil {
		return "", err
	}
	token, err := mintScopedLoginToken(key, time.Now(), ttl, approvalID)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(file.URL, "/") + "/login?approval=" + url.QueryEscape(approvalID) + "&token=" + token, nil
}

func mintLoginURL(path string) (string, error) {
	file, key, ttl, err := readLoginKey(path)
	if err != nil {
		return "", err
	}
	token, err := mintLoginToken(key, time.Now(), ttl)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(file.URL, "/") + "/login?token=" + token, nil
}

// readLoginKey reads the running console's key file.
func readLoginKey(path string) (loginKeyFile, []byte, time.Duration, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own key file under ~/.cerberus/web
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return loginKeyFile{}, nil, 0, fmt.Errorf("no running `cerberus web` for this address (no %s); start it with `cerberus web`", path)
		}
		return loginKeyFile{}, nil, 0, fmt.Errorf("read web login key: %w", err)
	}
	var file loginKeyFile
	if err = json.Unmarshal(data, &file); err != nil {
		return loginKeyFile{}, nil, 0, fmt.Errorf("web login key %s is not valid; restart `cerberus web`", path)
	}
	key, err := base64.RawURLEncoding.DecodeString(file.Key)
	if err != nil || len(key) != loginKeyBytes {
		return loginKeyFile{}, nil, 0, fmt.Errorf("web login key %s is not valid; restart `cerberus web`", path)
	}
	ttl, err := time.ParseDuration(file.TTL)
	if err != nil || ttl <= 0 {
		ttl = DefaultLoginTTL
	}
	return file, key, ttl, nil
}

func randomBytes(n int) ([]byte, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func randomToken() (string, error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", fmt.Errorf("generate web session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// sameToken compares two tokens in constant time.
func sameToken(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ConsoleOrigins are the origins of the consoles running for home, read from
// their key files: where a passkey may be used. A file that cannot be read
// is skipped; a console that is not running has no file.
func ConsoleOrigins(home string) []string {
	paths, _ := filepath.Glob(filepath.Join(home, ".cerberus", "web", "login-*.key"))
	var origins []string
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // the operator's own key files under ~/.cerberus/web
		if err != nil {
			continue
		}
		var file loginKeyFile
		if json.Unmarshal(data, &file) != nil {
			continue
		}
		u, err := url.Parse(file.URL)
		if err != nil || u.Host == "" {
			continue
		}
		origins = append(origins, u.Scheme+"://"+u.Host)
	}
	return origins
}
