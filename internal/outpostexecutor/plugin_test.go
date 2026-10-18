package outpostexecutor

import (
	"context"
	"runtime"
	"testing"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

func TestRefusalsNeverReachNativeExecutor(t *testing.T) {
	p := &Plugin{Run: func(context.Context, string, []string) (outpost.Result, error) {
		t.Fatal("refused request executed")
		return outpost.Result{}, nil
	}}
	for _, r := range []outpost.Request{
		{Verb: "exec", Unit: "anything"},
		{Verb: "restart", Unit: "--all"},
		{Verb: "restart", Unit: "x;touch /tmp/should-not-exist"},
		{Verb: "restart", Unit: "outpost.service"},
		{Verb: "stop", Unit: "com.hollis-labs.cerberus.service"},
		{Verb: "logs", Unit: "example.service", Lines: 1001},
	} {
		if _, err := p.Execute(context.Background(), r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}

func TestSystemdShortNameCannotStopOutpost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd short names are Linux-specific")
	}
	p := &Plugin{Run: func(context.Context, string, []string) (outpost.Result, error) {
		t.Fatal("control plane stop reached native executor")
		return outpost.Result{}, nil
	}}
	if _, err := p.Execute(context.Background(), outpost.Request{Verb: "stop", Unit: "outpost"}); err == nil {
		t.Fatal("accepted short control plane name")
	}
}

func TestUnitIsPassedAsOneNativeArgument(t *testing.T) {
	cmd, args, err := Command("linux", 1000, outpost.Request{Verb: "restart", Unit: "example@instance.service"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "systemctl" || args[len(args)-1] != "example@instance.service" || args[len(args)-2] != "--" {
		t.Fatalf("unsafe command: %s %q", cmd, args)
	}
}

func TestOutputIsBoundedWhileDraining(t *testing.T) {
	b := &boundedOutput{}
	input := make([]byte, 512<<10)
	n, err := b.Write(input)
	if err != nil || n != len(input) || b.Len() != 256<<10 || !b.truncated {
		t.Fatalf("output bound failed")
	}
}
