// Command confirmdialog is the browser smoke's server for the console
// confirm dialog (P3-3b, CERB-GAP-886). It runs, in one process under a
// scratch HOME, a daemon with enforcement installed and a policy that asks
// for a confirmation on everything (which the product does not do until
// P3-7), and the real console talking to it over its socket; it serves a
// fresh one-time sign-in link on 127.0.0.1:4798/login-url. It refuses a
// HOME outside /tmp. run.sh builds and drives it; smoke.mjs is the browser
// side.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/loopback"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/target"
	"github.com/hollis-labs/cerberus/internal/webui"
)

type enforceAll struct{}

func (enforceAll) Enforced(policy.Request) bool { return true }

type approveAll struct{}

func (approveAll) GlobalPosture() string { return policy.PostureSecure }
func (approveAll) Authorize(req policy.Request) policy.Result {
	if req.Principal.Kind == "agent" {
		// An agent is denied, for the circuit breaker's flow (P5-c).
		return policy.Result{Decision: policy.Deny, WouldBlock: true, Snapshot: "smoke",
			Matched: []policy.Match{{Rule: "smoke.no-agents", Decision: policy.Deny, Reason: "the smoke denies agents"}}}
	}
	return policy.Result{Decision: policy.Approve, WouldBlock: true, Snapshot: "smoke",
		Matched: []policy.Match{{Rule: "smoke.confirm", Decision: policy.Approve, Reason: "the smoke asks for a confirmation"}}}
}

// File carries the circuit breaker: two real denials in ten minutes.
func (approveAll) File() policy.File {
	return policy.File{Version: policy.FileVersion, CircuitBreaker: &policy.CircuitBreaker{Denials: 2, Window: 10 * time.Minute}}
}

type noSecrets struct{}

func (noSecrets) Get(context.Context, string, string) (string, error) { return "", nil }
func (noSecrets) Set(context.Context, string, string, string) error   { return nil }
func (noSecrets) Delete(context.Context, string, string) error        { return nil }

func main() {
	home := os.Getenv("HOME")
	if !strings.HasPrefix(home, "/tmp/") {
		fmt.Fprintln(os.Stderr, "refusing: HOME must be a scratch directory under /tmp")
		os.Exit(2)
	}
	dir := filepath.Join(home, ".cerberus")
	cfgPath := filepath.Join(dir, "config.yaml")
	repo := filepath.Join(home, "site")
	must(os.MkdirAll(filepath.Join(repo, ".vercel"), 0o750))                                //nolint:gosec // a path under the scratch HOME checked above
	must(os.WriteFile(filepath.Join(repo, ".vercel", "project.json"), []byte(`{}`), 0o600)) //nolint:gosec // a path under the scratch HOME checked above
	must(os.MkdirAll(filepath.Join(home, "webdir"), 0o750))                                 //nolint:gosec // a path under the scratch HOME checked above
	cfg := fmt.Sprintf(`version: 2
resources:
  - id: web
    type: process
    connector: local
    env: dev
    owner: self
    admin: self
    config:
      dir: %s
      command: ["/bin/sleep", "300"]
  - id: prod-api
    type: process
    connector: local
    env: prod
    owner: self
    admin: self
    config:
      dir: %s
      command: ["/bin/sleep", "300"]
`, filepath.Join(home, "webdir"), filepath.Join(home, "webdir"))
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) { //nolint:gosec // a path under the scratch HOME checked above
		must(os.MkdirAll(dir, 0o700))                   //nolint:gosec // a path under the scratch HOME checked above
		must(os.WriteFile(cfgPath, []byte(cfg), 0o600)) //nolint:gosec // a path under the scratch HOME checked above
	}
	if _, err := os.Stat(filepath.Join(dir, "infra.yaml")); os.IsNotExist(err) { //nolint:gosec // a path under the scratch HOME checked above
		must(infra.SaveState(cfgPath, &infra.State{Version: 1, Profiles: []infra.DeploymentProfile{{
			ID: "site", Name: "Site", Provider: "vercel", RepoPath: repo, VercelScope: "team",
			DeployCommand: "echo deployed > " + filepath.Join(home, "deployed.txt"),
			Env:           target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
		}}}))
	}

	sink := audit.NewMemory()
	cerbapi.SetPolicyDecisionPoint(approveAll{})
	cerbapi.SetEnforcement(enforceAll{})
	broker, err := cerbapi.NewBroker(sink, filepath.Join(dir, "approvals"))
	must(err)
	cerbapi.SetBroker(broker)
	// Passkeys (P3-4b), for the break-glass flow on a protected target.
	origins := []string{"http://localhost:4799", "http://127.0.0.1:4799"}
	passkeys := presence.New(filepath.Join(dir, "approvals", "passkeys"), sink, presence.Options{Origins: func() []string { return origins }})
	cerbapi.SetPresence(passkeys)
	// The emergency brake (§12), engaged and lifted from the console.
	cerbapi.SetBrakes(&cerbapi.Brakes{Store: brake.Store{Dir: filepath.Join(dir, "brakes")}, Sink: sink})

	runtime := cerbapi.NewResourceRuntimeService(sink, cerbapi.WithResourceRuntimeConfigPath(cfgPath))
	inProc := cerbapi.NewInProcessClient(cerbapi.WithConfigPath(cfgPath), cerbapi.WithResourceRuntimeService(runtime),
		cerbapi.WithDeploySecrets(noSecrets{}), cerbapi.WithInProcessAudit(sink))
	sock := filepath.Join(home, "s.sock")
	_ = os.Remove(sock) //nolint:gosec // a path under the scratch HOME checked above
	go func() { must(cerbapi.NewSocketServer(inProc, sock).Run(context.Background())) }()
	for i := 0; i < 100; i++ {
		if c, dialErr := net.Dial("unix", sock); dialErr == nil { //nolint:gosec // the smoke's own socket under the scratch HOME
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	web, err := webui.New(cerbapi.NewSocketClient(sock), sink, cfgPath, noSecrets{}, slog.Default())
	must(err)
	addr := "127.0.0.1:4799"
	ln, err := net.Listen("tcp", addr)
	must(err)
	guard, err := loopback.NewGuardForAddr(addr, ln.Addr())
	must(err)
	base := webui.ConsoleBaseURL(addr)
	go func() { _ = (&http.Server{Handler: web.Handler(guard), ReadHeaderTimeout: 5 * time.Second}).Serve(ln) }() //nolint:gosec // smoke
	// A fresh one-time sign-in link on demand, for the driver.
	ctl := http.NewServeMux()
	ctl.HandleFunc("/login-url", func(w http.ResponseWriter, _ *http.Request) {
		login, err := web.LoginURL(base)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(login))
	})
	// The CLI's half of break glass on a protected target (the CLI itself
	// is covered by its Go tests): ask, then retry once the console has
	// approved it with a passkey.
	cli := cerbapi.NewSocketClient(sock, cerbapi.WithPrincipalClaim(func(context.Context) cerbapi.Principal {
		return cerbapi.Principal{Kind: cerbapi.PrincipalHuman, Via: cerbapi.ViaCLI, Client: "cerberus-cli"}
	}))
	ctl.HandleFunc("/enroll-token", func(w http.ResponseWriter, _ *http.Request) {
		token, digest := presence.NewEnrollToken()
		if err := passkeys.AllowEnrollment(digest); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(token))
	})
	breakGlass := func(w http.ResponseWriter, r *http.Request) {
		opts := []cerbapi.MutationOption{cerbapi.WithAcknowledged(true), cerbapi.WithBreakGlass("prod is down", "prod-api")}
		if id := r.URL.Query().Get("id"); id != "" {
			opts = append(opts, cerbapi.WithApprovalID(id))
		}
		out, err := cli.StopResource(r.Context(), "prod-api", opts...)
		result := map[string]any{"result": out}
		if err != nil {
			result["error"] = err.Error()
			var coded *cerbapi.ExternalConnectorError
			if errors.As(err, &coded) && coded.Approval != nil {
				result["approval_id"] = coded.Approval.ID
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
	ctl.HandleFunc("/break-glass", breakGlass)
	// An agent's stop, which policy denies: twice trips the breaker.
	agent := cerbapi.NewSocketClient(sock, cerbapi.WithPrincipalClaim(func(context.Context) cerbapi.Principal {
		return cerbapi.Principal{Kind: cerbapi.PrincipalAgent, Via: cerbapi.ViaMCPStdio, Client: "smoke-agent", Session: "smoke-1"}
	}))
	ctl.HandleFunc("/agent-stop", func(w http.ResponseWriter, r *http.Request) {
		_, err := agent.StopResource(r.Context(), "web", cerbapi.WithAcknowledged(true))
		result := map[string]any{}
		if err != nil {
			result["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	// A person's stop from the CLI, which a lockdown refuses.
	ctl.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		_, err := cli.StopResource(r.Context(), "web", cerbapi.WithAcknowledged(true))
		result := map[string]any{}
		if err != nil {
			result["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	ctl.HandleFunc("/follow-ups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(broker.UnackedBreakGlass())
	})
	fmt.Println("READY")
	must((&http.Server{Addr: "127.0.0.1:4798", Handler: ctl, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()) //nolint:gosec // smoke
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
