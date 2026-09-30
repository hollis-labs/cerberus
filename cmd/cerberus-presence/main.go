// Command cerberus-presence asks the person at this Mac to prove they are
// there — Touch ID or the account password, through LocalAuthentication —
// before the daemon lets a passkey enrollment start (B1-b). It prints why
// it refused on stderr and exits:
//
//	0  verified
//	1  refused or canceled
//	2  not available here (no GUI session, no LocalAuthentication, another OS)
//
// It is a separate binary so the main build stays free of cgo. It raises
// the bar for a process running as the operator — it has to replace the
// daemon or this helper, and the daemon records the helper's digest — but it
// is not a boundary against one: neither binary is signed.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	reason := flag.String("reason", "", "what the person is asked to allow")
	flag.Parse()
	if *reason == "" {
		fmt.Fprintln(os.Stderr, "cerberus-presence: --reason is required")
		os.Exit(1)
	}
	code, msg := verify(*reason)
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(code)
}
