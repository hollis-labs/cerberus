package outpostexecutor

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// Bound outstanding filesystem workers too: a stalled network filesystem may
// ignore cancellation inside a syscall. The caller still returns by its deadline,
// and at most four workers can remain in kernel I/O until that filesystem recovers.
var fileIO = make(chan struct{}, 4)

func readRegular(ctx context.Context, path string, limit int64, fromEnd bool) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	select {
	case fileIO <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	type result struct {
		b         []byte
		truncated bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-fileIO }()
		b, truncated, err := readRegularFile(ctx, path, limit, fromEnd)
		done <- result{b, truncated, err}
	}()
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case r := <-done:
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return r.b, r.truncated, r.err
	}
}

func readRegularFile(ctx context.Context, path string, limit int64, fromEnd bool) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// NONBLOCK prevents a FIFO open from waiting for a writer. fstat checks the
	// opened object, so replacing a pathname between inspection and open cannot
	// substitute a special file. Symlinks to ordinary log files remain supported.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- operator-owned plist/log path, checked on the open descriptor.
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("unit file is not a regular file")
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	truncated := info.Size() > limit
	if fromEnd && truncated {
		if _, err = f.Seek(info.Size()-limit, io.SeekStart); err != nil {
			return nil, false, err
		}
	} else if !fromEnd && truncated {
		return nil, false, fmt.Errorf("unit definition exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		if !fromEnd {
			return nil, false, fmt.Errorf("unit definition exceeds size limit")
		}
		b = b[:limit]
		truncated = true
	}
	return b, truncated, ctx.Err()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
