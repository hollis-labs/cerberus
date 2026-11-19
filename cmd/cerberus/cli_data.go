package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
)

// `cerberus data query` (slice 7d): old's verb, flag, checks and output (cmd_data.go and
// cerbapi data_client.go on main) over POST /api/data/v1/query. The session token comes from a
// private file, never an argument or the environment, and travels in api.DataTokenHeader beside
// this CLI's key; the serving Cerberus verifies it, the grant and policy. There is no fallback:
// a refusal or a server without the versioned route is an error.

func dataCommand(client func() (*apiClient, error)) *cobra.Command {
	c := &cobra.Command{Use: "data", Short: "Session-authorized named data projections"}
	var tokenFile string
	q := &cobra.Command{Use: "query", Short: "Read a JSON projection request from stdin; token comes from a private file", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := os.Open(tokenFile) //nolint:gosec // the operator's explicitly named private file, bounded and checked below
			if err != nil {
				return errors.New("cannot open data token file")
			}
			defer func() { _ = f.Close() }()
			st, err := f.Stat()
			if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || st.Size() > 4096 {
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
			cl, err := client()
			if err != nil {
				return err
			}
			result, err := cl.queryData(cmd, strings.TrimSpace(string(b)), req)
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

// queryData is old's QueryData: no fallback, and an answer only from a server that marks it
// with the data route's version, decoded strictly.
func (c *apiClient) queryData(cmd *cobra.Command, token string, q dataaccess.Request) (dataaccess.Result, error) {
	var out dataaccess.Result
	b, err := json.Marshal(q)
	if err != nil {
		return out, errors.New("invalid data request")
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, c.base+"/data/v1/query", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set(api.DataTokenHeader, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return out, errors.New("data server unavailable; no fallback was attempted")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get(api.DataVersionHeader) != "1" {
		return out, errors.New("data request refused or versioned data service unavailable")
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return out, errors.New("invalid data response")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || dec.Decode(new(any)) != io.EOF {
		return out, errors.New("invalid data response")
	}
	return out, nil
}

func (c *cerberus) dataCommand() *cobra.Command {
	return dataCommand(func() (*apiClient, error) { return newAPIClient(c.app, c.home) })
}
