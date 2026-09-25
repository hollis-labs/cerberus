package audit

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
)

var randReader = rand.Reader

// Memory is a Sink that keeps records in memory and chains them like a
// FileSink. It is for tests: a service constructed with it still records
// everything, observably, so no test runs against a silent no-op.
type Memory struct {
	mu      sync.Mutex
	key     []byte
	records []Record
}

// NewMemory returns an empty in-memory sink.
func NewMemory() *Memory {
	key := make([]byte, 32)
	_, _ = randReader.Read(key)
	return &Memory{key: key}
}

// Write implements Sink.
func (m *Memory) Write(rec Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec.Version = SchemaVersion
	rec.Seq = uint64(len(m.records)) + 1
	if rec.ID == "" {
		rec.ID = NewID()
	}
	if rec.Posture == "" {
		rec.Posture = PostureSecure
	}
	if len(m.records) > 0 {
		rec.PrevHash = m.records[len(m.records)-1].Hash
	}
	rec.Hash = hashRecord(rec)
	m.records = append(m.records, rec)
	return rec, nil
}

// Digest implements Sink.
func (m *Memory) Digest(args any) string { return digest(m.key, args) }

// Records returns a copy of every record written.
func (m *Memory) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record(nil), m.records...)
}

// ErrUnavailable is what Failing returns.
var ErrUnavailable = errors.New("audit log unwritable")

// Failing is a Sink whose every write fails, for testing what an operation
// does when its record cannot be written.
type Failing struct{}

// Write implements Sink.
func (Failing) Write(Record) (Record, error) { return Record{}, ErrUnavailable }

// Digest implements Sink.
func (Failing) Digest(any) string { return "" }

// Unavailable is the sink of a process whose audit directory could not be
// opened. Every write fails with the reason, so non-read operations are
// refused and reads are logged (Decision 8) — the failure is never silent.
type Unavailable struct{ Err error }

// Write implements Sink.
func (u Unavailable) Write(Record) (Record, error) {
	return Record{}, fmt.Errorf("%w: %w", ErrUnavailable, u.Err)
}

// Digest implements Sink.
func (Unavailable) Digest(any) string { return "" }
