package codec

import (
	"errors"
	"math"
)

// Writer builds a message body with the conventions in the package comment.
// Byte-sized writes first flush pending bits.
type Writer struct {
	buf []byte
	bit uint // bits already used in the last byte, 0 = none pending
}

// NewWriter returns an empty Writer.
func NewWriter() *Writer { return &Writer{} }

// NewWriterCap returns an empty Writer with capacity n.
func NewWriterCap(n int) *Writer { return &Writer{buf: make([]byte, 0, n)} }

// Len reports the number of bytes written so far, including a partly filled one.
func (w *Writer) Len() int { return len(w.buf) }

// Bytes returns the written bytes. A partly filled byte is included, padded
// with zero bits.
func (w *Writer) Bytes() []byte { return w.buf }

// Bit appends one bit.
func (w *Writer) Bit(v bool) {
	var b uint64
	if v {
		b = 1
	}
	_ = w.Bits(b, 1)
}

// Bits appends the low n bits of v (0..64), most significant first.
func (w *Writer) Bits(v uint64, n uint) error {
	if n > 64 {
		return errors.New("codec: cannot write more than 64 bits at once")
	}
	for i := n; i > 0; i-- {
		if w.bit == 0 {
			w.buf = append(w.buf, 0)
		}
		if v>>(i-1)&1 != 0 {
			w.buf[len(w.buf)-1] |= 0x80 >> w.bit
		}
		w.bit = (w.bit + 1) % 8
	}
	return nil
}

// FlushBits pads the current byte with zero bits.
func (w *Writer) FlushBits() { w.bit = 0 }

func (w *Writer) put(b ...byte) {
	w.bit = 0
	w.buf = append(w.buf, b...)
}

// Uint8 appends one byte.
func (w *Writer) Uint8(v uint8) { w.put(v) }

// Uint16 appends a little-endian uint16.
func (w *Writer) Uint16(v uint16) { w.put(byte(v), byte(v>>8)) }

// Uint32 appends a little-endian uint32.
func (w *Writer) Uint32(v uint32) { w.put(byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }

// Uint64 appends a little-endian uint64.
func (w *Writer) Uint64(v uint64) {
	for i := 0; i < 8; i++ {
		w.put(byte(v >> (8 * uint(i))))
	}
}

// Int32 appends a little-endian int32.
func (w *Writer) Int32(v int32) { w.Uint32(uint32(v)) }

// Float32 appends a little-endian IEEE-754 float.
func (w *Writer) Float32(v float32) { w.Uint32(math.Float32bits(v)) }

// RawBytes appends raw bytes.
func (w *Writer) RawBytes(b []byte) { w.put(b...) }

// CString appends a NUL-terminated string.
func (w *Writer) CString(s string) {
	w.put([]byte(s)...)
	w.put(0)
}

// GUID appends a packed 128-bit GUID.
func (w *Writer) GUID(g GUID) {
	var mask uint16
	var out []byte
	for i := 0; i < 16; i++ {
		var b byte
		if i < 8 {
			b = byte(g.Lo >> (8 * uint(i)))
		} else {
			b = byte(g.Hi >> (8 * uint(i-8)))
		}
		if b != 0 {
			mask |= 1 << uint(i)
			out = append(out, b)
		}
	}
	w.Uint16(mask)
	w.put(out...)
}

// PackedGUID appends a legacy 64-bit packed GUID (synthetic fixtures only).
func (w *Writer) PackedGUID(guid uint64) {
	var mask byte
	var out []byte
	for i := 0; i < 8; i++ {
		if b := byte(guid >> (8 * uint(i))); b != 0 {
			mask |= 1 << uint(i)
			out = append(out, b)
		}
	}
	w.put(mask)
	w.put(out...)
}
