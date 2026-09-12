package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/redact"

	"github.com/chrispian/cerberus/internal/cerbapi"
	ncconn "github.com/chrispian/cerberus/internal/connector/namecheap"
	"github.com/spf13/cobra"
)

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Domain operations (Namecheap)",
	Long: `Domain inventory and nameserver management via the Namecheap connector.

Routes through the connector configured under 'connectors.namecheap' in the
v2 config (API user + key required, IP allowlisted on Namecheap's side).
Subcommands cover list / status / nameservers; use 'cerberus dns' for record
management on a domain.`,
}

var domainNameserversCmd = &cobra.Command{
	Use:   "nameservers",
	Short: "Nameserver operations for a domain",
}

var domainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all domains",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "namecheap",
			Operation: "list_domains",
		})
		if err != nil {
			return err
		}
		domains, ok := result.Data.([]ncconn.Domain)
		if !ok {
			return fmt.Errorf("domain list: unexpected result type %T", result.Data)
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
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "namecheap",
			Operation: "get_domain_status",
			Config: map[string]any{
				"domain": args[0],
			},
		})
		if err != nil {
			return err
		}
		status, ok := result.Data.(*ncconn.DomainStatus)
		if !ok {
			return fmt.Errorf("domain status: unexpected result type %T", result.Data)
		}

		data, _ := redact.MarshalIndent(status, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS operations (Namecheap)",
	Long: `DNS record management via the Namecheap connector.

Lists visible records on a domain you own through Namecheap. Per-record create/delete are disabled because getHosts can omit records. Use explicit set_dns_record_set for authoritative whole-zone replacement.
For Cloudflare-managed zones, use 'cerberus cloudflare dns' instead.
Records are addressed by record ID returned from 'dns list'.`,
}

var (
	namecheapDNSCreateType   string
	namecheapDNSCreateHost   string
	namecheapDNSCreateValue  string
	namecheapDNSCreateTTL    int
	namecheapDNSCreateMXPref int
	namecheapDryRun          bool
	namecheapAcknowledge     bool
)

var domainNameserversSetCmd = &cobra.Command{
	Use:   "set <domain> <nameserver-1> <nameserver-2> [nameserver-N...]",
	Short: "Switch a domain to custom nameservers",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "namecheap",
			Operation: "set_custom_nameservers",
			Config: map[string]any{
				"domain":      args[0],
				"nameservers": args[1:],
			},
			DryRun:       namecheapDryRun,
			Acknowledged: namecheapAcknowledge,
		})
		if err != nil {
			return err
		}
		if namecheapDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		update, ok := result.Data.(*ncconn.DomainNameserverUpdate)
		if !ok {
			return fmt.Errorf("nameserver set: unexpected result type %T", result.Data)
		}
		return writeJSON(cmd.OutOrStdout(), update)
	},
}

var dnsListCmd = &cobra.Command{
	Use:   "list <domain>",
	Short: "List DNS records for a domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "namecheap",
			Operation: "list_dns_records",
			Config: map[string]any{
				"domain": args[0],
			},
		})
		if err != nil {
			return err
		}
		records, ok := result.Data.([]ncconn.DNSRecord)
		if !ok {
			return fmt.Errorf("dns list: unexpected result type %T", result.Data)
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

var dnsCreateCmd = &cobra.Command{
	Use:   "create <domain>",
	Short: "Disabled: unsafe per-record Namecheap writes",
	Args:  cobra.ExactArgs(1),
	RunE:  func(_ *cobra.Command, _ []string) error { return ncconn.ErrUnsafePerRecordWrite },
}

var dnsDeleteCmd = &cobra.Command{
	Use:   "delete <domain> <record-id>",
	Short: "Disabled: unsafe per-record Namecheap writes",
	Args:  cobra.ExactArgs(2),
	RunE:  func(_ *cobra.Command, _ []string) error { return ncconn.ErrUnsafePerRecordWrite },
}

func init() {
	domainCmd.AddCommand(domainListCmd)
	domainCmd.AddCommand(domainStatusCmd)
	domainNameserversSetCmd.Flags().BoolVar(&namecheapDryRun, "dry-run", false, "preview the nameserver change without sending it to Namecheap")
	domainNameserversSetCmd.Flags().BoolVar(&namecheapAcknowledge, "ack", false, "acknowledge destructive nameserver change")
	domainNameserversCmd.AddCommand(domainNameserversSetCmd)
	domainCmd.AddCommand(domainNameserversCmd)
	dnsCreateCmd.Flags().StringVar(&namecheapDNSCreateType, "type", "A", "DNS record type")
	dnsCreateCmd.Flags().StringVar(&namecheapDNSCreateHost, "host", "", "host name such as @, www, or api")
	dnsCreateCmd.Flags().StringVar(&namecheapDNSCreateValue, "value", "", "record value")
	dnsCreateCmd.Flags().IntVar(&namecheapDNSCreateTTL, "ttl", 300, "TTL in seconds")
	dnsCreateCmd.Flags().IntVar(&namecheapDNSCreateMXPref, "mx-pref", 10, "MX preference for MX records")
	dnsCreateCmd.Flags().BoolVar(&namecheapDryRun, "dry-run", false, "preview the DNS change without sending it to Namecheap")
	dnsCreateCmd.Flags().BoolVar(&namecheapAcknowledge, "ack", false, "acknowledge destructive DNS change")
	dnsDeleteCmd.Flags().BoolVar(&namecheapDryRun, "dry-run", false, "preview the DNS change without sending it to Namecheap")
	dnsDeleteCmd.Flags().BoolVar(&namecheapAcknowledge, "ack", false, "acknowledge destructive DNS change")
	dnsCmd.AddCommand(dnsListCmd)
	dnsCmd.AddCommand(dnsCreateCmd)
	dnsCmd.AddCommand(dnsDeleteCmd)
}
