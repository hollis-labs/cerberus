//go:build linux

package cerbapi

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/presence/presencetest"
	"github.com/hollis-labs/cerberus/internal/userpresence"
)

type peerPresence struct {
	t     *testing.T
	asked atomic.Int32
	allow bool
}

func (p *peerPresence) Verify(ctx context.Context, _ string) error {
	p.asked.Add(1)
	pid, uid, start, ok := userpresence.PeerSubject(ctx)
	wantStart, err := userpresence.ProcessStartTime(os.Getpid())
	if !ok || err != nil || pid != os.Getpid() || uid != os.Getuid() || start != wantStart {
		p.t.Errorf("wrong kernel peer: %d %d %d %v", pid, uid, start, ok)
		return userpresence.ErrUnavailable
	}
	if !p.allow {
		return userpresence.ErrRefused
	}
	return nil
}

func (*peerPresence) EnrollmentSetup() userpresence.Setup {
	return userpresence.Setup{Method: "polkit", Available: true, NextStep: "run `cerberus approvals enroll --browser=false` in your SSH terminal"}
}

type failPromptAudit struct {
	*audit.Memory
	kind string
}

func (s failPromptAudit) Write(r audit.Record) (audit.Record, error) {
	if r.Operation == "presence_prompt" && r.Kind == s.kind {
		return audit.Record{}, audit.ErrUnavailable
	}
	return s.Memory.Write(r)
}

func TestLinuxEnrollmentUsesSocketPeerAndFailsClosedOnAuditErrors(t *testing.T) {
	for _, failure := range []string{"", audit.KindIntent, audit.KindOutcome} {
		t.Run("audit_"+failure, func(t *testing.T) {
			prevB := ProcessBroker()
			broker, err := NewBroker(audit.NewMemory(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			SetBroker(broker)
			t.Cleanup(func() { SetBroker(prevB) })
			prevP, prevU := presencePoint.Load(), userPresencePoint.Load()
			t.Cleanup(func() { presencePoint.Store(prevP); userPresencePoint.Store(prevU) })
			sink := failPromptAudit{Memory: audit.NewMemory(), kind: failure}
			const origin = "https://cerberus.example.test"
			p := presence.New(t.TempDir(), sink, presence.Options{Origins: func() []string { return []string{origin} }})
			presencePoint.Store(p)
			v := &peerPresence{t: t, allow: true}
			SetUserPresenceWith(v, sink)
			path := startPeerSocket(t, NewInProcessClient(), nil)
			cli := NewSocketClient(path, WithPrincipalClaim(func(context.Context) Principal { return Principal{Kind: PrincipalHuman, Via: ViaCLI} }))
			web := NewSocketClient(path, WithPrincipalClaim(func(context.Context) Principal { return Principal{Kind: PrincipalHuman, Via: ViaWeb} }))
			token, digest := presence.NewEnrollToken()
			err = cli.PasskeyAllowEnrollment(context.Background(), EnrollAllowArgs{Digest: digest})
			if failure != "" {
				var coded *ExternalConnectorError
				if !errors.As(err, &coded) || coded.Code != ExternalConnectorAuditUnavailable {
					t.Fatalf("audit error: %v", err)
				}
				if _, err = web.PasskeyEnrollBegin(context.Background(), EnrollBeginArgs{Token: token, Origin: origin}); err == nil {
					t.Fatal("audit failure allowed token")
				}
				if failure == audit.KindIntent && v.asked.Load() != 0 {
					t.Fatal("prompt raised without audit intent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			st, err := cli.PasskeyStatus(context.Background())
			if err != nil || st.Enrollment == nil || st.Enrollment.Method != "polkit" {
				t.Fatalf("status: %+v, %v", st, err)
			}
			begin, err := web.PasskeyEnrollBegin(context.Background(), EnrollBeginArgs{Token: token, Origin: origin})
			if err != nil {
				t.Fatal(err)
			}
			key := presencetest.New(t, "cerberus.example.test", origin)
			if _, err = web.PasskeyEnrollFinish(context.Background(), EnrollFinishArgs{Ceremony: begin.Ceremony, Attestation: key.Register(t, optionsJSON(t, begin.Creation))}); err != nil {
				t.Fatal(err)
			}
			if _, err = web.PasskeyEnrollBegin(context.Background(), EnrollBeginArgs{Token: token, Origin: origin}); err == nil {
				t.Fatal("enrollment token replayed")
			}
			if len(p.Status().Keys) != 1 {
				t.Fatal("passkey not registered")
			}
		})
	}
}

func TestLinuxSetupGuidanceSurvivesTheSocketAndApprovalRef(t *testing.T) {
	prevB := ProcessBroker()
	broker, err := NewBroker(audit.NewMemory(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	SetBroker(broker)
	t.Cleanup(func() { SetBroker(prevB) })
	prevP, prevU := presencePoint.Load(), userPresencePoint.Load()
	t.Cleanup(func() { presencePoint.Store(prevP); userPresencePoint.Store(prevU) })
	presencePoint.Store(presence.New(t.TempDir(), audit.NewMemory(), presence.Options{}))
	SetUserPresence(userpresence.Refuse{Why: "install the Linux enrollment policy"})
	path := startPeerSocket(t, NewInProcessClient(), nil)
	cli := NewSocketClient(path)
	st, err := cli.PasskeyStatus(context.Background())
	if err != nil || st.Enrollment == nil || st.Enrollment.Available || !strings.Contains(st.Enrollment.NextStep, "enrollment-policy") {
		t.Fatalf("status: %+v %v", st, err)
	}
}
