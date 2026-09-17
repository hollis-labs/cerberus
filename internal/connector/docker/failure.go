package docker

import (
	"strconv"
	"strings"
)

// placeholderHost is what the docker CLI prints in place of the host it was
// actually given, on every failure of the ssh:// transport. It is a constant,
// not a redaction — docker substitutes the same fake hostname whichever machine
// was asked for.
const placeholderHost = "docker.example.com"

// socketPermissionRecovery is the operator's next step when the remote account
// can reach the host but not the Docker socket on it.
//
// It is an instruction, not a credential, and it has to survive redact.Text
// intact — a safety net that eats the recovery step is worse than no recovery
// step. TestSocketPermissionRecoverySurvivesRedaction is the guard.
const socketPermissionRecovery = "the account can reach the host but not its Docker socket; " +
	"add the account to the docker group there (usermod -aG docker <user>, then reconnect) or run docker under sudo"

// failureReason turns docker's stderr into one operator-readable line.
//
// Two things make the raw text unusable on a remote target. Debug logging —
// which the ssh:// path turns on precisely to recover the cause — prefixes
// every line with a timestamp and wraps the message in a quoted field. And the
// line docker means for a human names placeholderHost rather than the host the
// operation was aimed at, so on its own it says neither where the failure was
// nor what it was.
func failureReason(stderr []byte) string {
	var plain []string
	var cause string
	for _, line := range strings.Split(string(stderr), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, `time="`):
			if c := commandConnCause(line); c != "" && cause == "" {
				cause = c
			}
		default:
			plain = append(plain, line)
		}
	}

	reason := strings.Join(plain, "; ")
	if cause != "" {
		// The cause is strictly better than the sentence naming a fake host, so
		// it replaces it rather than being appended to it.
		if strings.Contains(reason, placeholderHost) {
			reason = "cannot connect to the Docker daemon: " + cause
		} else {
			reason = strings.TrimSpace(reason + " " + cause)
		}
	} else {
		reason = strings.ReplaceAll(reason, " at http://"+placeholderHost, "")
	}

	if recovery := socketPermissionHint(reason); recovery != "" {
		reason += " — " + recovery
	}
	if reason == "" {
		return "docker reported no error output"
	}
	return reason
}

// commandConnCause extracts the child process's own error from one logrus debug
// line. docker logs an ssh transport failure as
//
//	time="..." level=debug msg="commandconn (ssh):dial unix /var/run/docker.sock: connect: permission denied\n"
//
// and nowhere else, at any other log level.
func commandConnCause(line string) string {
	const marker = `msg=`
	index := strings.Index(line, marker)
	if index < 0 {
		return ""
	}
	message := strings.TrimSpace(line[index+len(marker):])
	if unquoted, err := strconv.Unquote(message); err == nil {
		message = unquoted
	}
	message = strings.TrimSpace(message)

	const prefix = "commandconn ("
	if !strings.HasPrefix(message, prefix) {
		return ""
	}
	_, after, found := strings.Cut(message, "):")
	if !found {
		return ""
	}
	return strings.TrimSpace(after)
}

// socketPermissionHint recognizes the one remote-Docker failure with a
// specific, actionable recovery. It is the expected outcome on a host where the
// account exists but is not in the docker group, which is the normal state on a
// corporate host nobody has provisioned for this.
func socketPermissionHint(reason string) string {
	lower := strings.ToLower(reason)
	if !strings.Contains(lower, "permission denied") {
		return ""
	}
	for _, socket := range []string{"docker.sock", "docker daemon socket", "docker api"} {
		if strings.Contains(lower, socket) {
			return socketPermissionRecovery
		}
	}
	return ""
}
