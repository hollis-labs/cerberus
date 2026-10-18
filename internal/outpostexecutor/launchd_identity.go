package outpostexecutor

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

func (p *Plugin) guardLaunchd(ctx context.Context, r outpost.Request) error {
	// launchd has no systemd-style aliases for bootout/kickstart: their argument
	// is a literal label in gui/<uid>. Bind protection to the actual Outpost job,
	// whose PID is this subprocess's parent, as well as the installed defaults.
	result, err := p.Run(ctx, "launchctl", []string{"list"})
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return fmt.Errorf("cannot identify serving launchd job")
	}
	found := false
	for _, line := range strings.Split(result.Output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		if fields[0] == strconv.Itoa(os.Getppid()) || fields[0] == strconv.Itoa(os.Getpid()) {
			found = true
			if fields[2] == r.Unit {
				return controlPlaneError()
			}
		}
	}
	if !found && len(p.Protected) == 0 {
		return fmt.Errorf("cannot identify serving job; configure protected_units")
	}
	if r.Verb != "start" {
		return nil
	}
	// Bootstrap consumes a plist, whose Label may differ from its filename.
	// Refuse that alias rather than dispatch under an unchecked identity.
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	b, _, err := readRegular(ctx, filepath.Join(home, "Library", "LaunchAgents", r.Unit+".plist"), 1<<20, false)
	if err != nil {
		return err
	}
	var plist struct {
		Dict struct {
			Values []struct {
				XMLName xml.Name
				Text    string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.NewDecoder(contextReader{ctx, bytes.NewReader(b)}).Decode(&plist); err != nil {
		return err
	}
	label := ""
	for i, v := range plist.Dict.Values {
		if v.XMLName.Local != "key" || v.Text != "Label" {
			continue
		}
		if label != "" || i+1 >= len(plist.Dict.Values) || plist.Dict.Values[i+1].XMLName.Local != "string" {
			return fmt.Errorf("ambiguous launchd label")
		}
		label = plist.Dict.Values[i+1].Text
	}
	if label != r.Unit {
		return fmt.Errorf("launchd plist label must match requested unit")
	}
	return ctx.Err()
}
