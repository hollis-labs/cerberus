package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/app"
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
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		cf, err := cfconn.New(a.Secrets)
		if err != nil {
			return err
		}

		zones, err := cf.ListZones(context.Background())
		if err != nil {
			return err
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
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		cf, err := cfconn.New(a.Secrets)
		if err != nil {
			return err
		}

		records, err := cf.ListDNSRecords(context.Background(), args[0])
		if err != nil {
			return err
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
	dnsCreateType    string
	dnsCreateName    string
	dnsCreateContent string
	dnsCreateTTL     int
	dnsCreateProxied bool
)

var cloudflareDNSCreateCmd = &cobra.Command{
	Use:   "create <zone-id>",
	Short: "Create a DNS record",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		cf, err := cfconn.New(a.Secrets)
		if err != nil {
			return err
		}

		rec := cfconn.DNSRecord{
			Type:    dnsCreateType,
			Name:    dnsCreateName,
			Content: dnsCreateContent,
			TTL:     dnsCreateTTL,
			Proxied: dnsCreateProxied,
		}

		created, err := cf.CreateDNSRecord(context.Background(), args[0], rec)
		if err != nil {
			return err
		}

		data, _ := json.MarshalIndent(created, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	cloudflareDNSCreateCmd.Flags().StringVar(&dnsCreateType, "type", "A", "DNS record type (A, AAAA, CNAME, MX, TXT)")
	cloudflareDNSCreateCmd.Flags().StringVar(&dnsCreateName, "name", "", "DNS record name")
	cloudflareDNSCreateCmd.Flags().StringVar(&dnsCreateContent, "content", "", "DNS record content")
	cloudflareDNSCreateCmd.Flags().IntVar(&dnsCreateTTL, "ttl", 1, "TTL in seconds (1 = automatic)")
	cloudflareDNSCreateCmd.Flags().BoolVar(&dnsCreateProxied, "proxied", false, "proxy through Cloudflare")

	cloudflareDNSCmd.AddCommand(cloudflareDNSListCmd)
	cloudflareDNSCmd.AddCommand(cloudflareDNSCreateCmd)
	cloudflareCmd.AddCommand(cloudflareZonesCmd)
	cloudflareCmd.AddCommand(cloudflareDNSCmd)
}
