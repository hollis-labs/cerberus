package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/cerbapi"
	cfconn "github.com/chrispian/cerberus/internal/connector/cloudflare"
	"github.com/spf13/cobra"
)

var cloudflareCmd = &cobra.Command{
	Use:   "cloudflare",
	Short: "Cloudflare operations",
}

var cloudflareZonesCmd = &cobra.Command{
	Use:   "zones",
	Short: "List all zones",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "cloudflare",
			Operation: "list_zones",
		})
		if err != nil {
			return err
		}
		zones, ok := result.Data.([]cfconn.Zone)
		if !ok {
			return fmt.Errorf("cloudflare zones: unexpected result type %T", result.Data)
		}

		if len(zones) == 0 {
			fmt.Println("No zones found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tPAUSED\tPLAN")
		fmt.Fprintln(w, "--\t----\t------\t------\t----")
		for _, z := range zones {
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\n",
				z.ID, z.Name, z.Status, z.Paused, z.Plan)
		}
		return w.Flush()
	},
}

var cloudflareDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS record operations",
}

var cloudflareDNSListCmd = &cobra.Command{
	Use:   "list <zone-id>",
	Short: "List DNS records for a zone",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "cloudflare",
			Operation: "list_dns_records",
			Config: map[string]any{
				"zone_id": args[0],
			},
		})
		if err != nil {
			return err
		}
		records, ok := result.Data.([]cfconn.DNSRecord)
		if !ok {
			return fmt.Errorf("cloudflare dns list: unexpected result type %T", result.Data)
		}

		if len(records) == 0 {
			fmt.Println("No DNS records found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTYPE\tNAME\tCONTENT\tTTL\tPROXIED")
		fmt.Fprintln(w, "--\t----\t----\t-------\t---\t-------")
		for _, r := range records {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%v\n",
				r.ID, r.Type, r.Name, r.Content, r.TTL, r.Proxied)
		}
		return w.Flush()
	},
}

var (
	dnsCreateType         string
	dnsCreateName         string
	dnsCreateContent      string
	dnsCreateTTL          int
	dnsCreateProxied      bool
	cloudflareDryRun      bool
	cloudflareAcknowledge bool
)

var cloudflareDNSCreateCmd = &cobra.Command{
	Use:   "create <zone-id>",
	Short: "Create a DNS record",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "cloudflare",
			Operation: "create_dns_record",
			Config: map[string]any{
				"zone_id": args[0],
				"type":    dnsCreateType,
				"name":    dnsCreateName,
				"content": dnsCreateContent,
				"ttl":     dnsCreateTTL,
				"proxied": dnsCreateProxied,
			},
			DryRun:       cloudflareDryRun,
			Acknowledged: cloudflareAcknowledge,
		})
		if err != nil {
			return err
		}
		if cloudflareDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		created, ok := result.Data.(*cfconn.DNSRecord)
		if !ok {
			return fmt.Errorf("cloudflare dns create: unexpected result type %T", result.Data)
		}

		return writeJSON(cmd.OutOrStdout(), created)
	},
}

var cloudflareDNSDeleteCmd = &cobra.Command{
	Use:   "delete <zone-id> <record-id>",
	Short: "Delete a DNS record",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()
		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "cloudflare",
			Operation: "delete_dns_record",
			Config: map[string]any{
				"zone_id":   args[0],
				"record_id": args[1],
			},
			DryRun:       cloudflareDryRun,
			Acknowledged: cloudflareAcknowledge,
		})
		if err != nil {
			return err
		}
		if cloudflareDryRun {
			return writeJSON(cmd.OutOrStdout(), result.Data)
		}
		return nil
	},
}

func init() {
	cloudflareDNSCreateCmd.Flags().StringVar(&dnsCreateType, "type", "A", "DNS record type (A, AAAA, CNAME, MX, TXT)")
	cloudflareDNSCreateCmd.Flags().StringVar(&dnsCreateName, "name", "", "DNS record name")
	cloudflareDNSCreateCmd.Flags().StringVar(&dnsCreateContent, "content", "", "DNS record content")
	cloudflareDNSCreateCmd.Flags().IntVar(&dnsCreateTTL, "ttl", 1, "TTL in seconds (1 = automatic)")
	cloudflareDNSCreateCmd.Flags().BoolVar(&dnsCreateProxied, "proxied", false, "proxy through Cloudflare")
	cloudflareDNSCreateCmd.Flags().BoolVar(&cloudflareDryRun, "dry-run", false, "preview the DNS change without sending it to Cloudflare")
	cloudflareDNSCreateCmd.Flags().BoolVar(&cloudflareAcknowledge, "ack", false, "acknowledge destructive DNS change")
	cloudflareDNSDeleteCmd.Flags().BoolVar(&cloudflareDryRun, "dry-run", false, "preview the DNS change without sending it to Cloudflare")
	cloudflareDNSDeleteCmd.Flags().BoolVar(&cloudflareAcknowledge, "ack", false, "acknowledge destructive DNS change")

	cloudflareDNSCmd.AddCommand(cloudflareDNSListCmd)
	cloudflareDNSCmd.AddCommand(cloudflareDNSCreateCmd)
	cloudflareDNSCmd.AddCommand(cloudflareDNSDeleteCmd)
	cloudflareCmd.AddCommand(cloudflareZonesCmd)
	cloudflareCmd.AddCommand(cloudflareDNSCmd)
}
