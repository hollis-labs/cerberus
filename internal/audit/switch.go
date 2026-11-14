package audit

import "sync/atomic"

// Switch is the process's audit sink with a way to divert it. It writes to its primary
// sink, or, while Divert has set one, to that one instead; every service that captured the
// Switch follows the diversion without knowing. Digest always uses the primary sink's key,
// so a digest computed during a diversion still matches the ones stored before it (the
// applied-definition digests, for one).
//
// The daemon's standby mode diverts the main chain to the standby chain: in standby nothing
// may append to the main chain, and an audited read (a host list, a portfolio projection)
// is still recorded, in the standby chain (docs/substrate-port-plan.md section 7.1).
//
// An operation already in flight when the diversion starts or ends records its intent in
// one chain and its outcome in the other; entry and exit are the operator's acts, made in a
// quiet moment.
type Switch struct {
	primary Sink
	alt     atomic.Pointer[sinkBox]
}

type sinkBox struct{ s Sink }

// NewSwitch wraps primary.
func NewSwitch(primary Sink) *Switch { return &Switch{primary: primary} }

// Divert sends every write to to, until Divert(nil).
func (s *Switch) Divert(to Sink) {
	if to == nil {
		s.alt.Store(nil)
		return
	}
	s.alt.Store(&sinkBox{s: to})
}

// Diverted reports whether writes go somewhere other than the primary sink.
func (s *Switch) Diverted() bool { return s.alt.Load() != nil }

// Primary is the sink writes go to when not diverted.
func (s *Switch) Primary() Sink { return s.primary }

// Write writes rec to the current sink.
func (s *Switch) Write(rec Record) (Record, error) {
	if b := s.alt.Load(); b != nil {
		return b.s.Write(rec)
	}
	return s.primary.Write(rec)
}

// Digest is the primary sink's keyed digest.
func (s *Switch) Digest(args any) string { return s.primary.Digest(args) }
