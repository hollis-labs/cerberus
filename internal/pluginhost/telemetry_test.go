package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// The tap forwards every complete line, and hands each one written while a
// call runs to that call's collector, both through the plugin's redactor.
func TestStderrTapAttributesLinesToTheRunningCall(t *testing.T) {
	var out bytes.Buffer
	tap := newStderrTap()
	w := tap.wrap(&out)
	tap.setRedactor(redact.New("tok-SENTINEL-19"))

	_, _ = w.Write([]byte("before any call\n"))
	_, c := WithTelemetry(context.Background())
	detach := tap.attach(c)
	_, _ = w.Write([]byte("fetching with tok-SENTINEL-19\npartial"))
	_, _ = w.Write([]byte(" line\n"))
	detach()
	_, _ = w.Write([]byte("after the call\n"))

	if !strings.Contains(out.String(), "before any call") || !strings.Contains(out.String(), "after the call") {
		t.Fatalf("stderr not forwarded: %q", out.String())
	}
	got := c.Snapshot()
	if len(got.Stderr) != 2 || got.Stderr[1] != "partial line" || got.SharedStderr {
		t.Fatalf("lines = %#v", got)
	}
	if strings.Contains(strings.Join(got.Stderr, "\n"), "tok-SENTINEL-19") {
		t.Fatalf("a resolved credential reached the record: %v", got.Stderr)
	}
}

// Two calls in flight both get a line, marked shared; bounds cap the count.
func TestStderrTapSharesAndBounds(t *testing.T) {
	tap := newStderrTap()
	w := tap.wrap(nil)
	_, a := WithTelemetry(context.Background())
	_, b := WithTelemetry(context.Background())
	defer tap.attach(a)()
	defer tap.attach(b)()
	for i := 0; i < maxStderrLines+10; i++ {
		_, _ = fmt.Fprintf(w, "line %d\n", i)
	}
	for _, c := range []*Collector{a, b} {
		got := c.Snapshot()
		if !got.SharedStderr || len(got.Stderr) != maxStderrLines || !got.Truncated {
			t.Fatalf("shared=%v lines=%d truncated=%v", got.SharedStderr, len(got.Stderr), got.Truncated)
		}
	}
}

// Events are bounded in count and size, redacted, and never exceed the
// record's byte budget.
func TestCollectorBoundsEvents(t *testing.T) {
	_, c := WithTelemetry(context.Background())
	var events []plugin.TelemetryEvent
	for i := 0; i < maxTelemetryEvents+5; i++ {
		events = append(events, plugin.TelemetryEvent{Kind: "step", Message: strings.Repeat("x", maxTelemetryField*2)})
	}
	c.addEvents(events, redact.New())
	got := c.Snapshot()
	if !got.Truncated || len(got.Events) > maxTelemetryEvents {
		t.Fatalf("events=%d truncated=%v", len(got.Events), got.Truncated)
	}
	data, _ := json.Marshal(got)
	if len(data) > maxTelemetryBytes*2 {
		t.Fatalf("telemetry is %d bytes", len(data))
	}
}

// The host strips telemetry from a result before any caller sees it.
func TestSplitTelemetry(t *testing.T) {
	content, err := plugin.AttachTelemetry(json.RawMessage(`{"ok":true}`), plugin.TelemetryEvent{Kind: "step", Message: "scaled"})
	if err != nil {
		t.Fatal(err)
	}
	stripped, events := plugin.SplitTelemetry(content)
	if strings.Contains(string(stripped), plugin.TelemetryKey) || len(events) != 1 || events[0].Message != "scaled" {
		t.Fatalf("stripped %s events %v", stripped, events)
	}
	if _, events := plugin.SplitTelemetry(json.RawMessage(`[1,2]`)); events != nil {
		t.Fatal("a non-object result grew telemetry")
	}
	if _, events := plugin.SplitTelemetry(json.RawMessage(`{"cerberus_telemetry":"nope"}`)); len(events) != 1 || events[0].Kind != "malformed" {
		t.Fatalf("malformed telemetry: %v", events)
	}
}
