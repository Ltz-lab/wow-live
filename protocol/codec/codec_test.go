package codec

import (
	"bytes"
	"errors"
	"testing"
)

// The first flag of a group is bit 7: the create-block reader 0x10085e31c
// takes HasEntityPosition with `lsr w8, w21, #7` and MovementUpdate with
// `and w8, w21, #4` (the sixth flag).
func TestBitsAreMostSignificantFirst(t *testing.T) {
	r := NewReader([]byte{0x84})
	for i, want := range []bool{true, false, false, false, false, true, false, false} {
		got, err := r.Bit()
		if err != nil || got != want {
			t.Fatalf("bit %d = %v err=%v, want %v", i, got, err, want)
		}
	}
	w := NewWriter()
	w.Bit(true)
	w.Bits(0, 4)
	w.Bit(true)
	w.FlushBits()
	if !bytes.Equal(w.Bytes(), []byte{0x84}) {
		t.Fatalf("encoded %x, want 84", w.Bytes())
	}
}

// The spline-move reader (0x100858054) assembles a 16-bit point count that
// starts after one flag bit: 7 bits of byte 0, all of byte 1, the top bit of
// byte 2.
func TestMultiBitValueSpansBytes(t *testing.T) {
	w := NewWriter()
	w.Bit(true)
	w.Bits(0xABCD, 16)
	w.Bits(0b101101, 6)
	w.FlushBits()
	if want := []byte{0x80 | 0xABCD>>9, 0xABCD >> 1 & 0xFF, 0x80 | 0b101101<<1}; !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", w.Bytes(), want)
	}
	r := NewReader(w.Bytes())
	if b, _ := r.Bit(); !b {
		t.Fatal("first bit")
	}
	if v, err := r.Bits(16); err != nil || v != 0xABCD {
		t.Fatalf("16 bits = %#x err=%v", v, err)
	}
	if v, err := r.Bits(6); err != nil || v != 0b101101 {
		t.Fatalf("6 bits = %b err=%v", v, err)
	}
}

func TestByteReadsDiscardPendingBits(t *testing.T) {
	w := NewWriter()
	w.Bit(true)
	w.Uint32(0x01020304) // flushes the pending bit first
	w.Bit(true)
	w.Bit(true)
	w.Uint8(0x55)
	if want := []byte{0x80, 4, 3, 2, 1, 0xC0, 0x55}; !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", w.Bytes(), want)
	}
	r := NewReader(w.Bytes())
	_, _ = r.Bit()
	if v, err := r.Uint32(); err != nil || v != 0x01020304 {
		t.Fatalf("uint32 = %#x err=%v", v, err)
	}
	_, _ = r.Bit()
	if r.Aligned() {
		t.Fatal("one bit read must leave bits pending")
	}
	if v, err := r.Uint8(); err != nil || v != 0x55 {
		t.Fatalf("uint8 = %#x err=%v", v, err)
	}
	if r.Remaining() != 0 {
		t.Fatalf("remaining %d", r.Remaining())
	}
}

func TestGUIDKnownEncoding(t *testing.T) {
	// u16 mask: bit i set for each non-zero byte; low half first.
	g := GUID{Lo: 0x0000000000000102, Hi: 0x0800000000000000}
	w := NewWriter()
	w.GUID(g)
	if want := []byte{0x03, 0x80, 0x02, 0x01, 0x08}; !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", w.Bytes(), want)
	}
	got, err := NewReader(w.Bytes()).GUID()
	if err != nil || got != g {
		t.Fatalf("decoded %v err=%v", got, err)
	}
	w = NewWriter()
	w.GUID(GUID{})
	if !bytes.Equal(w.Bytes(), []byte{0, 0}) {
		t.Fatalf("empty guid encoded %x", w.Bytes())
	}
}

func TestGUIDRoundTrip(t *testing.T) {
	for _, g := range []GUID{{}, {Lo: 1}, {Hi: 1}, {Lo: 0x1122334455667788, Hi: 0x99AABBCCDDEEFF00}} {
		w := NewWriter()
		w.GUID(g)
		got, err := NewReader(w.Bytes()).GUID()
		if err != nil || got != g {
			t.Fatalf("%v -> %v err=%v (bytes %x)", g, got, err, w.Bytes())
		}
	}
}

func TestLegacyPackedGUID(t *testing.T) {
	w := NewWriter()
	w.PackedGUID(0x0102)
	if want := []byte{0x03, 0x02, 0x01}; !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", w.Bytes(), want)
	}
	if got, err := NewReader(w.Bytes()).PackedGUID(); err != nil || got != 0x0102 {
		t.Fatalf("decoded %#x err=%v", got, err)
	}
}

func TestFloatAndCString(t *testing.T) {
	w := NewWriter()
	w.Float32(1.0)
	w.CString("hello")
	if want := append([]byte{0, 0, 0x80, 0x3F}, "hello\x00"...); !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("encoded %x", w.Bytes())
	}
	r := NewReader(w.Bytes())
	if f, err := r.Float32(); err != nil || f != 1.0 {
		t.Fatalf("float %v err=%v", f, err)
	}
	if s, err := r.CString(); err != nil || s != "hello" {
		t.Fatalf("string %q err=%v", s, err)
	}
}

func TestUnderflowIsReported(t *testing.T) {
	if _, err := NewReader([]byte{1, 2}).Uint32(); !errors.Is(err, ErrUnderflow) {
		t.Fatalf("want ErrUnderflow, got %v", err)
	}
	if _, err := NewReader([]byte{0x03, 0x00, 0x01}).GUID(); !errors.Is(err, ErrUnderflow) {
		t.Fatalf("want ErrUnderflow for truncated guid, got %v", err)
	}
	if _, err := NewReader(nil).Bit(); !errors.Is(err, ErrUnderflow) {
		t.Fatalf("want ErrUnderflow for bit, got %v", err)
	}
}
