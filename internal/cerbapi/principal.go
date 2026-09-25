package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// A Principal is who a request is from, as well as Cerberus can tell
// (docs/plans/live-systems-security-target.md, section 3).
//
// It is a label that chooses default policy. It is never approval, and
// nothing may treat it as approval: an agent with a shell can run the CLI as
// the user, and nothing local can prove a call came from a human. The kind,
// the channel and the client are what the caller says about itself. Only the
// uid is established by the kernel, and only where UIDVerified says so.
// Approval is the control, and it always goes out of band (I5).
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	// Via is the section 3 surface: who is on the other end — the CLI, an
	// MCP client, the web console, the monitor, a pipeline. CallerSurface is
	// the transport the request entered this process through; one socket
	// carries the CLI, MCP and the web console alike.
	Via string `json:"via"`
	// UID is the caller's user id. UIDVerified is true when the kernel said
	// so: the socket's peer credentials, or this process's own uid in the
	// CLI.
	UID         int  `json:"uid"`
	UIDVerified bool `json:"uid_verified"`
	// Client is the MCP clientInfo name/version, or a declared label.
	Client     string `json:"client,omitempty"`
	Session    string `json:"session,omitempty"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	// SelfReported marks Kind, Via and Client as the caller's own claim.
	SelfReported bool `json:"self_reported"`
}

// PrincipalKind is the coarse class of a principal.
type PrincipalKind string

const (
	PrincipalHuman      PrincipalKind = "human"
	PrincipalAgent      PrincipalKind = "agent"
	PrincipalAutomation PrincipalKind = "automation"
)

// Channels a principal arrives through (Principal.Via).
const (
	ViaCLI      = "cli"
	ViaMCPStdio = "mcp_stdio"
	ViaMCPHTTP  = "mcp_http"
	ViaWeb      = "web"
	ViaMonitor  = "monitor"
	ViaPipeline = "pipeline"
	ViaUnknown  = "unknown"
)

// PrincipalEnv is the marker an agent launcher sets (Decision 19). A CLI
// call with it set to "agent" is an agent whatever its terminal.
const PrincipalEnv = "CERBERUS_PRINCIPAL"

// PrincipalHeader carries a caller's claim about itself over the socket.
// The daemon records it as self-reported; it adds the uid itself.
const PrincipalHeader = "X-Cerberus-Principal"

type principalKey struct{}

// WithPrincipal marks ctx with p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal ctx carries.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// CLIClassification is how a CLI process classifies itself, with the inputs
// that decided it, so `cerberus whoami` can show its working.
type CLIClassification struct {
	Principal   Principal `json:"principal"`
	StdinTTY    bool      `json:"stdin_tty"`
	StdoutTTY   bool      `json:"stdout_tty"`
	Marker      string    `json:"cerberus_principal,omitempty"`
	Explanation string    `json:"explanation"`
}

// ClassifyCLI applies Decisions 9 and 19: a CLI call is human only from an
// interactive terminal — stdin and stdout both TTYs — with no
// CERBERUS_PRINCIPAL=agent marker. Anything else is an agent. It is a label
// for default policy, not a proof, and never approval.
func ClassifyCLI(stdinTTY, stdoutTTY bool, marker string) CLIClassification {
	c := CLIClassification{StdinTTY: stdinTTY, StdoutTTY: stdoutTTY, Marker: marker}
	p := Principal{Kind: PrincipalAgent, Via: ViaCLI, UID: os.Getuid(), UIDVerified: true, Client: "cerberus-cli", SelfReported: true}
	switch {
	case strings.EqualFold(strings.TrimSpace(marker), string(PrincipalAgent)):
		c.Explanation = PrincipalEnv + "=agent is set, so this is an agent whatever its terminal"
	case !stdinTTY || !stdoutTTY:
		c.Explanation = "stdin or stdout is not a terminal, so this is treated as an agent (Decision 9)"
	default:
		p.Kind = PrincipalHuman
		c.Explanation = "an interactive terminal with no " + PrincipalEnv + "=agent marker; this is a label for default policy, never approval"
	}
	c.Principal = p
	return c
}

// DetectCLI classifies this process as a CLI caller.
func DetectCLI() CLIClassification {
	return ClassifyCLI(isatty.IsTerminal(os.Stdin.Fd()), isatty.IsTerminal(os.Stdout.Fd()), os.Getenv(PrincipalEnv))
}

// requestPrincipal is the principal BeginRequest gives a request that entered
// through surface. A claim already on ctx — the CLI's own classification, or
// a socket caller's header — is kept, with the uid this process can vouch
// for. Without one, the strict reading applies: an unknown caller is an
// agent.
func requestPrincipal(ctx context.Context, surface CallerSurface) Principal {
	claim, claimed := PrincipalFrom(ctx)
	switch surface {
	case SurfaceInProcess:
		if !claimed {
			claim = DetectCLI().Principal
		}
		claim.UID, claim.UIDVerified = os.Getuid(), true
		return claim
	case SurfaceSocket:
		if !claimed {
			claim = Principal{Kind: PrincipalAgent, Via: ViaUnknown}
		}
		claim.SelfReported = true
		claim.UID, claim.UIDVerified = -1, false
		if peer, ok := peerFrom(ctx); ok && peer.err == nil {
			claim.UID, claim.UIDVerified = peer.uid, true
		}
		return claim
	case SurfaceWeb:
		// The label every console request starts with. Nothing the browser
		// sends is read as a claim. Only a signed-in session reaches an API
		// route, and the console then replaces this with that session's
		// principal (WebSessionPrincipal).
		return Principal{Kind: PrincipalHuman, Via: ViaWeb, UID: -1, SelfReported: true}
	case SurfaceMonitor:
		return Principal{Kind: PrincipalAutomation, Via: ViaMonitor, UID: os.Getuid(), UIDVerified: true, Client: "resource-monitor"}
	case SurfaceUnknown:
	}
	return Principal{Kind: PrincipalAgent, Via: ViaUnknown, UID: -1, SelfReported: true}
}

// WebSessionPrincipal is the principal of a console request made by a
// signed-in session (P2-2, Decision 20): a human, established by the
// one-time sign-in link rather than claimed, named by the session's public
// id. The uid stays unverified: the link proves possession of a 0600 file,
// which is not the kernel vouching for a peer.
func WebSessionPrincipal(session string) Principal {
	return Principal{Kind: PrincipalHuman, Via: ViaWeb, UID: -1, Session: session}
}

// pipelinePrincipal is the principal a pipeline's stages run as: automation
// acting for whoever started the run.
func pipelinePrincipal(ctx context.Context, id string) Principal {
	p := Principal{Kind: PrincipalAutomation, Via: ViaPipeline, UID: os.Getuid(), UIDVerified: true, Client: "pipeline:" + id}
	if caller, ok := PrincipalFrom(ctx); ok {
		p.OnBehalfOf = fmt.Sprintf("%s via %s", caller.Kind, caller.Via)
		if caller.Client != "" {
			p.OnBehalfOf += " (" + caller.Client + ")"
		}
	}
	return p
}

// principalClaim is what a caller says about itself on the wire. The uid is
// never part of it: the daemon reads that from the kernel.
type principalClaim struct {
	Kind       PrincipalKind `json:"kind"`
	Via        string        `json:"via"`
	Client     string        `json:"client,omitempty"`
	Session    string        `json:"session,omitempty"`
	OnBehalfOf string        `json:"on_behalf_of,omitempty"`
}

func setPrincipalHeader(h http.Header, p Principal) {
	data, err := json.Marshal(principalClaim{Kind: p.Kind, Via: p.Via, Client: p.Client, Session: p.Session, OnBehalfOf: p.OnBehalfOf})
	if err == nil {
		h.Set(PrincipalHeader, string(data))
	}
}

// principalFromHeader reads a socket caller's claim. A missing or malformed
// claim is no claim.
func principalFromHeader(h http.Header) (Principal, bool) {
	raw := h.Get(PrincipalHeader)
	if raw == "" || len(raw) > 1024 {
		return Principal{}, false
	}
	var c principalClaim
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Principal{}, false
	}
	// Only Cerberus itself is automation — the monitor, a pipeline's
	// stages — so a caller claiming it is read as an agent, as is any kind
	// outside the vocabulary.
	if c.Kind != PrincipalHuman {
		c.Kind = PrincipalAgent
	}
	if c.Via == "" {
		c.Via = ViaUnknown
	}
	return Principal{Kind: c.Kind, Via: clip(c.Via), Client: clip(c.Client), Session: clip(c.Session), OnBehalfOf: clip(c.OnBehalfOf), SelfReported: true}, true
}

func clip(s string) string {
	if len(s) > 128 {
		return s[:128]
	}
	return s
}
