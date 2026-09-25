//go:build !darwin && !linux

package cerbapi

import "errors"

// errPeerCredUnsupported is a platform with no peer-credential reader. The
// socket refuses every request there rather than serve one it cannot
// attribute.
var errPeerCredUnsupported = errors.New("peer credentials are not supported on this platform")

func readPeerCred(int) peerCred { return peerCred{uid: -1, err: errPeerCredUnsupported} }
