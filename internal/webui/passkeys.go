package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/presence"
)

// passkeysClient is the daemon's passkey registry as the console reaches it.
type passkeysClient interface {
	PasskeyStatus(ctx context.Context) (presence.Status, error)
	PasskeyEnrollBegin(ctx context.Context, args cerbapi.EnrollBeginArgs) (cerbapi.EnrollCeremony, error)
	PasskeyEnrollFinish(ctx context.Context, args cerbapi.EnrollFinishArgs) (presence.KeyInfo, error)
	PasskeyRemoveBegin(ctx context.Context, args cerbapi.RemoveBeginArgs) (cerbapi.PasskeyCeremony, error)
	PasskeyRemoveFinish(ctx context.Context, args cerbapi.RemoveFinishArgs) error
	ApprovalChallenge(ctx context.Context, id string, args cerbapi.ApprovalChallengeArgs) (cerbapi.PasskeyCeremony, error)
}

func (s *Server) passkeys(w http.ResponseWriter) (passkeysClient, bool) {
	c, ok := s.client.(passkeysClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "this console is not connected to a daemon that holds passkeys")
	}
	return c, ok
}

// ceremonyOrigin is the origin a passkey ceremony is made for: the page's
// own, which the browser writes into what the authenticator signs. The
// state-change guard has already matched it against this console's; the
// daemon matches it again against the consoles that are running.
func ceremonyOrigin(w http.ResponseWriter, r *http.Request) (string, bool) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		writeError(w, http.StatusBadRequest, "a passkey ceremony needs the page's Origin; use the console in a browser")
	}
	return origin, origin != ""
}

// passkeyBody is what the enrollment and removal pages send.
type passkeyBody struct {
	Token       string          `json:"token,omitempty"`
	Label       string          `json:"label,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	Ceremony    string          `json:"ceremony,omitempty"`
	Credential  json.RawMessage `json:"credential,omitempty"`
	Authorize   json.RawMessage `json:"authorize,omitempty"`
}

// handlePasskeys is /api/approvals/keys: GET the registry, and POST the
// enrollment (register/begin, register/finish) and removal (remove/begin,
// remove/finish) ceremonies. The state-change guard has run.
func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request, action string) {
	c, ok := s.passkeys(w)
	if !ok {
		return
	}
	if action == "" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		st, err := c.PasskeyStatus(r.Context())
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body passkeyBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var (
		out any
		err error
	)
	switch action {
	case "register/begin", "remove/begin":
		origin, ok := ceremonyOrigin(w, r)
		if !ok {
			return
		}
		if action == "register/begin" {
			out, err = c.PasskeyEnrollBegin(r.Context(), cerbapi.EnrollBeginArgs{Token: body.Token, Origin: origin, Label: body.Label})
		} else {
			out, err = c.PasskeyRemoveBegin(r.Context(), cerbapi.RemoveBeginArgs{Fingerprint: body.Fingerprint, Origin: origin})
		}
	case "register/finish":
		out, err = c.PasskeyEnrollFinish(r.Context(), cerbapi.EnrollFinishArgs{Ceremony: body.Ceremony, Attestation: body.Credential, Authorize: body.Authorize})
	case "remove/finish":
		err = c.PasskeyRemoveFinish(r.Context(), cerbapi.RemoveFinishArgs{Ceremony: body.Ceremony, Assertion: body.Credential})
		out = map[string]bool{"removed": err == nil}
	default:
		writeError(w, http.StatusNotFound, "unknown passkey action "+action)
		return
	}
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApprovalChallenge is POST /api/approvals/{id}/challenge: the
// options for the passkey assertion that approves an out-of-band request.
func (s *Server) handleApprovalChallenge(w http.ResponseWriter, r *http.Request, id string) {
	c, ok := s.passkeys(w)
	if !ok {
		return
	}
	origin, ok := ceremonyOrigin(w, r)
	if !ok {
		return
	}
	out, err := c.ApprovalChallenge(r.Context(), id, cerbapi.ApprovalChallengeArgs{Origin: origin})
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// passkeysAlert is the console header's line about out-of-band approval, or
// nil when the daemon cannot say. It is best effort: the header must not
// wait on it.
func (s *Server) passkeysAlert(ctx context.Context) map[string]any {
	c, ok := s.client.(passkeysClient)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := c.PasskeyStatus(ctx)
	if err != nil {
		return nil
	}
	summary, alert := st.Summary(time.Now())
	return map[string]any{"summary": summary, "alert": alert, "state": st.State, "keys": len(st.Keys)}
}
