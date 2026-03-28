package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/app"
	ncconn "github.com/chrispian/cerberus/internal/connector/namecheap"
	"github.com/spf13/cobra"
)

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Domain operations (Namecheap)",
}

var domainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all domains",
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		nc, err := ncconn.New(a.Secrets)
		if err != nil {
			return err
		}

		domains, err := nc.ListDomains(context.Background())
		if err != nil {
			return err
		}

		if len(domains) == 0 {
			fmt.Println("No domains found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tEXPIRES\tEXPIRED\tLOCKED\tAUTO-RENEW\tWHOISGUARD")
		fmt.Fprintln(w, "----\t-------\t-------\t------\t----------\t----------")
		for _, d := range domains {
			fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%v\t%s\n",
				d.Name, d.Expires, d.IsExpired, d.IsLocked, d.AutoRenew, d.WhoisGuard)
		}
		return w.Flush()
	},
}

var domainStatusCmd = &cobra.Command{
	Use:   "status <domain>",
	Short: "Show domain status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		nc, err := ncconn.New(a.Secrets)
		if err != nil {
			return err
		}

		status, err := nc.GetDomainStatus(context.Background(), args[0])
		if err != nil {
			return err
		}

		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS operations (Namecheap)",
}

var dnsListCmd = &cobra.Command{
	Use:   "list <domain>",
	Short: "List DNS records for a domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		nc, err := ncconn.New(a.Secrets)
		if err != nil {
			return err
		}

		records, err := nc.ListDNSRecords(context.Background(), args[0])
		if err != nil {
			return err
		}

		if len(records) == 0 {
			fmt.Println("No DNS records found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTYPE\tHOST\tVALUE\tTTL\tMX-PREF")
		fmt.Fprintln(w, "--\t----\t----\t-----\t---\t-------")
		for _, r := range records {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%d\n",
				r.ID, r.Type, r.Host, r.Value, r.TTL, r.MXPref)
		}
		return w.Flush()
	},
}

func init() {
	domainCmd.AddCommand(domainListCmd)
	domainCmd.AddCommand(domainStatusCmd)
	dnsCmd.AddCommand(dnsListCmd)
}
