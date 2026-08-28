package sandbox

import "sync"

// BoundedBuffer is an io.Writer that retains at most Max bytes and discards
// the rest, while ALWAYS reporting a full, successful write to its caller.
//
// The "always claim the full write" contract is the whole point. os/exec
// propagates a short write or an error from the Stdout/Stderr writer by
// tearing the pipe down, which delivers SIGPIPE/EPIPE to the child. A child
// that is killed by its own log volume produces a confusing, non-reproducible
// failure that looks nothing like "the output was too long". So the buffer
// swallows the overflow silently at this layer and records that it did, and
// the caller surfaces truncation explicitly in the tool result instead.
//
// The unbounded-output counterpart of this type is a recorded P0 in this
// estate (helixon-fleet-agent Exec buffered without limit, so a single noisy
// command could exhaust the host's memory). Every command path in this
// package writes through a BoundedBuffer.
type BoundedBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	total     int
	truncated bool
}

// NewBoundedBuffer returns a buffer that retains at most limit bytes. A limit
// of zero or less is replaced with DefaultMaxOutputBytes so a mis-configured
// caller cannot accidentally create an unbounded buffer.
func NewBoundedBuffer(limit int) *BoundedBuffer {
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	return &BoundedBuffer{max: limit, buf: make([]byte, 0, min(limit, 4096))}
}

// Write appends up to the remaining capacity and reports len(p) written with
// a nil error regardless of how much was retained.
func (b *BoundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += len(p)
	room := b.max - len(b.buf)
	if room <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// String returns the retained bytes.
func (b *BoundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// Truncated reports whether any bytes were discarded.
func (b *BoundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Total returns the number of bytes offered to the buffer, including the
// discarded ones.
func (b *BoundedBuffer) Total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Max returns the retention cap.
func (b *BoundedBuffer) Max() int {
	return b.max
}
