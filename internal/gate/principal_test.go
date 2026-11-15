package gate

import (
	"context"
	"os"
	"testing"
)

func TestRequestPrincipalPerSurface(t *testing.T) {
	human := WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI})
	// The web console reads nothing the browser claims.
	if p, _ := PrincipalFrom(BeginRequest(human, SurfaceWeb)); p.Kind != PrincipalHuman || p.Via != ViaWeb || p.UIDVerified {
		t.Errorf("web: %+v", p)
	}
	fakeAgent := WithPrincipal(context.Background(), Principal{Kind: PrincipalAgent, Via: ViaMCPStdio})
	if p, _ := PrincipalFrom(BeginRequest(fakeAgent, SurfaceWeb)); p.Via != ViaWeb {
		t.Errorf("web took a claim: %+v", p)
	}
	// A socket caller without a claim is an agent; with no peer credentials
	// read here, its uid is unknown.
	if p, _ := PrincipalFrom(BeginRequest(context.Background(), SurfaceSocket)); p.Kind != PrincipalAgent || p.Via != ViaUnknown || p.UID != -1 || p.UIDVerified || !p.SelfReported {
		t.Errorf("socket, no claim: %+v", p)
	}
	// An in-process caller with no claim is read strictly: an agent at the
	// CLI, with this process's uid.
	if p, _ := PrincipalFrom(BeginRequest(context.Background(), SurfaceInProcess)); p.Kind != PrincipalAgent || p.Via != ViaCLI || p.UID != os.Getuid() || !p.UIDVerified {
		t.Errorf("in-process, no claim: %+v", p)
	}
	if p, _ := PrincipalFrom(BeginRequest(context.Background(), SurfaceMonitor)); p.Kind != PrincipalAutomation || p.SelfReported {
		t.Errorf("monitor: %+v", p)
	}
	if p, _ := PrincipalFrom(BeginRequest(human, SurfaceInProcess)); p.Kind != PrincipalHuman || p.UID != os.Getuid() || !p.UIDVerified {
		t.Errorf("in-process with a CLI claim: %+v", p)
	}
	if p, _ := PrincipalFrom(BeginRequest(context.Background(), SurfaceUnknown)); p.Kind != PrincipalAgent {
		t.Errorf("unknown: %+v", p)
	}
	run := PipelinePrincipal(WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli"}), "release")
	if run.Kind != PrincipalAutomation || run.Via != ViaPipeline || run.Client != "pipeline:release" || run.OnBehalfOf != "human via cli (cerberus-cli)" {
		t.Errorf("pipeline: %+v", run)
	}
}
