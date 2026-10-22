package main

import "encoding/binary"

// ---------------------------------------------------------------------------
// Wire format
//
// The Adaptix teamserver <-> beacon-agent protocol is asymmetric on purpose:
//
//   server -> agent : little-endian uint32 integers and little-endian
//                     length prefixes (produced by the Go extender PackArray)
//   agent  -> server : big-endian uint32 integers and big-endian length
//                     prefixes (produced by the C++ Packer of the Windows
//                     beacon, which this agent reproduces byte for byte)
//
// `reader` therefore decodes little-endian and `writer` encodes big-endian.
// ---------------------------------------------------------------------------

type reader struct {
	buf []byte
	pos int
}

func newReader(b []byte) *reader { return &reader{buf: b} }

func (r *reader) rem() int { return len(r.buf) - r.pos }

func (r *reader) u8() uint8 {
	if r.rem() < 1 {
		return 0
	}
	v := r.buf[r.pos]
	r.pos++
	return v
}

func (r *reader) u16() uint16 {
	if r.rem() < 2 {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return v
}

func (r *reader) u32() uint32 {
	if r.rem() < 4 {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v
}

func (r *reader) u64() uint64 {
	if r.rem() < 8 {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return v
}

// bytes reads a little-endian length prefixed byte slice.
func (r *reader) bytes() []byte {
	n := int(r.u32())
	if n <= 0 || n > r.rem() {
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

// str reads a little-endian length prefixed string, trimming NUL padding.
func (r *reader) str() string { return string(trimNul(r.bytes())) }

type writer struct {
	buf []byte
}

func newWriter() *writer { return &writer{buf: make([]byte, 0, 64)} }

func (w *writer) u8(v uint8) { w.buf = append(w.buf, v) }

func (w *writer) u16(v uint16) {
	w.buf = append(w.buf, byte(v>>8), byte(v))
}

func (w *writer) u32(v uint32) {
	w.buf = append(w.buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func (w *writer) u64(v uint64) {
	w.buf = append(w.buf,
		byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// flat appends raw bytes with no length prefix.
func (w *writer) flat(b []byte) { w.buf = append(w.buf, b...) }

// bytes appends a big-endian length prefixed byte slice (Packer::PackBytes).
func (w *writer) bytes(b []byte) {
	w.u32(uint32(len(b)))
	w.flat(b)
}

// str appends a big-endian length prefixed string (Packer::PackStringA without
// the trailing NUL, which the extender trims anyway).
func (w *writer) str(s string) { w.bytes([]byte(s)) }

func trimNul(b []byte) []byte {
	i := len(b)
	for i > 0 && b[i-1] == 0 {
		i--
	}
	return b[:i]
}
