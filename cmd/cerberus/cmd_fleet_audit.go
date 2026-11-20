package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/fleetaudit"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var fleetAuditOpts struct {
	json            bool
	behindThreshold int
	stateDir        string
	notifyCommand   string
	descriptorDirs  []string
	onlyNew         bool
	keepReports     int
}

var fleetAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Find provenance problems across the fleet and report what is new",
	Long: `Audit takes a fresh fleet inventory and reports findings:

  dirty_build       a running service built from a tree with uncommitted changes
  unstamped         a running Go service with no VCS revision
  replaced_on_disk  a process running a binary replaced after it started
  behind            a build more than --behind-threshold commits behind its
                    default branch (installed binaries: twice that)
  drift             the same executable at different builds across hosts
  module_mismatch   a resource built from a source dir whose module differs
                    from the module it runs
  stale_descriptor  a *.cerberus.yaml the registry index does not load
  not_running       an auto_start resource with no running unit or container
  host_unreachable, no_probe

Each finding has a stable ID. The IDs of the last run are kept in
--state-dir, so a run marks what is new and what was resolved, and
--notify-command runs only when something is new. The command runs with
sh -c, receives the summary on stdin and the subject in FLEET_AUDIT_SUBJECT.
Each run's full report is written to --state-dir/reports/.

Descriptor directories default to ~/.config, or the colon-separated
CERBERUS_FLEET_DESCRIPTOR_DIRS.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		inv, err := fleetInventory(cmd, cerbapi.FleetInventoryQuery{Refresh: true})
		if err != nil {
			return err
		}
		opts := fleetaudit.Options{BehindThreshold: fleetAuditOpts.behindThreshold, ResourceModules: map[string]string{}}
		if v2, err := registry.ResolveConfig(cfgPath); err == nil {
			opts.Resources = v2.Resources
			for _, r := range v2.Resources {
				if _, builds := r.Config["build_strategy"]; !builds {
					continue
				}
				if dir, _ := r.Config["dir"].(string); dir != "" {
					if mod := goModule(filepath.Join(dir, "go.mod")); mod != "" {
						opts.ResourceModules[r.ID] = mod
					}
				}
			}
		}
		if indexPath, err := registry.IndexPathFor(cfgPath); err == nil {
			if idx, err := registry.LoadIndex(indexPath); err == nil {
				for _, e := range idx.Entries {
					opts.Registered = append(opts.Registered, e.Path)
				}
				opts.Descriptors = findDescriptors(auditDescriptorDirs(cmd))
			}
		}
		findings := fleetaudit.Run(inv, opts)

		stateDir := fleetAuditOpts.stateDir
		if stateDir == "" {
			home, _ := os.UserHomeDir()
			stateDir = filepath.Join(home, ".cerberus", "fleet-audit")
		}
		prev := loadAuditState(stateDir)
		rep := &fleetaudit.Report{Schema: fleetaudit.Schema, GeneratedAt: time.Now().UTC().Truncate(time.Second), Hosts: len(inv.Hosts), Findings: findings}
		rep.Resolved = fleetaudit.MarkNew(rep.Findings, prev)
		if err := saveAuditState(stateDir, rep, fleetAuditOpts.keepReports); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: audit state not saved: %v\n", err)
		}

		var newOnes []fleetaudit.Finding
		for _, f := range rep.Findings {
			if f.New {
				newOnes = append(newOnes, f)
			}
		}
		if fleetAuditOpts.notifyCommand != "" && len(newOnes) > 0 {
			if err := notifyAudit(fleetAuditOpts.notifyCommand, rep, newOnes); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: notify command failed: %v\n", err)
			}
		}
		if fleetAuditOpts.json {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		}
		printAudit(cmd.OutOrStdout(), rep, fleetAuditOpts.onlyNew)
		return nil
	},
}

func init() {
	f := fleetAuditCmd.Flags()
	f.BoolVar(&fleetAuditOpts.json, "json", false, "print the report as JSON ("+fleetaudit.Schema+")")
	f.IntVar(&fleetAuditOpts.behindThreshold, "behind-threshold", 20, "commits behind the default branch before a running service is a finding")
	f.StringVar(&fleetAuditOpts.stateDir, "state-dir", "", "where the last run's finding IDs and the reports are kept (default ~/.cerberus/fleet-audit)")
	f.StringVar(&fleetAuditOpts.notifyCommand, "notify-command", "", "run with sh -c when there are new findings (summary on stdin, subject in FLEET_AUDIT_SUBJECT)")
	f.StringSliceVar(&fleetAuditOpts.descriptorDirs, "descriptor-dir", nil, "directories searched for *.cerberus.yaml (default ~/.config or $CERBERUS_FLEET_DESCRIPTOR_DIRS)")
	f.BoolVar(&fleetAuditOpts.onlyNew, "new", false, "print only findings that are new since the last run")
	f.IntVar(&fleetAuditOpts.keepReports, "keep-reports", 30, "reports to keep in --state-dir/reports")
	fleetCmd.AddCommand(fleetAuditCmd)
}

func auditDescriptorDirs(cmd *cobra.Command) []string {
	if cmd.Flags().Changed("descriptor-dir") {
		return fleetAuditOpts.descriptorDirs
	}
	if env := os.Getenv("CERBERUS_FLEET_DESCRIPTOR_DIRS"); env != "" {
		var dirs []string
		for _, d := range strings.Split(env, ":") {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".config")}
}

// findDescriptors lists *.cerberus.yaml files up to three levels below each dir.
func findDescriptors(dirs []string) []string {
	var out []string
	for _, root := range dirs {
		base := strings.Count(filepath.Clean(root), string(filepath.Separator))
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.Count(p, string(filepath.Separator))-base >= 3 || (p != root && strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".cerberus.yaml") {
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

func goModule(gomod string) string {
	f, err := os.Open(gomod)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); strings.HasPrefix(line, "module ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`)
		}
	}
	return ""
}

type auditState struct {
	GeneratedAt time.Time `json:"generated_at"`
	FindingIDs  []string  `json:"finding_ids"`
}

func loadAuditState(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil
	}
	var st auditState
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return st.FindingIDs
}

func saveAuditState(dir string, rep *fleetaudit.Report, keep int) error {
	reports := filepath.Join(dir, "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		return err
	}
	st := auditState{GeneratedAt: rep.GeneratedAt}
	for _, f := range rep.Findings {
		st.FindingIDs = append(st.FindingIDs, f.ID)
	}
	if err := writeJSONFile(filepath.Join(dir, "state.json"), st); err != nil {
		return err
	}
	name := rep.GeneratedAt.Format("20060102T150405Z") + ".json"
	if err := writeJSONFile(filepath.Join(reports, name), rep); err != nil {
		return err
	}
	ents, err := os.ReadDir(reports)
	if err != nil || keep <= 0 {
		return nil
	}
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(reports, names[0]))
		names = names[1:]
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func auditSummary(rep *fleetaudit.Report, newOnes []fleetaudit.Finding) (string, string) {
	counts := map[string]int{}
	for _, f := range rep.Findings {
		counts[f.Severity]++
	}
	subject := fmt.Sprintf("fleet audit: %d new finding(s)", len(newOnes))
	var b strings.Builder
	fmt.Fprintf(&b, "%d hosts · %d findings (%d high, %d medium, %d low) · %d new · %d resolved\n\nNEW\n",
		rep.Hosts, len(rep.Findings), counts[fleetaudit.High], counts[fleetaudit.Medium], counts[fleetaudit.Low], len(newOnes), len(rep.Resolved))
	for _, f := range newOnes {
		host := ""
		if f.Host != "" {
			host = f.Host + " "
		}
		fmt.Fprintf(&b, "[%s] %s: %s%s\n  %s\n", f.Severity, f.Kind, host, f.Subject, f.Detail)
	}
	b.WriteString("\nFull report: cerberus fleet audit (or --json); state in ~/.cerberus/fleet-audit.\n")
	return subject, b.String()
}

func notifyAudit(command string, rep *fleetaudit.Report, newOnes []fleetaudit.Finding) error {
	subject, body := auditSummary(rep, newOnes)
	c := exec.Command("sh", "-c", command) //nolint:gosec // the operator's own notify command, from their flag
	c.Env = append(os.Environ(), "FLEET_AUDIT_SUBJECT="+subject)
	c.Stdin = strings.NewReader(body)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func printAudit(w io.Writer, rep *fleetaudit.Report, onlyNew bool) {
	counts := map[string]int{}
	newCount := 0
	for _, f := range rep.Findings {
		counts[f.Severity]++
		if f.New {
			newCount++
		}
	}
	fmt.Fprintf(w, "fleet audit · %d hosts · %d findings (%d high, %d medium, %d low) · %d new · %d resolved\n\n",
		rep.Hosts, len(rep.Findings), counts[fleetaudit.High], counts[fleetaudit.Medium], counts[fleetaudit.Low], newCount, len(rep.Resolved))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEV\tKIND\tHOST\tSUBJECT\tNEW\tDETAIL")
	for _, f := range rep.Findings {
		if onlyNew && !f.New {
			continue
		}
		mark := ""
		if f.New {
			mark = "new"
		}
		detail := f.Detail
		if len(detail) > 140 {
			detail = detail[:137] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", f.Severity, f.Kind, valueOrDash(f.Host), f.Subject, valueOrDash(mark), detail)
	}
	tw.Flush()
}
