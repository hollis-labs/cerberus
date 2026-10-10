package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/redact"
	gmcp "github.com/hollis-labs/libs/plugin-mcp/go-mcp/server"
)

func TestPipelineResolvedValuesProtectEveryNotificationField(t *testing.T) {
	const value = "synthetic-resolved-label-161803"
	ctx, scope := redact.EnsureScope(context.Background())
	scope.Add("run-value", value)
	var notifications bytes.Buffer
	ctx = gmcp.WithNotifier(ctx, func(n gmcp.Notification) {
		data, err := json.Marshal(n)
		if err != nil {
			t.Error(err)
			return
		}
		notifications.Write(data)
	})
	var logs bytes.Buffer
	p, err := New(value).Stage(value).Action(&mockAction{name: value, failExec: true}).Done().Build()
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewExecutor(slog.New(slog.NewTextHandler(&logs, nil))).Run(ctx, p, &domain.PipelineEnv{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for lane, text := range map[string]string{"notifications": notifications.String(), "slog": logs.String(), "result": string(data)} {
		if strings.Contains(text, value) || !strings.Contains(text, redact.Marker) {
			t.Fatalf("%s leaked or did not exercise label redaction: %s", lane, text)
		}
	}
}
