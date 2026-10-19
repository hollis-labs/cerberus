package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/spf13/cobra"
)

func newDataCmd() *cobra.Command {
	c := &cobra.Command{Use: "data", Short: "Session-authorized named data projections"}
	var tokenFile string
	q := &cobra.Command{Use: "query", Short: "Read a JSON projection request from stdin; token comes from a private file", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		f, err := os.Open(tokenFile) //nolint:gosec // explicitly selected private operator file, bounded and checked below
		if err != nil {
			return errors.New("cannot open data token file")
		}
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
			return errors.New("data token requires a private regular file of at most 4096 bytes")
		}
		b, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil || len(b) > 4096 {
			return errors.New("cannot read data token")
		}
		var req dataaccess.Request
		d := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 65537))
		d.DisallowUnknownFields()
		if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
			return errors.New("invalid data request JSON")
		}
		client, err := newResourceSocketClient()
		if err != nil {
			return err
		}
		result, err := client.QueryData(cmd.Context(), strings.TrimSpace(string(b)), req)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	q.Flags().StringVar(&tokenFile, "token-file", "", "Private Tether session token file (never the token itself)")
	_ = q.MarkFlagRequired("token-file")
	c.AddCommand(q)
	return c
}
func init() { rootCmd.AddCommand(newDataCmd()) }
