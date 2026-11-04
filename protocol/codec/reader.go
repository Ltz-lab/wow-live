// Package codec implements the byte- and bit-level reader and writer used by
// World of Warcraft: Forever 1.60.1.70707 message bodies.
//
// Conventions [observed in the decrypted client, docs/EVIDENCE.md E21]:
//
//   - Integers and floats are little-endian.
//   - Bit fields are packed most-significant bit first: the first flag is bit 7
//     of its byte (the client extracts flags with `lsr w, #7`, `ubfx w, #6, 1`,
//     ...). A multi-bit value is read high bit first and may span bytes.
//   - Every byte-sized read discards the unread bits of a partly read byte, and
//     every byte-sized write first flushes pending bits. The client reads each
//     flag group as whole bytes, so a group always starts on a byte boundary.
//   - Object GUIDs are 128-bit and packed: a little-endian uint16 mask (low byte
//     for the low 64 bits, high byte for the high 64 bits), then the non-zero
//     bytes of the low half and then the high half, least significant first
//     (client reader 0x1014bf588).
//
// Per-message layouts live in their own packages and are added only once
// checked against the client's decoders.
package codec

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

var (
	// ErrUnderflow means a read ran past the end of the buffer.
	ErrUnderflow = errors.New("codec: read past end of buffer")
	// ErrPackedGUID means a packed GUID mask or byte count is impossible.
	ErrPackedGUID = errors.New("codec: malformed packed guid")
)

// GUID is a 128-bit object GUID as the 70707 client stores it: [Lo, Hi].
type GUID struct {
	Lo, Hi uint64
}

// IsEmpty reports whether g is the empty GUID.
func (g GUID) IsEmpty() bool { return g.Lo == 0 && g.Hi == 0 }

// String formats g as Hi:Lo in hex.
func (g GUID) String() string { return fmt.Sprintf("%016x%016x", g.Hi, g.Lo) }

// Reader reads a message body.
type Reader struct {
	buf []byte
	pos int  // next unread byte
	cur byte // byte being consumed by bit reads
	bit uint // unread bits left in cur, 0..7
}

// NewReader returns a Reader over b. The Reader does not copy b.
func NewReader(b []byte) *Reader { return &Reader{buf: b} }

// Remaining reports how many whole bytes are unread. Bits left in a partly
// read byte are not counted: the next byte read discards them.
func (r *Reader) Remaining() int { return len(r.buf) - r.pos }

// Pos reports the byte offset of the next byte read.
func (r *Reader) Pos() int { return r.pos }

// Aligned reports whether no bits of a partly read byte are pending.
func (r *Reader) Aligned() bool { return r.bit == 0 }

// ResetBits discards the unread bits of a partly read byte.
func (r *Reader) ResetBits() { r.bit = 0 }

// Bit reads one bit.
func (r *Reader) Bit() (bool, error) {
	v, err := r.Bits(1)
	return v != 0, err
}

// Bits reads n (0..64) bits, most significant first, and returns them
// right-aligned.
func (r *Reader) Bits(n uint) (uint64, error) {
	if n > 64 {
		return 0, fmt.Errorf("codec: cannot read %d bits at once", n)
	}
	var out uint64
	for i := uint(0); i < n; i++ {
		if r.bit == 0 {
			if r.pos >= len(r.buf) {
				return 0, ErrUnderflow
			}
			r.cur = r.buf[r.pos]
			r.pos++
			r.bit = 8
		}
		r.bit--
		out = out<<1 | uint64(r.cur>>r.bit&1)
	}
	return out, nil
}

func (r *Reader) take(n int) ([]byte, error) {
	r.bit = 0
	if n < 0 || len(r.buf)-r.pos < n {
		return nil, ErrUnderflow
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// Uint8 reads one byte.
func (r *Reader) Uint8() (uint8, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// Uint16 reads a little-endian uint16.
func (r *Reader) Uint16() (uint16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return uint16(b[0]) | uint16(b[1])<<8, nil
}

// Uint32 reads a little-endian uint32.
func (r *Reader) Uint32() (uint32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24, nil
}

// Uint64 reads a little-endian uint64.
func (r *Reader) Uint64() (uint64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v, nil
}

// Int32 reads a little-endian int32.
func (r *Reader) Int32() (int32, error) {
	v, err := r.Uint32()
	return int32(v), err
}

// Float32 reads a little-endian IEEE-754 float.
func (r *Reader) Float32() (float32, error) {
	v, err := r.Uint32()
	return math.Float32frombits(v), err
}

// Bytes reads n bytes and returns a copy.
func (r *Reader) Bytes(n int) ([]byte, error) {
	b, err := r.take(n)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), b...), nil
}

// CString reads a NUL-terminated string.
func (r *Reader) CString() (string, error) {
	r.bit = 0
	for i := r.pos; i < len(r.buf); i++ {
		if r.buf[i] == 0 {
			s := string(r.buf[r.pos:i])
			r.pos = i + 1
			return s, nil
		}
	}
	return "", ErrUnderflow
}

// GUID reads a packed 128-bit GUID (see the package comment).
func (r *Reader) GUID() (GUID, error) {
	mask, err := r.Uint16()
	if err != nil {
		return GUID{}, err
	}
	b, err := r.take(bits.OnesCount16(mask))
	if err != nil {
		return GUID{}, err
	}
	var g GUID
	k := 0
	for i := 0; i < 16; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		if i < 8 {
			g.Lo |= uint64(b[k]) << (8 * uint(i))
		} else {
			g.Hi |= uint64(b[k]) << (8 * uint(i-8))
		}
		k++
	}
	return g, nil
}

// PackedGUID reads a legacy 64-bit packed GUID (one mask byte). The 70707
// client does not use this form; it remains for the synthetic replay fixtures.
func (r *Reader) PackedGUID() (uint64, error) {
	mask, err := r.Uint8()
	if err != nil {
		return 0, err
	}
	b, err := r.take(bits.OnesCount8(mask))
	if err != nil {
		return 0, err
	}
	var guid uint64
	k := 0
	for i := 0; i < 8; i++ {
		if mask&(1<<uint(i)) != 0 {
			guid |= uint64(b[k]) << (8 * uint(i))
			k++
		}
	}
	return guid, nil
}

// Reset rewinds the reader to the start of the buffer.
func (r *Reader) Reset() { r.pos, r.bit = 0, 0 }
