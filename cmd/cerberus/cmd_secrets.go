package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

var errSecretsNotInteractive = errors.New("a secret is stored only from an interactive terminal, where you paste it and it is not shown; run this in your terminal, not from a script or an agent")

// secretsReadValue reads a pasted value without echo. It reads in raw mode,
// so a long value (a Keeper configuration, a 1Password service account
// token) is not cut at the terminal's line limit. Tests swap it.
var secretsReadValue = func(in *os.File, out io.Writer) (string, error) {
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() { _ = term.Restore(fd, state) }()
	var value []byte
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		switch buf[0] {
		case '\r', '\n':
			fmt.Fprint(out, "\r\n")
			return strings.TrimSpace(string(value)), nil
		case 3: // ctrl-C
			fmt.Fprint(out, "\r\n")
			return "", errors.New("canceled; nothing was stored")
		case 127, 8: // backspace
			if len(value) > 0 {
				value = value[:len(value)-1]
			}
		default:
			value = append(value, buf[0])
		}
	}
}

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Store credentials in the OS credential store",
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <service>/<key>",
	Short: "Store one credential in the OS credential store (interactive)",
	Long: `Store one credential in the OS credential store (the macOS Keychain, the
Windows Credential Manager or the Linux Secret Service), under Cerberus's
service, where keyring://<service>/<key> and every connector's fallback read it.

It is how you store a secret backend's own credential, which must come from the
OS credential store and never from another vault:

    cerberus secrets set keeper/ksm_config
    cerberus secrets set onepassword/service_account_token

You paste the value; it is not shown, and it is never an argument, so it stays
out of your shell history and the process list. It runs only on an interactive
terminal, and the write is recorded in the audit log by name, never by value.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !policyIsTerminal() {
			return errSecretsNotInteractive
		}
		service, key, ok := strings.Cut(strings.TrimSpace(args[0]), "/")
		if !ok {
			return fmt.Errorf("%q is not <service>/<key>, for example keeper/ksm_config", args[0])
		}
		out := cmd.OutOrStdout()
		store := app.SecretStore()
		if existing, err := store.Get(cmd.Context(), service, key); err == nil && existing != "" {
			fmt.Fprintf(out, "%s/%s already has a value; pasting replaces it.\n", service, key)
		}
		fmt.Fprintf(out, "Paste the value for %s/%s (it is not shown), then press Enter: ", service, key)
		value, err := secretsReadValue(os.Stdin, out)
		if err != nil {
			return err
		}
		if err := cerbapi.SetStoredSecret(cmd.Context(), app.AuditSink(), store, service, key, value); err != nil {
			return err
		}
		fmt.Fprintf(out, "Stored %s/%s (%d characters) in the OS credential store.\n", service, key, len(value))
		fmt.Fprintf(out, "A plugin reads its credentials when it loads. If %s is loaded, run `cerberus connectors plugin managed unload %s`, then `cerberus connectors plugin managed load %s`.\n", service, service, service)
		return nil
	},
}

func init() {
	secretsCmd.AddCommand(secretsSetCmd)
	rootCmd.AddCommand(secretsCmd)
}
