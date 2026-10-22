package main

import "sync"

// outbox is the single response accumulator of the agent.
//
// Every producer (command dispatcher, job output pumps, tunnel readers,
// terminal readers) appends complete task records to the same buffer under a
// mutex, exactly like the Windows beacon appends to its single Packer. The main
// loop atomically swaps the buffer out, patching the big-endian total size into
// the leading four bytes.
//
// The first four bytes of the buffer are always a placeholder for the total
// size, because the teamserver expects the size prefix to cover itself.
type outbox struct {
	mu   sync.Mutex
	buf  []byte
	wake chan struct{}
}

func newOutbox() *outbox {
	return &outbox{
		buf:  make([]byte, 4),
		wake: make(chan struct{}, 1),
	}
}

// emit appends one framed record built by fn.
func (o *outbox) emit(fn func(*writer)) {
	w := newWriter()
	fn(w)
	if len(w.buf) == 0 {
		return
	}
	o.mu.Lock()
	o.buf = append(o.buf, w.buf...)
	o.mu.Unlock()
	o.signal()
}

func (o *outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// pending reports whether the buffer holds at least one record.
func (o *outbox) pending() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.buf) > 4
}

// take returns the fully framed response stream and resets the buffer. It
// returns nil when there is nothing to send.
func (o *outbox) take() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.buf) <= 4 {
		return nil
	}
	w := newWriter()
	w.u32(uint32(len(o.buf)))
	out := make([]byte, len(w.buf)+len(o.buf)-4)
	copy(out, w.buf)
	copy(out[4:], o.buf[4:])

	// Keep the allocated array, drop the payload.
	for i := range o.buf {
		o.buf[i] = 0
	}
	o.buf = o.buf[:4]
	return out
}

// reset discards any buffered output.
func (o *outbox) reset() {
	o.mu.Lock()
	o.buf = o.buf[:4]
	for i := range o.buf {
		o.buf[i] = 0
	}
	o.mu.Unlock()
}
