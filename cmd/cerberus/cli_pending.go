package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The old verbs whose backend no served lane carries yet (slice 7c's plan): each exists with
// old's use line, short help and flags, so a script that runs it parses as it did, and answers
// "pending (slice N)" with exit 1, having done nothing. The flags mirror main's
// (`cerberus <path> --help` on main); the parity test checks every one of them.

// The slices that own the backends these verbs wait for. Slice 7d (CW-20261115-0089) takes the
// ones no slice owned: the policy point's CLI routes (policy, posture), the audit chain's writes
// (audit prune, reanchor), the server's view of the caller (whoami) and data access. Slice 8b
// owns the rest: host writes, components, pipelines, and every other backend not served yet.
const (
	slice7d = "slice 7d"
	slice8b = "slice 8b"
)

// pendingOwner is the slice a path waits for, by the pendingWhy key that names it.
var pendingOwner = map[string]string{
	"audit": slice7d, "data": slice7d, "policy": slice7d, "posture": slice7d, "whoami": slice7d,
}

// pendingSlice is the slice path waits for.
func pendingSlice(path string) string {
	if s, ok := pendingOwner[whyKey(path)]; ok {
		return s
	}
	return slice8b
}

// flagSpec is one old flag: name, shorthand, type (bool, string, int, float, duration,
// stringArray), default as old printed it, and usage.
type flagSpec struct{ name, short, kind, def, usage string }

// pendingSpec is one old command path. A group carries its flags as persistent flags, as old's
// did, and runs nothing itself.
type pendingSpec struct {
	path, use, short string
	group            bool
	flags            []flagSpec
}

// pendingWhy is why a path is pending, by the longest prefix that names it.
var pendingWhy = map[string]string{
	"audit":               "pruning and reanchoring write the audit chain, which only the serving Cerberus writes, and no served lane does either yet (a second, in-process writer would race it)",
	"component":           "the component inventory has no route on the serving Cerberus yet",
	"connectors describe": "the definitions are served (GET /api/connectors), but the live field needs the serving Cerberus's live-connector read, which no route serves yet; `cerberus connectors exec <id> <op>` runs the connector's operations",
	"data":                "the data-access route (old POST /data/v1/query) is not served yet",
	"host":                "this host verb has no route yet; host list, get, probe, status, samples, monitor and events are served",
	"pipeline":            "no pipeline route is served yet",
	"policy":              "the policy point runs inside the serving Cerberus, and no route serves policy to the CLI yet",
	"portfolio":           "the portfolio health projection has no route yet",
	"posture":             "the posture is the policy's, and no route serves it to the CLI yet",
	"schedule":            "the scheduling core is off and no schedule route is served",
	"whoami":              "how the serving Cerberus classifies this CLI (it calls with its API key, an agent via api) needs a route not served yet",
}

// pendingError is a pending verb's answer.
func pendingError(path string) error {
	return fmt.Errorf("cerberus %s: pending (%s): %s; nothing was done", path, pendingSlice(path), pendingWhy[whyKey(path)])
}

// whyKey is the longest pendingWhy key that names path.
func whyKey(path string) string {
	best := ""
	for p := range pendingWhy {
		if (path == p || strings.HasPrefix(path, p+" ")) && len(p) > len(best) {
			best = p
		}
	}
	return best
}

// addPendingCommands adds every pending path under root, beside the commands already there
// (a pending host verb joins the served ones).
func addPendingCommands(root *cobra.Command, specs []pendingSpec) error {
	for _, s := range specs {
		parent := root
		words := strings.Fields(s.path)
		for _, w := range words[:len(words)-1] {
			next := childNamed(parent, w)
			if next == nil {
				return fmt.Errorf("pending %q: no command %q under %q", s.path, w, parent.CommandPath())
			}
			parent = next
		}
		cmd := childNamed(parent, words[len(words)-1])
		if cmd == nil {
			cmd = &cobra.Command{Use: s.use, Short: s.short}
			parent.AddCommand(cmd)
		}
		flags := cmd.Flags()
		if s.group {
			flags = cmd.PersistentFlags()
		} else {
			path := s.path
			cmd.Long = s.short + "\n\nPending (" + pendingSlice(path) + "): " + pendingWhy[whyKey(path)] + "."
			cmd.Args = cobra.ArbitraryArgs
			cmd.RunE = func(*cobra.Command, []string) error { return pendingError(path) }
		}
		for _, f := range s.flags {
			if err := addFlagSpec(flags, f); err != nil {
				return fmt.Errorf("pending %q: %w", s.path, err)
			}
		}
	}
	return nil
}

// addFlagSpec defines f on fs with old's type and default.
func addFlagSpec(fs *pflag.FlagSet, f flagSpec) error {
	switch f.kind {
	case "bool":
		fs.BoolP(f.name, f.short, f.def == "true", f.usage)
	case "string":
		fs.StringP(f.name, f.short, f.def, f.usage)
	case "int":
		n := 0
		if f.def != "" {
			if _, err := fmt.Sscan(f.def, &n); err != nil {
				return fmt.Errorf("flag %s default %q: %w", f.name, f.def, err)
			}
		}
		fs.IntP(f.name, f.short, n, f.usage)
	case "float":
		fs.Float64P(f.name, f.short, 0, f.usage)
	case "duration":
		var d time.Duration
		if f.def != "" {
			var err error
			if d, err = time.ParseDuration(f.def); err != nil {
				return fmt.Errorf("flag %s default %q: %w", f.name, f.def, err)
			}
		}
		fs.DurationP(f.name, f.short, d, f.usage)
	case "stringArray":
		fs.StringArrayP(f.name, f.short, nil, f.usage)
	default:
		return fmt.Errorf("flag %s: unknown type %q", f.name, f.kind)
	}
	return nil
}

var pendingSpecs = []pendingSpec{
	{path: "audit prune", use: "prune --before <date>", short: "Remove whole audit month files older than a date (interactive only)", group: false, flags: []flagSpec{
		{"before", "", "string", "", "remove month files wholly before this date (required)"},
	}},
	{path: "audit reanchor", use: "reanchor", short: "Acknowledge an audit chain that does not verify (interactive only)", group: false, flags: []flagSpec{}},
	{path: "component", use: "component", short: "Spare and installed parts recorded in hosts.yaml (read-only)", group: true, flags: []flagSpec{
		{"json", "", "bool", "", "print JSON"},
		{"output", "o", "string", "text", "output format: text|json"},
	}},
	{path: "component list", use: "list", short: "List components (RAM, disks, CPUs, other), installed and spare", group: false, flags: []flagSpec{
		{"host", "", "string", "", "only components located on this host"},
		{"kind", "", "string", "", "ram|disk|cpu|other"},
		{"spare", "", "bool", "", "only spare components"},
	}},
	{path: "component get", use: "get <id>", short: "Show one component", group: false, flags: []flagSpec{}},
	{path: "component history", use: "history <id>", short: "Show the change history of a part, newest first", group: false, flags: []flagSpec{
		{"limit", "", "int", "100", "most entries"},
	}},
	{path: "component create", use: "create --id ID --kind ram|disk|cpu|other (--spare | --host H --slot S) [--size 16GiB ...]", short: "Add a spare or installed part", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"host", "", "string", "", "host the part is installed in"},
		{"id", "", "string", "", "component id"},
		{"kind", "", "string", "", "ram|disk|cpu|other"},
		{"notes", "", "string", "", "notes"},
		{"patch", "", "string", "", "JSON object of further fields"},
		{"patch-file", "", "string", "", "file with a JSON object of further fields (- for stdin)"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
		{"serial", "", "string", "", "serial number"},
		{"size", "", "string", "", "size, such as 16GiB or 2TB"},
		{"slot", "", "string", "", "slot on that host"},
		{"spare", "", "bool", "", "the part is a spare"},
		{"speed", "", "string", "", "speed, such as 3200"},
		{"type", "", "string", "", "type, such as DDR4 or hdd"},
	}},
	{path: "component update", use: "update <id> [--size 16GiB] [--type ...] [--serial ...] [--notes ...] [--patch JSON]", short: "Change a part's fields (not its location: use move)", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"kind", "", "string", "", "ram|disk|cpu|other"},
		{"notes", "", "string", "", "notes"},
		{"patch", "", "string", "", "JSON object of further fields"},
		{"patch-file", "", "string", "", "file with a JSON object of further fields (- for stdin)"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
		{"serial", "", "string", "", "serial number"},
		{"size", "", "string", "", "size, such as 16GiB or 2TB"},
		{"speed", "", "string", "", "speed, such as 3200"},
		{"type", "", "string", "", "type, such as DDR4 or hdd"},
	}},
	{path: "component move", use: "move <id> (--spare | --host H --slot S)", short: "Move a part to a slot on a host, or to the spares", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"host", "", "string", "", "host the part is installed in"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
		{"slot", "", "string", "", "slot on that host"},
		{"spare", "", "bool", "", "the part is a spare"},
	}},
	{path: "component remove", use: "remove <id>", short: "Delete a part and free the slot it held", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
	}},
	{path: "connectors describe", use: "describe <connector-id>", short: "Show detailed connector discovery metadata", group: false, flags: []flagSpec{}},
	{path: "data", use: "data", short: "Session-authorized named data projections", group: true, flags: []flagSpec{}},
	{path: "data query", use: "query", short: "Read a JSON projection request from stdin; token comes from a private file", group: false, flags: []flagSpec{
		{"token-file", "", "string", "", "Private Tether session token file (never the token itself)"},
	}},
	{path: "host capacity", use: "capacity", short: "List the hosts that satisfy constraints, from recorded facts", group: false, flags: []flagSpec{
		{"disk-type", "", "stringArray", "", "a recorded disk of this type: ssd|hdd|nvme|emmc|usb|other (repeatable)"},
		{"explain", "", "bool", "", "also list excluded hosts with the reason for each"},
		{"free-memory-slots", "", "int", "", "at least this many empty memory slots"},
		{"free-slot", "", "stringArray", "", "free storage slots by kind, kind=N, such as nvme_m2=1 (repeatable)"},
		{"include-offline", "", "bool", "", "judge offline hosts on their last measurement"},
		{"label", "", "stringArray", "", "label that must match, key=value (repeatable)"},
		{"max-age", "", "duration", "", "oldest probe to judge on (default 5m)"},
		{"min-cpu", "", "int", "", "minimum probed logical cores"},
		{"min-disk", "", "string", "", "minimum free disk on the data mount, such as 50GB"},
		{"min-disk-size", "", "string", "", "a recorded disk at least this large, such as 500GB"},
		{"min-memory", "", "string", "", "minimum available memory, such as 4GiB"},
		{"min-memory-total", "", "string", "", "installed memory at least this, such as 16GiB"},
		{"min-memory-upgrade", "", "string", "", "room to add at least this much memory (ceiling minus installed)"},
		{"provider", "", "string", "", "provider"},
		{"recent-max-load", "", "float", "", "load per core must stay at or below this over the window"},
		{"recent-min-disk", "", "string", "", "free disk must stay at or above this over the window"},
		{"recent-min-memory", "", "string", "", "memory available must stay at or above this over the window"},
		{"recent-window", "", "duration", "", "judge headroom on the worst sample in this window (default 15m when a recent limit is given)"},
		{"role", "", "string", "", "declared role"},
		{"status", "", "stringArray", "", "monitor status must be one of: online|degraded|offline|unknown (repeatable)"},
		{"tool", "", "stringArray", "", "probed tool that must be installed: docker|tmux|tailscale|caddy (repeatable)"},
	}},
	{path: "host history", use: "history <name>", short: "Show the change history of a host, newest first", group: false, flags: []flagSpec{
		{"limit", "", "int", "100", "most entries"},
	}},
	{path: "host set", use: "set <name>", short: "Change a host in hosts.yaml (validated, backed up, recorded in the history)", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"create", "", "bool", "", "create the host when it does not exist"},
		{"label", "", "stringArray", "", "set a label, key=value (repeatable)"},
		{"owner", "", "string", "", "set the owner"},
		{"patch", "", "string", "", "JSON merge patch of the host record"},
		{"patch-file", "", "string", "", "file with the JSON merge patch (- for stdin)"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
		{"role", "", "string", "", "set the role"},
		{"unlabel", "", "stringArray", "", "remove a label by key (repeatable)"},
	}},
	{path: "host remove", use: "remove <name>", short: "Remove a host from hosts.yaml (refused while components are located on it)", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
	}},
	{path: "host inventory", use: "inventory <name>", short: "Collect a host's hardware over ssh and record it (keeps notes, storage_slots, vendor ceilings, components)", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this change to hosts.yaml (required)"},
		{"approval-id", "", "string", "", "out-of-band approval bound to this change"},
		{"dry-run", "", "bool", "", "collect and show the change; write nothing"},
		{"revision", "", "string", "", "hosts.yaml revision this change is based on (default: the current one)"},
	}},
	{path: "host plan", use: "plan <host> <units|logs|start|stop|restart> [unit]", short: "Plan or execute a scoped Outpost unit operation through Cerberus policy", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge the requested operation"},
		{"approval-id", "", "string", "", "out-of-band approval bound to the plan"},
		{"lines", "", "int", "", "log line limit (logs only; zero uses 100)"},
	}},
	{path: "host act", use: "act <host> <units|logs|start|stop|restart> [unit]", short: "Plan or execute a scoped Outpost unit operation through Cerberus policy", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge the requested operation"},
		{"approval-id", "", "string", "", "out-of-band approval bound to the plan"},
		{"lines", "", "int", "", "log line limit (logs only; zero uses 100)"},
	}},
	{path: "pipeline", use: "pipeline", short: "Pipeline management", group: true, flags: []flagSpec{}},
	{path: "pipeline list", use: "list", short: "List available pipelines", group: false, flags: []flagSpec{}},
	{path: "pipeline show", use: "show <pipeline-id>", short: "Show pipeline details", group: false, flags: []flagSpec{}},
	{path: "pipeline plan", use: "plan <pipeline-id>", short: "Show the plan an approval of a pipeline run would bind to", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "plan the run as it would be sent with --ack"},
	}},
	{path: "pipeline run", use: "run <pipeline-id>", short: "Run a pipeline", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge the run; a pipeline stage can run shell commands, so every run requires it"},
		{"approval", "", "string", "", "run under this approved approval id"},
		{"break-glass", "", "bool", "", "get past an approve decision yourself, loudly: needs a terminal, --reason and the target typed; never gets past a deny"},
		{"reason", "", "string", "", "why you are breaking glass (required with --break-glass; it goes on the record)"},
	}},
	{path: "policy", use: "policy", short: "Explain, apply and report on the operator's policy", group: true, flags: []flagSpec{}},
	{path: "policy apply", use: "apply", short: "Make the working policy files the applied snapshot (interactive)", group: false, flags: []flagSpec{}},
	{path: "policy enforce", use: "enforce --scope <key=value,...> | --all", short: "Switch enforcement on for a scope, or back to shadow (interactive)", group: false, flags: []flagSpec{
		{"all", "", "bool", "", "enforce everything (mode: enforce)"},
		{"id", "", "string", "", "a name for the enforced scope"},
		{"off", "", "bool", "", "return the scope, or with --all the mode, to shadow"},
		{"scope", "", "string", "", "the scope to enforce, as comma-separated key=value terms"},
		{"since", "", "duration", "168h0m0s", "the shadow data to show for the scope"},
	}},
	{path: "policy explain", use: "explain <connector.operation>", short: "Show the decision for an operation and every rule that matched", group: false, flags: []flagSpec{
		{"adhoc", "", "bool", "", "evaluate a target named by connection settings rather than a registered resource"},
		{"as", "", "string", "", "principal kind: human, agent or automation (default: how this terminal is classified)"},
		{"dry-run", "", "bool", "", "evaluate as a dry run (the plan step)"},
		{"output", "o", "string", "text", "output format: text|json"},
		{"target", "", "string", "", "a registered resource id to evaluate against"},
		{"working", "", "bool", "", "evaluate the working files instead of the applied snapshot"},
	}},
	{path: "policy report", use: "report", short: "Summarize what policy would block, and what enforcing it would need", group: false, flags: []flagSpec{
		{"output", "o", "string", "text", "output format: text|json"},
		{"scope", "", "string", "", "only decisions in this scope: key=value terms, comma-separated (principal, env, effect, connector, target, rule)"},
		{"since", "", "string", "", "decisions at or after this date or time"},
		{"until", "", "string", "", "decisions before this date or time"},
	}},
	{path: "portfolio", use: "portfolio", short: "Combined portfolio health and cross-fleet observation", group: true, flags: []flagSpec{}},
	{path: "portfolio health", use: "health", short: "Combined portfolio health projection across hosts, projects, and deployments", group: false, flags: []flagSpec{
		{"host", "", "string", "", "filter to one host name"},
		{"include-unsupervised", "", "bool", "true", "include unsupervised connector handles"},
		{"json", "", "bool", "", "output JSON"},
		{"kind", "", "string", "", "filter by deployment kind (local_service, remote_service, shared_infra, connector_handle)"},
		{"mode", "", "string", "cached", "observation mode: cached or live_bounded"},
		{"output", "o", "string", "text", "output format: text|json"},
		{"project", "", "string", "", "filter to one project ID"},
		{"rollup-status", "", "string", "", "filter by rollup status"},
	}},
	{path: "posture", use: "posture", short: "Show or change the posture: secure by default, permissive by choice", group: true, flags: []flagSpec{}},
	{path: "posture show", use: "show", short: "Show the applied posture and what it relaxes", group: false, flags: []flagSpec{
		{"output", "o", "string", "text", "output format: text|json"},
	}},
	{path: "posture set", use: "set <secure|permissive>", short: "Set the global posture, or with --scope a posture rule (interactive)", group: false, flags: []flagSpec{
		{"scope", "", "stringArray", "", "a posture rule's scope, as key=value (repeatable)"},
	}},
	{path: "posture reset", use: "reset", short: "Return to the secure default, or with --scope remove one posture rule (interactive)", group: false, flags: []flagSpec{
		{"scope", "", "stringArray", "", "a posture rule's scope, as key=value (repeatable)"},
	}},
	{path: "schedule", use: "schedule", short: "Manage durable schedules (engine remains inactive)", group: true, flags: []flagSpec{
		{"json", "", "bool", "", "print the shared JSON response (errors also use JSON)"},
	}},
	{path: "schedule admin-view", use: "admin-view", short: "Scheduled job admin-view", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"id", "", "string", "", "job ID selector"},
		{"state", "", "string", "", "enabled, paused or completed filter"},
	}},
	{path: "schedule create", use: "create", short: "Scheduled job create", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"file", "", "string", "-", "JSON job file, or - for stdin"},
		{"id", "", "string", "", "job ID selector"},
		{"idempotency-key", "", "string", "", "required durable create idempotency key"},
	}},
	{path: "schedule delete", use: "delete", short: "Scheduled job delete", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"id", "", "string", "", "job ID selector"},
		{"revision", "", "string", "", "required revision from get; stale edits refuse"},
	}},
	{path: "schedule dry-run", use: "dry-run", short: "Scheduled job dry-run", group: false, flags: []flagSpec{
		{"after", "", "string", "now", "RFC3339 dry-run starting instant"},
		{"app", "", "string", "", "application namespace selector"},
		{"file", "", "string", "-", "JSON job file, or - for stdin"},
		{"id", "", "string", "", "job ID selector"},
		{"limit", "", "int", "5", "next fire count, 1..100"},
	}},
	{path: "schedule get", use: "get", short: "Scheduled job get", group: false, flags: []flagSpec{
		{"app", "", "string", "", "application namespace selector"},
		{"id", "", "string", "", "job ID selector"},
	}},
	{path: "schedule history", use: "history", short: "Scheduled job history", group: false, flags: []flagSpec{
		{"app", "", "string", "", "application namespace selector"},
		{"id", "", "string", "", "job ID selector"},
		{"limit", "", "int", "100", "history limit, 1..1000"},
	}},
	{path: "schedule list", use: "list", short: "Scheduled job list", group: false, flags: []flagSpec{
		{"app", "", "string", "", "application namespace selector"},
		{"id", "", "string", "", "job ID selector"},
		{"state", "", "string", "", "enabled, paused or completed filter"},
	}},
	{path: "schedule logs", use: "logs", short: "Scheduled job logs", group: false, flags: []flagSpec{
		{"app", "", "string", "", "application namespace selector"},
		{"fire-id", "", "string", "", "run ID; logs currently report unavailable"},
		{"id", "", "string", "", "job ID selector"},
	}},
	{path: "schedule pause", use: "pause", short: "Scheduled job pause", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"id", "", "string", "", "job ID selector"},
		{"revision", "", "string", "", "required revision from get; stale edits refuse"},
	}},
	{path: "schedule register", use: "register", short: "Scheduled job register", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"file", "", "string", "-", "JSON job file, or - for stdin"},
		{"id", "", "string", "", "job ID selector"},
		{"idempotency-key", "", "string", "", "required durable create idempotency key"},
	}},
	{path: "schedule resume", use: "resume", short: "Scheduled job resume", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"id", "", "string", "", "job ID selector"},
		{"revision", "", "string", "", "required revision from get; stale edits refuse"},
	}},
	{path: "schedule run-now", use: "run-now", short: "Scheduled job run-now", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"id", "", "string", "", "job ID selector"},
		{"request-id", "", "string", "", "required durable manual-run request ID"},
	}},
	{path: "schedule update", use: "update", short: "Scheduled job update", group: false, flags: []flagSpec{
		{"ack", "", "bool", "", "acknowledge this mutation; never execution authority"},
		{"app", "", "string", "", "application namespace selector"},
		{"approval-id", "", "string", "", "existing shared policy approval ID"},
		{"file", "", "string", "-", "JSON job file, or - for stdin"},
		{"id", "", "string", "", "job ID selector"},
		{"revision", "", "string", "", "required revision from get; stale edits refuse"},
	}},
	{path: "whoami", use: "whoami", short: "Show how Cerberus classifies the current caller", group: false, flags: []flagSpec{
		{"output", "o", "string", "text", "output format: text|json"},
	}},
}
