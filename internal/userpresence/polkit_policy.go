package userpresence

import (
	"context"
	_ "embed"
)

// PolkitAction is limited to allowing one passkey enrollment, not approving
// operations. The system administrator installs its action declaration.
const PolkitAction = "com.hollis-labs.cerberus.enroll"

const PolkitPolicyPath = "/usr/share/polkit-1/actions/com.hollis-labs.cerberus.policy"

//go:embed com.hollis-labs.cerberus.policy
var PolkitPolicy string

const LinuxRecovery = "install polkit and its pkcheck/pkttyagent tools, and have the system administrator install the action printed by `cerberus approvals enrollment-policy` at /usr/share/polkit-1/actions/com.hollis-labs.cerberus.policy; then run `cerberus approvals enroll --browser=false` in your SSH terminal and authenticate there"

// Setup describes prerequisites without raising an authentication prompt.
type Setup struct {
	Method    string `json:"method"`
	Available bool   `json:"available"`
	NextStep  string `json:"next_step"`
}

// SetupReporter optionally exposes a verifier's platform-specific recovery.
type SetupReporter interface{ EnrollmentSetup() Setup }

type peerSubject struct {
	PID, UID  int
	StartTime uint64
}
type subjectKey struct{}

// WithPeerSubject is used only by the daemon's kernel peer-credential reader.
// These values must never come from a request body or caller headers.
func WithPeerSubject(ctx context.Context, pid, uid int, start uint64) context.Context {
	return context.WithValue(ctx, subjectKey{}, peerSubject{pid, uid, start})
}

// PeerSubject returns the identity attached by the serving socket. It is not
// a principal claim and must not be populated from HTTP headers.
func PeerSubject(ctx context.Context) (pid, uid int, start uint64, ok bool) {
	s, ok := ctx.Value(subjectKey{}).(peerSubject)
	return s.PID, s.UID, s.StartTime, ok
}
