package mcp

import (
	"context"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// HostMonitorTools are the monitoring reads (Hosts E): per-host status with
// reasons, retained samples, and what the monitor is doing. Facts only: they
// carry no score and no order beyond name or time.
func HostMonitorTools(client cerbapi.HostMonitorClient) []Tool {
	text := func(v any, err error) (any, error) {
		if err != nil {
			return "", err
		}
		data, merr := redact.MarshalIndent(v, "", "  ")
		return string(data), merr
	}
	return []Tool{
		contractTool(Tool{
			Name: "cerberus_host_status",
			Description: "Health of one host, or of every host when no name is given: online, degraded, offline or unknown, with reasons in plain words (disk_low, memory_low, load_high, probe_slow, unreachable, stale, ...), since when, the last good measurement, " +
				"and the thresholds the status is derived from. A measurement older than three probe intervals reads unknown with its age. Facts only: not ranked, nothing reserved.",
			InputSchema: objectSchema(map[string]interface{}{"name": map[string]interface{}{"type": "string", "description": "Host name; omit for every host."}}),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				if name := stringArg(args, "name"); name != "" {
					return text(client.HostStatus(ctx, name))
				}
				return text(client.AllHostStatus(ctx))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_host_samples",
			Description: "A host's retained measurements (load, memory, disk, uptime, probe time) in time order, raw or as 5m, 1h or 1d rollups. since is an RFC 3339 time or a duration such as 6h; at most limit samples (default 500, the newest).",
			InputSchema: objectSchema(map[string]interface{}{
				"name":  map[string]interface{}{"type": "string", "description": "Host name."},
				"since": map[string]interface{}{"type": "string", "description": "RFC 3339 time or a duration such as 6h."},
				"until": map[string]interface{}{"type": "string", "description": "RFC 3339 time or a duration ago."},
				"limit": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 5000},
				"grain": map[string]interface{}{"type": "string", "enum": []string{"raw", "5m", "1h", "1d"}, "description": "raw (default) returns measurements; 5m, 1h and 1d return rollups: per bucket the sample count and min, mean and max of load, memory available, free disk and probe time."},
			}, "name"),
			Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
				now := time.Now()
				since, err := cerbapi.ParseSamplesTime(stringArg(args, "since"), now)
				if err != nil {
					return "", err
				}
				until, err := cerbapi.ParseSamplesTime(stringArg(args, "until"), now)
				if err != nil {
					return "", err
				}
				limit := 0
				if n, ok := args["limit"].(float64); ok {
					limit = int(n)
				}
				q := cerbapi.SamplesQuery{Since: since, Until: until, Limit: limit}
				if g := stringArg(args, "grain"); g != "" && g != "raw" {
					return text(client.HostRollups(ctx, stringArg(args, "name"), g, q))
				}
				return text(client.HostSamples(ctx, stringArg(args, "name"), q))
			},
		}),
		contractTool(Tool{
			Name:        "cerberus_host_monitor",
			Description: "What the host monitor is doing: whether it is on, its effective settings (intervals, jitter, backoff, concurrency, thresholds) and each monitored host's interval, last attempt and next probe.",
			InputSchema: objectSchema(map[string]interface{}{}),
			Handler: func(ctx context.Context, _ map[string]interface{}) (any, error) {
				return text(client.MonitorState(ctx))
			},
		}),
	}
}

func init() {
	for _, op := range []string{cerbapi.OpHostStatus, cerbapi.OpHostSamples, cerbapi.OpHostMonitor} {
		toolOperations["cerberus_"+op] = opRef{cerbapi.HostDefinition, op}
	}
}

// HostEventTools is the poll side of the status event stream: MCP has no
// streaming, so an agent resumes by id.
func HostEventTools(client cerbapi.HostEventsClient) []Tool {
	return []Tool{contractTool(Tool{
		Name: "cerberus_host_events",
		Description: "Host status-change events (status_changed: from, to, reason codes) after an event id, oldest first. Resume from last_id of the previous call; a gap means older events were pruned. " +
			"Events carry status and reason codes only. Use cerberus_host_status for the details.",
		InputSchema: objectSchema(map[string]interface{}{
			"after_id": map[string]interface{}{"type": "integer", "minimum": 0, "description": "Return events after this id (0 for the oldest kept)."},
			"host":     map[string]interface{}{"type": "string", "description": "Only this host."},
			"limit":    map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 1000},
		}),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			q := cerbapi.EventsQuery{Host: stringArg(args, "host")}
			if n, ok := args["after_id"].(float64); ok {
				q.AfterID = int64(n)
			}
			if n, ok := args["limit"].(float64); ok {
				q.Limit = int(n)
			}
			res, err := client.HostEvents(ctx, q)
			if err != nil {
				return "", err
			}
			data, merr := redact.MarshalIndent(res, "", "  ")
			return string(data), merr
		},
	})}
}

func init() {
	toolOperations["cerberus_"+cerbapi.OpHostEvents] = opRef{cerbapi.HostDefinition, cerbapi.OpHostEvents}
}
