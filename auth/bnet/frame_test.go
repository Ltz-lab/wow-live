package bnet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"testing"
)

// The golden request frame is what the client's request builder produces
// (0x1022dce60): service_id explicitly 0, method_id, token, size and the
// fixed32 service_hash, in field order, behind a big-endian u16 header length.
// 0x5dbb51c2 is GameUtilities (stub 0x10239446c), method 1 is ProcessTask.
func TestGoldenRequestFrame(t *testing.T) {
	f := Frame{
		Header: Header{ServiceID: 0, MethodID: 1, Token: 5, ServiceHash: 0x5dbb51c2},
		Body:   []byte{0xaa, 0xbb, 0xcc},
	}
	got, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("000d" + // u16 BE header length = 13
		"0800" + "1001" + "1805" + "2803" + "5dc251bb5d" + // service_id 0, method 1, token 5, size 3, hash
		"aabbcc")
	if !bytes.Equal(got, want) {
		t.Fatalf("frame\n got %x\nwant %x", got, want)
	}
	back, n, err := Decode(got)
	if err != nil || n != len(got) {
		t.Fatalf("Decode: n=%d err=%v", n, err)
	}
	if back.Header.Size != 3 || back.Header.ServiceHash != 0x5dbb51c2 || !bytes.Equal(back.Body, f.Body) {
		t.Fatalf("round trip %+v", back)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		h    Header
	}{
		{"request", Header{ServiceID: 0, MethodID: 3, Token: 9, ServiceHash: 0x65446991}},
		{"response", Header{ServiceID: 0, Token: 9, IsResponse: true}},
		{"response with status", Header{ServiceID: 0xfe, Token: 70000, IsResponse: true, Status: 0x3ee, ObjectID: 1 << 40}},
		{"client instance id", Header{Token: 1, MethodID: 1, ServiceHash: 1, CIID: "7b1e0b36-8d41-4b6a-9f0e-0123456789ab"}},
		{"timeout and reason", Header{Token: 2, Timeout: 45000000, ErrorReason: "no such method"}},
		{"errors", Header{Token: 3, Errors: []ErrorInfo{
			{ObjectAddress: ObjectAddress{Host: ProcessID{Label: 1, Epoch: 2}, ObjectID: 7}, Status: 5, ServiceHash: 0x9da8116b, MethodID: 1},
			{ObjectAddress: ObjectAddress{Host: ProcessID{Label: 3, Epoch: 4}}, Status: 6, ServiceHash: 1, MethodID: 2},
		}}},
		{"forward and fanout", Header{Token: 4,
			ForwardTargets: []ProcessID{{Label: 9, Epoch: 8}},
			FanoutTargets:  []FanoutTarget{{ClientID: "c", Key: []byte{1, 2}, ObjectID: 3}},
			CIIDFanout:     []string{"a", "b"},
			ClientRecord:   []byte{9}, OriginalSender: []byte{8, 7},
			SenderToken: 12, RouterLabel: 13, SpanTraceID: "trace"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Encode(Frame{Header: tc.h, Body: []byte("payload")})
			if err != nil {
				t.Fatal(err)
			}
			got, n, err := Decode(raw)
			if err != nil || n != len(raw) {
				t.Fatalf("Decode: n=%d err=%v", n, err)
			}
			want := tc.h
			want.Size = 7
			if !reflect.DeepEqual(got.Header, want) {
				t.Fatalf("header\n got %+v\nwant %+v", got.Header, want)
			}
			if string(got.Body) != "payload" {
				t.Fatalf("body %q", got.Body)
			}
		})
	}
}

func TestZeroOptionalFieldsAreOmitted(t *testing.T) {
	raw, err := Encode(Frame{Header: Header{ServiceID: 0, Token: 0}})
	if err != nil {
		t.Fatal(err)
	}
	// u16 length, then only the required service_id and token and the size the
	// client always writes: 08 00 | 18 00 | 28 00.
	want, _ := hex.DecodeString("0006" + "0800" + "1800" + "2800")
	if !bytes.Equal(raw, want) {
		t.Fatalf("frame %x, want %x", raw, want)
	}
}

func TestDecodeRejects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"empty", "", ErrShortFrame},
		{"one byte", "00", ErrShortFrame},
		{"header cut short", "0006" + "0800", ErrShortFrame},
		{"payload cut short", "0006" + "080018002803" + "01", ErrShortFrame},
		{"missing token", "0002" + "0800", ErrMissingRequired},
		{"missing service id", "0002" + "1800", ErrMissingRequired},
		{"empty header", "0000", ErrMissingRequired},
		{"unterminated varint", "0001" + "ff", ErrBadVarint},
		{"wrong wire type for service_id", "0003" + "0a0100", ErrBadMessage},
		{"hash with the wrong wire type", "0006" + "0800" + "1800" + "5801", ErrBadMessage},
		{"group wire type", "0002" + "0b0c", ErrBadMessage},
		{"truncated length-delimited", "0004" + "0800" + "6a05", ErrBadMessage},
		{"size over the cap", "0008" + "0800" + "1800" + "28808040", ErrPayloadTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := hex.DecodeString(stripSpaces(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := Decode(raw); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func stripSpaces(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		if r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestUnknownHeaderFieldsAreSkipped(t *testing.T) {
	// Field 99 (varint) after the required pair; a proto2 parser skips it.
	raw, _ := hex.DecodeString("0009" + "0800" + "1805" + "98060a" + "2800")
	f, _, err := Decode(raw)
	if err != nil || f.Header.Token != 5 {
		t.Fatalf("Decode: %+v %v", f, err)
	}
}

func TestEncodeLimits(t *testing.T) {
	if _, err := Encode(Frame{Body: make([]byte, MaxPayload)}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized payload: %v", err)
	}
	if _, err := Encode(Frame{Body: make([]byte, MaxPayload-1)}); err != nil {
		t.Fatalf("largest payload: %v", err)
	}
	if _, err := Encode(Frame{Header: Header{ClientRecord: make([]byte, 0x10000)}}); err == nil {
		t.Fatal("header beyond the u16 prefix must not encode")
	}
}

func TestVarintHelpers(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1<<32 - 1, 1 << 63} {
		b := appendVarint(nil, v)
		got, n, err := readVarint(b, 0)
		if err != nil || got != v || n != len(b) {
			t.Fatalf("%d: got %d n=%d err=%v", v, got, n, err)
		}
	}
	if _, _, err := readVarint([]byte{0x80, 0x80}, 0); !errors.Is(err, ErrBadVarint) {
		t.Fatalf("truncated: %v", err)
	}
	if _, _, err := readVarint(bytes.Repeat([]byte{0xff}, 11), 0); !errors.Is(err, ErrBadVarint) {
		t.Fatalf("overlong: %v", err)
	}
}

func TestConnReadsFramesAcrossArbitrarySplits(t *testing.T) {
	var wire bytes.Buffer
	c := NewConn(&wire)
	frames := []Frame{
		{Header: Header{MethodID: 1, Token: 1, ServiceHash: 0x65446991}, Body: []byte{1, 2, 3}},
		{Header: Header{Token: 2, IsResponse: true}},
		{Header: Header{MethodID: 1, Token: 3, ServiceHash: 0x5dbb51c2}, Body: bytes.Repeat([]byte{7}, 70000)},
	}
	for _, f := range frames {
		if err := c.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	// Feed one byte at a time to prove the reader does not rely on message
	// boundaries.
	r := NewConn(readWriter{Reader: oneByteReader{bytes.NewReader(wire.Bytes())}})
	for i, want := range frames {
		got, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Header.Token != want.Header.Token || !bytes.Equal(got.Body, want.Body) {
			t.Fatalf("frame %d mismatch: %+v", i, got.Header)
		}
	}
	if _, err := r.ReadFrame(); err != io.EOF {
		t.Fatalf("end of stream: %v", err)
	}
}

func TestConnTruncatedFrameIsUnexpectedEOF(t *testing.T) {
	raw, _ := Encode(Frame{Header: Header{Token: 1}, Body: []byte{1, 2, 3, 4}})
	for _, cut := range []int{3, len(raw) - 1} {
		r := NewConn(readWriter{Reader: bytes.NewReader(raw[:cut])})
		if _, err := r.ReadFrame(); err != io.ErrUnexpectedEOF {
			t.Fatalf("cut at %d: %v", cut, err)
		}
	}
}

func TestConnRejectsOversizedSizeBeforeAllocating(t *testing.T) {
	// size = 1<<20 exactly: the client's `size >> 0x14 == 0` test fails.
	raw, _ := hex.DecodeString("0008" + "0800" + "1800" + "28808040")
	r := NewConn(readWriter{Reader: bytes.NewReader(raw)})
	if _, err := r.ReadFrame(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v", err)
	}
}

// oneByteReader returns at most one byte per Read.
type oneByteReader struct{ r io.Reader }

func (o oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

// readWriter glues independent halves into an io.ReadWriter.
type readWriter struct {
	io.Reader
	io.Writer
}
