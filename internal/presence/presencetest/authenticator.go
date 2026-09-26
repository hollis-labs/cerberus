// Package presencetest is a software passkey for tests: a P-256 key that
// answers WebAuthn ceremonies as a platform authenticator with user
// verification would, with "none" attestation.
package presencetest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

var b64 = base64.RawURLEncoding

// Authenticator is one passkey. UV is whether it reports user verification;
// Origin is the page origin the browser would write into client data.
type Authenticator struct {
	RPID    string
	Origin  string
	UV      bool
	Counter uint32
	key     *ecdsa.PrivateKey
	id      []byte
}

// New is a passkey for rpID used from origin, with user verification.
func New(t testing.TB, rpID, origin string) *Authenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &Authenticator{RPID: rpID, Origin: origin, UV: true, key: k, id: id}
}

func (a *Authenticator) flags(attested bool) byte {
	f := byte(0x01) // user present
	if a.UV {
		f |= 0x04
	}
	if attested {
		f |= 0x40
	}
	return f
}

func (a *Authenticator) authData(attested bool) []byte {
	rp := sha256.Sum256([]byte(a.RPID))
	out := append([]byte{}, rp[:]...)
	out = append(out, a.flags(attested))
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], a.Counter)
	out = append(out, c[:]...)
	if attested {
		out = append(out, make([]byte, 16)...) // aaguid
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(a.id))) //nolint:gosec // 16 bytes
		out = append(out, l[:]...)
		out = append(out, a.id...)
		x, y := a.key.X.FillBytes(make([]byte, 32)), a.key.Y.FillBytes(make([]byte, 32))
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		out = append(out, cose...)
	}
	return out
}

func (a *Authenticator) clientData(typ, challenge string) []byte {
	data, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return data
}

// ChallengeOf reads the challenge out of begin options.
func ChallengeOf(t testing.TB, opts json.RawMessage) string {
	t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &o); err != nil || o.PublicKey.Challenge == "" {
		t.Fatalf("no challenge in %s", opts)
	}
	return o.PublicKey.Challenge
}

// Register answers creation options with a new credential, as the browser's
// PublicKeyCredential.toJSON() would.
func (a *Authenticator) Register(t testing.TB, creation json.RawMessage) json.RawMessage {
	t.Helper()
	cd := a.clientData("webauthn.create", ChallengeOf(t, creation))
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(true)})
	out, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att)}, "clientExtensionResults": map[string]any{}})
	return out
}

// Assert signs request options' challenge, advancing the counter.
func (a *Authenticator) Assert(t testing.TB, opts json.RawMessage) json.RawMessage {
	t.Helper()
	a.Counter++
	cd := a.clientData("webauthn.get", ChallengeOf(t, opts))
	ad := a.authData(false)
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig)},
		"clientExtensionResults": map[string]any{}})
	return out
}
