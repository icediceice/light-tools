package bash

import "bytes"

// captureLimit bounds what a single stream may retain in memory.
//
// Two streams at this size stay comfortably inside the spill store's own
// ceiling (defaultMaximumBytes, 64 MiB in spill.go), including the STDOUT /
// STDERR header runSync prepends. That matters: a retained size the spill
// store would refuse buys nothing, because the only thing we do with output
// past outputLimit is hand it to that store.
const captureLimit = 24 * 1024 * 1024

// boundedBuffer retains at most limit bytes and counts the rest.
//
// The defect it closes: runSync collected into a plain bytes.Buffer and only
// consulted outputLimit AFTER command.Run() returned. That made outputLimit a
// spill threshold rather than a memory bound, so a command that never stops
// writing — `yes`, `cat /dev/urandom | base64` — grew the buffer until the
// process died. The file read path already refuses this class up front
// (filetool.maxReadBytes); this is the same guarantee for command output.
//
// Dropping the tail rather than the head is deliberate: the beginning of a
// runaway stream is where the cause usually is, and the spill keeps whatever
// was retained.
type boundedBuffer struct {
	limit   int
	buf     bytes.Buffer
	dropped int64
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit <= 0 {
		limit = captureLimit
	}
	return &boundedBuffer{limit: limit}
}

// Write always reports the full length as written. A short count with a nil
// error violates the io.Writer contract and would surface as an I/O failure on
// the child process rather than as the deliberate truncation it is.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.dropped += int64(len(p))
		return len(p), nil
	}
	if len(p) <= room {
		return b.buf.Write(p)
	}
	if _, err := b.buf.Write(p[:room]); err != nil {
		return 0, err
	}
	b.dropped += int64(len(p) - room)
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

func (b *boundedBuffer) Dropped() int64 { return b.dropped }

// Limit reports the ceiling actually in force, after the zero-value default
// has been resolved, so a caller reporting it cannot quote a limit that was
// never applied.
func (b *boundedBuffer) Limit() int { return b.limit }
