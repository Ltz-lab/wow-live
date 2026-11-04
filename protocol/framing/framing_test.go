package framing

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestHandshakeStringsMatchShippedBytes(t *testing.T) {
	// Lengths and first/last bytes are pinned against the strings recovered from
	// the shipped image so an accidental edit breaks the test.
	if len(ClientConnect) != 52 {
		t.Fatalf("client connect string length = %d, want 52", len(ClientConnect))
	}
	if len(ServerConnect) != 52 {
		t.Fatalf("server connect string length = %d, want 52", len(ServerConnect))
	}
	if ClientConnect[:5] != "WORLD" || ClientConnect[50:] != "V2" {
		t.Fatalf("unexpected client connect string %q", ClientConnect)
	}
	if !IsConnectPayload([]byte(ServerConnect)) || IsConnectPayload([]byte("nope")) {
		t.Fatal("IsConnectPayload misclassified")
	}
}

func TestPlainCodecRoundTrip(t *testing.T) {
	c := Plain{}
	hdr := c.Encode(0x0102, 6)
	if len(hdr) != 4 {
		t.Fatalf("header length = %d", len(hdr))
	}
	h, n, err := c.Decode(append(hdr, make([]byte, 6)...))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != 4 || h.Opcode != 0x0102 || h.Size != 10 || h.BodyLen() != 6 {
		t.Fatalf("decoded %+v (n=%d)", h, n)
	}
}

func TestPlainCodecRejectsShortAndAbsurd(t *testing.T) {
	c := Plain{}
	if _, _, err := c.Decode([]byte{0, 1}); !errors.Is(err, ErrShortRead) {
		t.Fatalf("want ErrShortRead, got %v", err)
	}
	// size = 0 is impossible: a header alone is 4 bytes.
	if _, _, err := c.Decode([]byte{0, 0, 0, 1}); !errors.Is(err, ErrBadSize) {
		t.Fatalf("want ErrBadSize, got %v", err)
	}
}

func TestStreamReassemblesAcrossWrites(t *testing.T) {
	c := Plain{}
	s := NewStream(c)
	frame := Frame(c, 0x0001, ConnectPayload())
	msgs, err := s.Push(frame[:3])
	if err != nil || len(msgs) != 0 {
		t.Fatalf("partial push: msgs=%d err=%v", len(msgs), err)
	}
	msgs, err = s.Push(frame[3:])
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if string(msgs[0].Body) != ClientConnect {
		t.Fatalf("body = %q", msgs[0].Body)
	}
	if s.Buffered() != 0 {
		t.Fatalf("buffered %d bytes", s.Buffered())
	}
}

func TestStreamHandlesTwoFramesInOneWrite(t *testing.T) {
	c := Plain{}
	s := NewStream(c)
	both := append(Frame(c, 1, ConnectPayload()), Frame(c, 2, ConnectPayload())...)
	msgs, err := s.Push(both)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Opcode != 1 || msgs[1].Opcode != 2 {
		t.Fatalf("got %+v", msgs)
	}
}

// TestLE32WireBytes pins the frame layout of build 70707 to the bytes the
// shipped client produces and consumes:
//
//	uint32le size   (bytes that follow: opcode + payload)
//	uint32le opcode
func TestLE32WireBytes(t *testing.T) {
	c := LE32{}
	hdr := c.Encode(0x4E000D, 4)
	want := []byte{
		0x08, 0x00, 0x00, 0x00, // size = 4 (opcode) + 4 (payload)
		0x0d, 0x00, 0x4e, 0x00, // opcode 0x4E000D, little-endian
	}
	if len(hdr) != 8 {
		t.Fatalf("header length = %d, want 8", len(hdr))
	}
	for i := range want {
		if hdr[i] != want[i] {
			t.Fatalf("header byte %d = %#02x, want %#02x (header %x)", i, hdr[i], want[i], hdr)
		}
	}
	h, n, err := c.Decode(append(hdr, make([]byte, 4)...))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != 8 {
		t.Fatalf("decoded header length = %d, want 8", n)
	}
	if h.Opcode != 0x4E000D {
		t.Fatalf("opcode = %#08x, want 0x4E000D", h.Opcode)
	}
	// Size is the whole frame on the wire: the size field plus what it counts.
	if h.Size != 12 {
		t.Fatalf("size = %d, want 12", h.Size)
	}
	if h.BodyLen() != 4 {
		t.Fatalf("body length = %d, want 4", h.BodyLen())
	}
}

func TestLE32RejectsImplausibleSize(t *testing.T) {
	c := LE32{}
	// A size below 4 cannot even hold the opcode.
	if _, _, err := c.Decode([]byte{0, 0, 0, 0, 1, 0, 0, 0}); !errors.Is(err, ErrBadSize) {
		t.Fatalf("want ErrBadSize, got %v", err)
	}
	// The client caps the declared size at 1 MiB.
	big := []byte{0x00, 0x00, 0x10, 0x00, 1, 0, 0, 0}
	if _, _, err := c.Decode(big); !errors.Is(err, ErrBadSize) {
		t.Fatalf("want ErrBadSize, got %v", err)
	}
	if _, _, err := c.Decode([]byte{1, 2, 3}); !errors.Is(err, ErrShortRead) {
		t.Fatalf("want ErrShortRead, got %v", err)
	}
}

func TestConnectLineHasNoHeader(t *testing.T) {
	line := ConnectLine()
	if len(line) != 53 || line[52] != '\n' {
		t.Fatalf("connect line = %q (%d bytes), want 52 bytes + newline", line, len(line))
	}
	if string(line[:52]) != ClientConnect {
		t.Fatalf("payload = %q", line[:52])
	}
}

func TestSessionStreamLineThenFrames(t *testing.T) {
	c := LE32{}
	s := NewSessionStream(c)
	wire := append([]byte(ServerConnect+"\n"), Frame(c, 0x4E000E, []byte{1, 2, 3})...)
	wire = append(wire, Frame(c, 0x4E000D, nil)...)

	// Feed byte by byte: the line and both frames must survive any split.
	var got []Message
	for i := range wire {
		msgs, err := s.Push(wire[i : i+1])
		if err != nil {
			t.Fatalf("Push at %d: %v", i, err)
		}
		got = append(got, msgs...)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	if !got[0].Handshake || string(got[0].Body) != ServerConnect {
		t.Fatalf("handshake = %+v", got[0])
	}
	if got[1].Opcode != 0x4E000E || len(got[1].Body) != 3 {
		t.Fatalf("frame 1 = %+v", got[1])
	}
	if got[2].Opcode != 0x4E000D || len(got[2].Body) != 0 {
		t.Fatalf("frame 2 = %+v", got[2])
	}
	if s.Buffered() != 0 {
		t.Fatalf("buffered %d", s.Buffered())
	}
}

func TestSessionStreamRejectsLongLine(t *testing.T) {
	s := NewSessionStream(LE32{})
	if _, err := s.Push(make([]byte, MaxHandshakeLine)); !errors.Is(err, ErrHandshakeTooLong) {
		t.Fatalf("want ErrHandshakeTooLong, got %v", err)
	}
}

func TestLE32FrameSizeField(t *testing.T) {
	f := Frame(LE32{}, 0x4E000D, []byte("abcd"))
	if got := binary.LittleEndian.Uint32(f[0:4]); got != 8 {
		t.Fatalf("size field = %d, want 8", got)
	}
}

func TestTaggedWireBytes(t *testing.T) {
	c := Tagged{}
	got := Frame(c, 0x4C0001, []byte{0xAA, 0xBB})
	want := []byte{
		0x06, 0, 0, 0, // size = opcode + payload, tag not counted
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // zero tag before encryption
		0x01, 0x00, 0x4C, 0x00, // opcode
		0xAA, 0xBB,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = % x\nwant    % x", got, want)
	}
	h, n, err := c.Decode(got)
	if err != nil || n != 20 || h.Opcode != 0x4C0001 || h.Size != len(got) || h.BodyLen() != 2 {
		t.Fatalf("decode = %+v %d %v", h, n, err)
	}
}

func TestTaggedRejectsNonZeroTagAndBadSize(t *testing.T) {
	c := Tagged{}
	f := Frame(c, 0x4C0001, nil)
	f[4+11] = 1
	if _, _, err := c.Decode(f); !errors.Is(err, ErrNonZeroTag) {
		t.Fatalf("non-zero tag: err = %v", err)
	}
	f = Frame(c, 0x4C0001, nil)
	binary.LittleEndian.PutUint32(f, 0x100000)
	if _, _, err := c.Decode(f); !errors.Is(err, ErrBadSize) {
		t.Fatalf("1 MiB: err = %v", err)
	}
	if _, _, err := c.Decode(f[:19]); !errors.Is(err, ErrShortRead) {
		t.Fatalf("short: err = %v", err)
	}
}

func TestTaggedSessionStreamByteByByte(t *testing.T) {
	wire := append([]byte(ServerConnect+"\n"), Frame(Tagged{}, 0x4E000E, []byte{1, 2, 3})...)
	wire = append(wire, Frame(Tagged{}, 0x470000, nil)...)
	s := NewSessionStream(Tagged{})
	var got []Message
	for _, b := range wire {
		m, err := s.Push([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, m...)
	}
	if len(got) != 3 || !got[0].Handshake || got[1].Opcode != 0x4E000E ||
		!bytes.Equal(got[1].Body, []byte{1, 2, 3}) || got[2].Opcode != 0x470000 || s.Buffered() != 0 {
		t.Fatalf("got %+v, buffered %d", got, s.Buffered())
	}
}

func TestLE32IsNotTagged(t *testing.T) {
	// The two layouts must not be confused: an LE32 frame read as Tagged
	// either is too short or yields a non-zero "tag" from the opcode bytes.
	f := Frame(LE32{}, 0x4C0001, make([]byte, 16))
	if _, _, err := (Tagged{}).Decode(f); err == nil {
		t.Fatal("LE32 frame decoded as Tagged")
	}
}

// TestTaggedCryptSealedLayout pins the sealed frame: the size counts the whole
// ciphertext (opcode included, nothing added), the tag sits at +4 and the first
// four ciphertext bytes fill the header slot the receiver keeps. Getting either
// wrong desyncs the stream by four bytes and fails the GCM tag (E34).
func TestTaggedCryptSealedLayout(t *testing.T) {
	ct := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x11, 0x22}
	var tag [TagLen]byte
	for i := range tag {
		tag[i] = byte(0xF0 + i)
	}
	hdr := TaggedCrypt{}.EncodeSealed(ct, tag)
	if want := (TaggedCrypt{}).HeaderLen(); len(hdr) != want {
		t.Fatalf("header is %d bytes, want %d", len(hdr), want)
	}
	if got := binary.LittleEndian.Uint32(hdr[0:4]); got != uint32(len(ct)) {
		t.Fatalf("size %d, want %d (the ciphertext length, nothing added)", got, len(ct))
	}
	if !bytes.Equal(hdr[4:4+TagLen], tag[:]) {
		t.Fatalf("tag %x", hdr[4:4+TagLen])
	}
	if !bytes.Equal(hdr[16:20], ct[:4]) {
		t.Fatalf("ciphertext head %x, want %x", hdr[16:20], ct[:4])
	}
}

// TestTaggedCryptSealedDecode: a sealed frame decodes to a body that is the
// whole ciphertext, with no opcode taken out of it, because the opcode only
// exists once the frame is decrypted. A zero tag still yields the cleartext
// opcode, stripped from the body.
func TestTaggedCryptSealedDecode(t *testing.T) {
	sealed := append(TaggedCrypt{}.EncodeSealed([]byte{1, 2, 3, 4, 5}, [TagLen]byte{9}), 5)
	h, n, err := TaggedCrypt{}.Decode(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 16 || h.HdrLen != 16 {
		t.Fatalf("consumed %d bytes (hdr %d), want 16", n, h.HdrLen)
	}
	if got := sealed[n:h.Size]; !bytes.Equal(got, []byte{1, 2, 3, 4, 5}) {
		t.Fatalf("body %x, want the whole ciphertext", got)
	}

	clear := append(TaggedCrypt{}.Encode(uint32(0x4400CA), 2), 0x77, 0x88)
	h, n, err = TaggedCrypt{}.Decode(clear)
	if err != nil {
		t.Fatal(err)
	}
	if h.Opcode != 0x4400CA {
		t.Fatalf("cleartext opcode %#x", h.Opcode)
	}
	if got := clear[n:h.Size]; !bytes.Equal(got, []byte{0x77, 0x88}) {
		t.Fatalf("cleartext body %x", got)
	}
}
