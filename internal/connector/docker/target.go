package docker

import (
	"fmt"
	"strings"
)

// Target selects which Docker daemon an operation runs against.
//
// It is per-operation, not per-process: the connector is constructed by a
// registry factory on every call, and the target is read from that call's
// config. One daemon therefore serves several Docker hosts without any of them
// becoming the process-wide default.
//
// The zero Target means "whatever the caller's own environment resolves to",
// which is the behavior every Docker operation had before host selection
// existed.
type Target struct {
	// Host is a DOCKER_HOST value — ssh://user@host, tcp://host:2376, or a
	// unix:// socket path.
	Host string
	// Context is a docker context name, as `docker context ls` lists it.
	Context string
}

// TargetFromConfig reads host selection out of an operation's config, under the
// key names the connector's own input schemas declare.
func TargetFromConfig(cfg map[string]any) (Target, error) {
	target := Target{
		Host:    trimmedString(cfg, "host"),
		Context: trimmedString(cfg, "context"),
	}
	return target, target.Validate()
}

func trimmedString(cfg map[string]any, key string) string {
	value, _ := cfg[key].(string)
	return strings.TrimSpace(value)
}

// IsZero reports whether the target selects the caller's default daemon.
func (t Target) IsZero() bool { return t.Host == "" && t.Context == "" }

// Validate rejects a target naming both a host and a context.
//
// The docker CLI resolves the conflict silently rather than refusing: verified
// against docker 24.0.2, an explicit --context wins and DOCKER_HOST is ignored,
// with nothing printed to say so. Accepting both would run the operation
// against a daemon the caller did not ask for, which for `destroy` is the worst
// possible place to be wrong.
func (t Target) Validate() error {
	if t.Host != "" && t.Context != "" {
		return fmt.Errorf("docker host %q and docker context %q are mutually exclusive; set one or the other", t.Host, t.Context)
	}
	return nil
}

// Describe names the target for operator-facing error text.
//
// This exists because the docker CLI will not name it. Every ssh:// transport
// failure is reported as "Cannot connect to the Docker daemon at
// http://docker.example.com" — a placeholder that is the same string whichever
// host was asked for, so an operator reading it cannot tell which machine
// refused, or even that a remote one was involved.
func (t Target) Describe() string {
	switch {
	case t.Host != "":
		return t.Host
	case t.Context != "":
		return "docker context " + t.Context
	default:
		return "the local Docker daemon"
	}
}

// masksFailureCause reports whether the CLI hides why a connection failed on
// this target's transport.
//
// Only ssh:// does. It shells out to `ssh -- <host> docker system dial-stdio`
// and logs the child's stderr — where the real reason lives — at debug level
// only. tcp:// and unix:// report a usable error on their own, so they are left
// on the CLI's default logging.
func (t Target) masksFailureCause() bool {
	return strings.HasPrefix(t.Host, "ssh://")
}
