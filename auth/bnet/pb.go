package bnet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Protobuf wire helpers. The bgs messages in this package are small and their
// numbering is read straight out of the descriptors embedded in the client
// image (tools/pbdesc.py), so they are hand-coded rather than generated: no
// protobuf dependency, and every field number sits next to its evidence.

// Wire types (protobuf encoding spec).
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

var (
	// ErrShortFrame means the buffer does not hold a complete frame.
	ErrShortFrame = errors.New("bnet: short frame")
	// ErrBadVarint means a protobuf varint was malformed.
	ErrBadVarint = errors.New("bnet: malformed varint")
	// ErrBadMessage means a protobuf message was structurally malformed.
	ErrBadMessage = errors.New("bnet: malformed protobuf message")
	// ErrMissingRequired means a proto2 required field was absent.
	ErrMissingRequired = errors.New("bnet: required field missing")
)

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func readVarint(b []byte, pos int) (uint64, int, error) {
	var v uint64
	var shift uint
	for {
		if pos >= len(b) {
			return 0, pos, ErrBadVarint
		}
		c := b[pos]
		pos++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, pos, nil
		}
		shift += 7
		if shift > 63 {
			return 0, pos, ErrBadVarint
		}
	}
}

// pbWriter appends fields in the order they are written; callers write in
// ascending field-number order, as the client's own serializers do.
type pbWriter struct{ b []byte }

func (w *pbWriter) tag(field, wire int) {
	w.b = appendVarint(w.b, uint64(field)<<3|uint64(wire))
}

func (w *pbWriter) varint(field int, v uint64) {
	w.tag(field, wireVarint)
	w.b = appendVarint(w.b, v)
}

func (w *pbWriter) boolean(field int, v bool) {
	if v {
		w.varint(field, 1)
	} else {
		w.varint(field, 0)
	}
}

func (w *pbWriter) fixed32(field int, v uint32) {
	w.tag(field, wireFixed32)
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
}

func (w *pbWriter) double(field int, v float64) {
	w.tag(field, wireFixed64)
	w.b = binary.LittleEndian.AppendUint64(w.b, math.Float64bits(v))
}

func (w *pbWriter) bytes(field int, v []byte) {
	w.tag(field, wireBytes)
	w.b = appendVarint(w.b, uint64(len(v)))
	w.b = append(w.b, v...)
}

func (w *pbWriter) str(field int, v string) {
	w.tag(field, wireBytes)
	w.b = appendVarint(w.b, uint64(len(v)))
	w.b = append(w.b, v...)
}

// pbField is one parsed key/value. Num is the field number; for varint and
// fixed fields V holds the value, for length-delimited fields B does.
type pbField struct {
	Num  int
	Wire int
	V    uint64
	B    []byte
}

// pbParse splits a message into its fields. It is strict about structure:
// truncated values, group wire types and field number 0 are errors. What each
// field means is left to the caller.
func pbParse(b []byte) ([]pbField, error) {
	var out []pbField
	pos := 0
	for pos < len(b) {
		key, next, err := readVarint(b, pos)
		if err != nil {
			return nil, err
		}
		pos = next
		f := pbField{Num: int(key >> 3), Wire: int(key & 7)}
		if f.Num == 0 {
			return nil, fmt.Errorf("%w: field number 0", ErrBadMessage)
		}
		switch f.Wire {
		case wireVarint:
			f.V, pos, err = readVarint(b, pos)
			if err != nil {
				return nil, err
			}
		case wireFixed64:
			if pos+8 > len(b) {
				return nil, fmt.Errorf("%w: truncated fixed64", ErrBadMessage)
			}
			f.V = binary.LittleEndian.Uint64(b[pos:])
			pos += 8
		case wireFixed32:
			if pos+4 > len(b) {
				return nil, fmt.Errorf("%w: truncated fixed32", ErrBadMessage)
			}
			f.V = uint64(binary.LittleEndian.Uint32(b[pos:]))
			pos += 4
		case wireBytes:
			n, next, err := readVarint(b, pos)
			if err != nil {
				return nil, err
			}
			if n > uint64(len(b)-next) {
				return nil, fmt.Errorf("%w: truncated length-delimited field %d", ErrBadMessage, f.Num)
			}
			f.B = b[next : next+int(n)]
			pos = next + int(n)
		default:
			return nil, fmt.Errorf("%w: wire type %d (field %d)", ErrBadMessage, f.Wire, f.Num)
		}
		out = append(out, f)
	}
	return out, nil
}

// want checks that a known field arrived with the wire type its descriptor
// declares.
func (f pbField) want(wire int, name string) error {
	if f.Wire != wire {
		return fmt.Errorf("%w: %s (field %d) has wire type %d, want %d", ErrBadMessage, name, f.Num, f.Wire, wire)
	}
	return nil
}
