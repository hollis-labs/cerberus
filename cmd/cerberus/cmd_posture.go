package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
)

var postureFlags struct {
	scope  []string
	output string
}

var postureCmd = &cobra.Command{
	Use:   "posture",
	Short: "Show or change the posture: secure by default, permissive by choice",
	Long: `The posture decides how strict Cerberus is (section 13 of the security plan).

  secure      the default: the policy baseline, the target defaults, plugin
              install review, changed-hash refusal, loopback-only listeners,
              and opt-in MCP exposure
  permissive  opted into: the built-in strictness steps aside

The global posture governs the host-wide switches, which have no target, and is
where policy evaluation starts. Posture rules, scoped with --scope, change only
the policy evaluation of the targets they match. A permissive rule reaches only
targets whose env and owner are declared and that are not ad hoc.

Nothing relaxes the audit log, credential redaction, the --ack gate, or the
deny on a target labeled admin: owner.

The posture is part of the applied policy snapshot. 'cerberus posture set'
writes ~/.cerberus/policy/posture.yaml and applies it the way 'cerberus policy
apply' does: in a terminal, after showing what changes, on a typed
confirmation, recorded in the audit log.`,
}

var postureShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the applied posture and what it relaxes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		summary := currentPosture()
		if postureFlags.output == outputFormatJSON {
			return printJSON(map[string]any{"posture": summary, "host_wide": hostWideRows(summary.Global)})
		}
		return writePostureShow(cmd.OutOrStdout(), summary)
	},
}

var postureSetCmd = &cobra.Command{
	Use:   "set <secure|permissive>",
	Short: "Set the global posture, or with --scope a posture rule (interactive)",
	Long: `Set the global posture, or with --scope add a posture rule for the targets that
match. A scope is one or more key=value pairs: id, connector, kind, env, owner,
admin, or tag, which may repeat. A value prefixed with "!" matches anything
else. For example:

  cerberus posture set permissive                     # everything
  cerberus posture set permissive --scope env=dev --scope owner=self
  cerberus posture set secure --scope id=payments-db  # keep one target strict

A rule with the same scope is replaced. It runs only from an interactive
terminal: it shows the host-wide switches and the sampled policy decisions that
change, and applies on a typed confirmation.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		posture := args[0]
		if posture != policy.PostureSecure && posture != policy.PosturePermissive {
			return fmt.Errorf("the posture is secure or permissive, not %q", posture)
		}
		return changePosture(cmd, func(f *policy.File, match *policy.TargetMatch) (string, error) {
			if match == nil {
				f.Posture = posture
				return "set the global posture to " + posture, nil
			}
			f.SetPostureRule(*match, posture)
			return fmt.Sprintf("set %s for %s", posture, match.String()), nil
		})
	},
}

var postureResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Return to the secure default, or with --scope remove one posture rule (interactive)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return changePosture(cmd, func(f *policy.File, match *policy.TargetMatch) (string, error) {
			if match == nil {
				*f = policy.File{Version: policy.FileVersion}
				return "reset the posture to secure, with no posture rules", nil
			}
			if !f.RemovePostureRule(*match) {
				return "", fmt.Errorf("there is no posture rule for %s; `cerberus posture show` lists them", match.String())
			}
			return "remove the posture rule for " + match.String(), nil
		})
	},
}

var errPostureNotInteractive = errors.New("the posture is changed only from an interactive terminal, where the change is shown and you type a confirmation; run `cerberus posture set` in your terminal, not from a script or an agent")

// changePosture is set and reset: edit posture.yaml in memory, show what the
// change flips, and apply it through the policy apply path on a typed
// confirmation. Nothing is written before the confirmation.
func changePosture(cmd *cobra.Command, edit func(*policy.File, *policy.TargetMatch) (string, error)) error {
	if !policyIsTerminal() {
		return errPostureNotInteractive
	}
	match, err := parseScope(postureFlags.scope)
	if err != nil {
		return err
	}
	store, err := policyStore()
	if err != nil {
		return err
	}
	elsewhere, err := store.PostureDeclaredElsewhere()
	if err != nil {
		return err
	}
	if len(elsewhere) > 0 {
		return fmt.Errorf("the posture is also declared in %s; `cerberus posture` owns it in posture.yaml, so move it there or remove it, then retry", strings.Join(elsewhere, ", "))
	}
	current, status := store.Load()
	if status.Mismatch() {
		return errors.New("the applied policy snapshot fails its hash check, so the baseline is deciding; review the working files and run `cerberus policy apply` before changing the posture")
	}
	// posture set applies the working files. If they already differ from the
	// applied snapshot — including when nothing is applied yet — it would
	// apply someone's unreviewed edits along with the posture; policy apply
	// is where those are reviewed.
	if unapplied, uerr := workingDiffersFromApplied(store, current); uerr != nil {
		return uerr
	} else if unapplied {
		return errors.New("the working policy files have changes that are not applied; review and apply them with `cerberus policy apply` first, so a posture change does not carry them in unseen")
	}
	file, err := store.PostureFile()
	if err != nil {
		return fmt.Errorf("read %s: %w", policy.PostureFileName, err)
	}
	before := file.PostureSummary(status.Snapshot)
	what, err := edit(&file, match)
	if err != nil {
		return err
	}
	working, problems, err := store.LoadWorkingWithPosture(file)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("the working policy files have problems, so nothing was changed:\n  %s", strings.Join(problems, "\n  "))
	}
	data, err := policy.Encode(working)
	if err != nil {
		return err
	}
	hash := policy.Hash(data)
	if hash == status.Snapshot {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "That is already the applied posture; nothing to change.")
		return err
	}
	after := working.PostureSummary(hash)

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Posture: %s\n     to: %s\n\n", before, after)
	writeHostWideChange(out, before.Global, after.Global)
	flips := policy.Flips(current, policy.NewEvaluator(working, hash), policy.Sample(policyDefinitions(cmd.Context()), policyResources()))
	fmt.Fprintln(out)
	writeFlips(out, flips)
	phrase := "posture " + shortHash(hash)
	fmt.Fprintf(out, "\nThis will %s. Type %q to apply: ", what, phrase)
	line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if strings.TrimSpace(line) != phrase {
		return errors.New("the confirmation did not match; nothing was changed")
	}
	restore, err := store.WritePostureFile(file)
	if err != nil {
		return err
	}
	applied, err := cerbapi.ApplyPolicy(inProcessContext(cmd.Context()), policySink(), store, working, len(flips))
	if err != nil {
		restore()
		return err
	}
	_, err = fmt.Fprintf(out, "Applied %s. The posture is now: %s\n", applied, after)
	return err
}

// workingDiffersFromApplied reports whether the working files, as they are,
// would apply something other than the applied snapshot. Problems in them
// are left for the posture change's own load to report.
func workingDiffersFromApplied(store policy.Store, applied *policy.Evaluator) (bool, error) {
	working, problems, err := store.LoadWorking()
	if err != nil || len(problems) > 0 {
		return false, err
	}
	w, err := policy.Encode(working)
	if err != nil {
		return false, err
	}
	a, err := policy.Encode(applied.File())
	if err != nil {
		return false, err
	}
	return policy.Hash(w) != policy.Hash(a), nil
}

// parseScope reads --scope key=value pairs into a match, or nil for none.
func parseScope(pairs []string) (*policy.TargetMatch, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	var m policy.TargetMatch
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, fmt.Errorf("--scope takes key=value, not %q", pair)
		}
		set := func(field *string) error {
			if *field != "" {
				return fmt.Errorf("--scope names %s twice", key)
			}
			*field = value
			return nil
		}
		var err error
		switch key {
		case "id":
			err = set(&m.ID)
		case "connector":
			err = set(&m.Connector)
		case "kind":
			err = set(&m.Kind)
		case "env":
			err = set(&m.Env)
		case "owner":
			err = set(&m.Owner)
		case "admin":
			err = set(&m.Admin)
		case "tag":
			m.Tags = append(m.Tags, value)
		default:
			err = fmt.Errorf("--scope key %q is not id, connector, kind, env, owner, admin or tag", key)
		}
		if err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// hostWideRow is one host-wide switch the global posture governs (section
// 13's table). Live is false until the release that wires it.
type hostWideRow struct {
	Switch     string `json:"switch"`
	Secure     string `json:"secure"`
	Permissive string `json:"permissive"`
	Live       bool   `json:"live"`
}

var hostWide = []hostWideRow{
	{Switch: "plugin install", Secure: "review summary and a confirmation on a terminal", Permissive: "summary printed; --yes skips the confirmation", Live: true},
	{Switch: "changed plugin", Secure: "refused until re-reviewed", Permissive: "loaded, with a warning and an audit record", Live: true},
	{Switch: "MCP exposure of plugin operations", Secure: "only what connector-config.yaml exposes", Permissive: "every declared operation, narrowed by connector-config.yaml"},
	{Switch: "mcp-http listen", Secure: "loopback only", Permissive: "--insecure-listen allowed, with a warning and an audit record"},
}

// hostWideRows is the table with the side the posture selects.
func hostWideRows(global string) []map[string]any {
	out := make([]map[string]any, 0, len(hostWide))
	for _, r := range hostWide {
		effect := r.Secure
		if global == policy.PosturePermissive {
			effect = r.Permissive
		}
		out = append(out, map[string]any{"switch": r.Switch, "now": effect, "live": r.Live})
	}
	return out
}

func writeHostWideChange(w io.Writer, from, to string) {
	if from == to {
		fmt.Fprintf(w, "Host-wide switches: unchanged (the global posture stays %s).\n", to)
		return
	}
	fmt.Fprintf(w, "Host-wide switches change (the global posture goes %s -> %s):\n", from, to)
	for _, r := range hostWide {
		was, now := r.Secure, r.Permissive
		if to == policy.PostureSecure {
			was, now = r.Permissive, r.Secure
		}
		note := ""
		if !r.Live {
			note = " (in a later release; recorded in the snapshot now)"
		}
		fmt.Fprintf(w, "  %s: %s -> %s%s\n", r.Switch, was, now, note)
	}
}

func writePostureShow(w io.Writer, s policy.PostureSummary) error {
	fmt.Fprintf(w, "Posture: %s\n", s)
	fmt.Fprintf(w, "Snapshot: %s\n\nHost-wide switches (the global posture, %s):\n", s.Snapshot, s.Global)
	for _, r := range hostWideRows(s.Global) {
		note := ""
		if live, _ := r["live"].(bool); !live {
			note = " (in a later release)"
		}
		fmt.Fprintf(w, "  %s: %s%s\n", r["switch"], r["now"], note)
	}
	if len(s.Rules) > 0 {
		fmt.Fprintln(w, "\nPosture rules (policy evaluation of the targets they match):")
		for _, r := range s.Rules {
			fmt.Fprintf(w, "  %s: %s\n", r.Match, r.Posture)
		}
	}
	_, err := fmt.Fprintln(w, "\nNever relaxed: the audit log, credential redaction, the --ack gate, and the deny on a target labeled admin: owner.")
	return err
}

func init() {
	postureCmd.GroupID = "runtime"
	for _, c := range []*cobra.Command{postureSetCmd, postureResetCmd} {
		c.Flags().StringArrayVar(&postureFlags.scope, "scope", nil, "a posture rule's scope, as key=value (repeatable)")
	}
	addOutputFlag(postureShowCmd, &postureFlags.output)
	postureCmd.AddCommand(postureShowCmd, postureSetCmd, postureResetCmd)
	rootCmd.AddCommand(postureCmd)
}
