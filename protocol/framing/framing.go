// Package framing models the per-message framing of the world connection.
//
// Established for build 70707 from the decrypted client (docs/EVIDENCE.md E12,
// E15):
//
//   - A connection starts with one newline-terminated ASCII line per direction,
//     with no frame header: "WORLD OF WARCRAFT CONNECTION - SERVER TO CLIENT - V2"
//     from the server, the CLIENT TO SERVER variant from the client. The client
//     buffers at most 128 bytes looking for the newline.
//
//   - Every later message is framed one of two ways, chosen by the transport
//     (factory 0x101093e54):
//
//     Tagged, on a plain stream (the default; what TCP servers speak):
//
//     +0   uint32le  size    opcode + payload length (the tag is not counted)
//     +4   [12]byte  tag     AES-GCM tag; all zero until encryption starts
//     +16  uint32le  opcode
//     +20  bytes     payload
//
//     Untagged (LE32), on a transport that sets up a certificate store and
//     so is already secured; the client refuses GCM mode there:
//
//     +0  uint32le  size    opcode + payload length
//     +4  uint32le  opcode
//     +8  bytes     payload
//
//     Both read the size least-significant byte first and cap it below 1 MiB.
//
// The encrypted phase (AES-GCM over opcode + payload, IV = 64-bit counter ||
// "SRVR"/"CLNT") is not implemented here: Tagged decodes plaintext frames only
// and rejects a non-zero tag, as the client does before encryption starts.
// Per-message payload layouts are out of scope.
package framing

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Handshake strings, byte-for-byte as they appear in the shipped image.
const (
	ClientConnect = "WORLD OF WARCRAFT CONNECTION - CLIENT TO SERVER - V2"
	ServerConnect = "WORLD OF WARCRAFT CONNECTION - SERVER TO CLIENT - V2"
)

var (
	// ErrShortRead means the buffer does not contain a whole header.
	ErrShortRead = errors.New("framing: short header")
	// ErrBadSize means the declared size cannot describe a valid message.
	ErrBadSize = errors.New("framing: implausible declared size")
)

// Header is a decoded message header.
type Header struct {
	// Tag holds the 12 GCM tag bytes of a Tagged or TaggedCrypt frame. It is
	// zero on a plaintext frame.
	Tag [TagLen]byte
	// Size is the declared total size in bytes, header included, as written by
	// the sender. Zero when the layout does not carry a size.
	Size int
	// Opcode is the numeric message id. This build uses 32-bit opcodes.
	Opcode uint32
	// HdrLen is the encoded header length this header was decoded with.
	HdrLen int
}

// HeaderLen reports the encoded header length for this header.
func (h Header) HeaderLen() int { return h.HdrLen }

// BodyLen reports how many bytes follow the header.
func (h Header) BodyLen() int { return h.Size - h.HeaderLen() }

// HeaderCodec encodes and decodes the header of one message.
type HeaderCodec interface {
	// Name identifies the layout for logging and fixtures.
	Name() string
	// HeaderLen reports the encoded header length in bytes.
	HeaderLen() int
	// Decode reads a header from the start of b and reports the header length.
	Decode(b []byte) (Header, int, error)
	// Encode writes a header for a body of bodyLen bytes.
	Encode(opcode uint32, bodyLen int) []byte
}

// LE32 is the untagged frame layout of build 70707, used only on a transport
// that is already secured (one that sets up a certificate store; transport
// kind 1 at 0x101093e54). On a plain stream the client uses Tagged.
//
// Wire format, per message:
//
//	+0  uint32le  size   number of bytes that follow this field
//	+4  uint32le  opcode
//	+8  bytes     payload
//
// The size field therefore counts the opcode plus the payload, and a frame is
// 4 + size bytes long in total. The opcode is the first four bytes of the
// body the client hands to the dispatcher (`ldr w3, [x21]` at 0x101099210).
//
// Status: VERIFIED against the shipped code, for that transport. The size
// field is assembled one byte at a time by the state machine at
// 0x1010932a0-0x1010932ec
// (`orr w8, w9, w8, lsl 8/16/24`), which fixes both the width and the
// byte order.
type LE32 struct {
	// MaxSize is an exclusive upper bound on the declared size. Zero means
	// 0x100000, the limit the client enforces when bit 0 of its stream flags
	// is set (`tbz w8, 0` at 0x101093304). Applying it always is the
	// conservative choice for a peer.
	MaxSize int
}

// Name implements HeaderCodec.
func (LE32) Name() string { return "le32-size-le32-opcode" }

// HeaderLen implements HeaderCodec.
func (LE32) HeaderLen() int { return 8 }

func (p LE32) max() int {
	if p.MaxSize > 0 {
		return p.MaxSize
	}
	return 0x100000
}

// Decode implements HeaderCodec.
func (p LE32) Decode(b []byte) (Header, int, error) {
	if len(b) < 8 {
		return Header{}, 0, ErrShortRead
	}
	size := int(binary.LittleEndian.Uint32(b[0:4]))
	op := binary.LittleEndian.Uint32(b[4:8])
	// The size field counts the opcode, so the smallest legal frame body is
	// the 4 opcode bytes. The client rejects a declared size of 1 MiB or more
	// (`cmp w4, 0x100, lsl 12; b.hs` at 0x101093308), so the limit is
	// exclusive.
	if size < 4 || size >= p.max() {
		return Header{}, 0, fmt.Errorf("%w: %d (max %d)", ErrBadSize, size, p.max())
	}
	return Header{Size: size + 4, Opcode: op, HdrLen: 8}, 8, nil
}

// Encode implements HeaderCodec.
func (p LE32) Encode(opcode uint32, bodyLen int) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(bodyLen+4))
	binary.LittleEndian.PutUint32(buf[4:8], opcode)
	return buf
}

// TagLen is the length of the AES-GCM tag in a Tagged header.
const TagLen = 12

// ErrNonZeroTag means a Tagged frame carries a tag although encryption has
// not started. The client treats that as fatal (error 0x20 at 0x101093de8).
// It is also what an encrypted frame looks like to this plaintext codec.
var ErrNonZeroTag = errors.New("framing: non-zero GCM tag on a plaintext frame (encrypted, or corrupt)")

// Tagged is the frame layout of build 70707 on a plain stream: LE32 with a
// 12-byte AES-GCM tag between the size and the opcode.
//
//	+0   uint32le  size    number of bytes after the tag (opcode + payload)
//	+4   [12]byte  tag     zero until encryption starts
//	+16  uint32le  opcode
//	+20  bytes     payload
//
// A frame is 16 + size bytes long.
//
// Status: VERIFIED against the shipped code (docs/EVIDENCE.md E15). Receive
// 0x101093848: states 2-5 read the size (0x101093c04-0x101093c4c), state 6
// takes exactly 12 more bytes (`cmp x8, 0xc` at 0x101093c58, then
// `add x21, x20, 0xc`; states 7-17 collect them one by one at +0x2251), then
// waits for `size` bytes. With encryption off, the 12 tag bytes are ORed
// together and must be zero (0x101093b70-0x101093bcc). Send 0x101093558
// writes the same 16-byte header, zeroing the tag when not encrypting
// (0x101093838). This is also the layout of the TrinityCore `forever` fork,
// which serves real 1.60.1 clients.
type Tagged struct {
	// MaxSize is an exclusive upper bound on the declared size; zero means
	// 0x100000 (`cmp w4, 0x100, lsl 12` at 0x101093c70).
	MaxSize int
}

// Name implements HeaderCodec.
func (Tagged) Name() string { return "le32-size-gcm12-le32-opcode" }

// HeaderLen implements HeaderCodec.
func (Tagged) HeaderLen() int { return 4 + TagLen + 4 }

func (p Tagged) max() int {
	if p.MaxSize > 0 {
		return p.MaxSize
	}
	return 0x100000
}

// Decode implements HeaderCodec.
func (p Tagged) Decode(b []byte) (Header, int, error) {
	if len(b) < p.HeaderLen() {
		return Header{}, 0, ErrShortRead
	}
	size := int(binary.LittleEndian.Uint32(b[0:4]))
	if size < 4 || size >= p.max() {
		return Header{}, 0, fmt.Errorf("%w: %d (max %d)", ErrBadSize, size, p.max())
	}
	for _, c := range b[4 : 4+TagLen] {
		if c != 0 {
			return Header{}, 0, ErrNonZeroTag
		}
	}
	op := binary.LittleEndian.Uint32(b[16:20])
	var h Header
	h.Size, h.Opcode, h.HdrLen = 4+TagLen+size, op, p.HeaderLen()
	copy(h.Tag[:], b[4:4+TagLen])
	return h, p.HeaderLen(), nil
}

// Encode implements HeaderCodec. The tag is zero: plaintext phase only.
func (p Tagged) Encode(opcode uint32, bodyLen int) []byte {
	buf := make([]byte, p.HeaderLen())
	binary.LittleEndian.PutUint32(buf[0:4], uint32(bodyLen+4))
	binary.LittleEndian.PutUint32(buf[16:20], opcode)
	return buf
}

// TaggedCrypt is Tagged once encryption is armed: the same bytes, except the
// 12-byte tag is real and no longer required to be zero.
//
// Status: VERIFIED. Receive 0x101093848 handles the tag identically either way
// and only decrypts when the stream's flag bit 3 is set (EVIDENCE.md E15), so
// the layout does not change when the cipher comes on; only the tag's meaning
// does.
type TaggedCrypt struct {
	// MaxSize is an exclusive upper bound on the declared size; zero means
	// 0x100000.
	MaxSize int
}

// Name implements HeaderCodec.
func (TaggedCrypt) Name() string { return "le32-size-gcm12-le32-opcode-encrypted" }

// HeaderLen implements HeaderCodec.
func (TaggedCrypt) HeaderLen() int { return 4 + TagLen + 4 }

func (p TaggedCrypt) max() int {
	if p.MaxSize > 0 {
		return p.MaxSize
	}
	return 0x100000
}

// Decode implements HeaderCodec. A tag of all zeroes is accepted: it is what a
// sender that has not armed its cipher yet writes.
//
// The four bytes behind the tag are the opcode while the tag is zero, and the
// first four bytes of the ciphertext once it is not. The client reads the
// header area and the server keeps it in its 20-byte header struct
// (IncomingPacketHeader: Size, Tag[12], EncryptedOpcode), but the payload sits
// directly behind the tag, so a sealed frame consumes 16 header bytes and its
// body is the whole ciphertext — the opcode only appears once it is decrypted.
func (p TaggedCrypt) Decode(b []byte) (Header, int, error) {
	if len(b) < p.HeaderLen() {
		return Header{}, 0, ErrShortRead
	}
	size := int(binary.LittleEndian.Uint32(b[0:4]))
	if size < 4 || size >= p.max() {
		return Header{}, 0, fmt.Errorf("%w: %d (max %d)", ErrBadSize, size, p.max())
	}
	var h Header
	h.Size, h.HdrLen = 4+TagLen+size, 4+TagLen
	copy(h.Tag[:], b[4:4+TagLen])
	if h.Tag == ([TagLen]byte{}) {
		h.Opcode = binary.LittleEndian.Uint32(b[16:20])
		h.HdrLen = p.HeaderLen()
	}
	return h, h.HdrLen, nil
}

// Encode implements HeaderCodec. It writes a zero tag; use EncodeTag for a
// sealed frame.
func (p TaggedCrypt) Encode(opcode uint32, bodyLen int) []byte {
	return p.EncodeTag(opcode, bodyLen, [TagLen]byte{})
}

// EncodeTag returns a 20-byte header carrying a real tag: size, tag, and the
// first four bytes of the ciphertext (which is what the receiver keeps in its
// header struct). opcode fills that slot for a cleartext frame; for a sealed
// one the caller overwrites [16:20] with the head of its ciphertext and writes
// the rest of it after the header.
func (p TaggedCrypt) EncodeTag(opcode uint32, bodyLen int, tag [TagLen]byte) []byte {
	buf := make([]byte, p.HeaderLen())
	binary.LittleEndian.PutUint32(buf[0:4], uint32(bodyLen+4))
	copy(buf[4:4+TagLen], tag[:])
	binary.LittleEndian.PutUint32(buf[16:20], opcode)
	return buf
}

// CipherHead is the offset of the ciphertext head inside an EncodeTag header.
const CipherHead = 4 + TagLen

// EncodeSealed returns a header for a sealed frame: the size is the ciphertext
// length exactly, which already covers the opcode, so unlike EncodeTag nothing
// is added. The first four ciphertext bytes go into the header slot, since that
// is what the receiver keeps and prepends back; ct must be at least 4 long.
func (p TaggedCrypt) EncodeSealed(ct []byte, tag [TagLen]byte) []byte {
	buf := make([]byte, p.HeaderLen())
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(ct)))
	copy(buf[4:4+TagLen], tag[:])
	copy(buf[16:20], ct[:4])
	return buf
}

// Plain is the candidate layout inherited from earlier World of Warcraft
// clients: a big-endian uint16 total size (header included) followed by a
// big-endian uint16 opcode.
//
// Status: CONTRADICTED by the shipped client. The decrypted code reads a
// 32-bit little-endian size and a 32-bit little-endian opcode, so this layout
// cannot describe a frame of build 70707. It is kept only as the historical
// hypothesis and for the regression test that records why it was dropped.
type Plain struct {
	// MaxSize bounds an acceptable declared size. Zero means 0x4000, the
	// historic split point above which clients switch to extended headers.
	MaxSize int
}

// Name implements HeaderCodec.
func (Plain) Name() string { return "plain-u16be-size-u16be-opcode" }

// HeaderLen implements HeaderCodec.
func (Plain) HeaderLen() int { return 4 }

func (p Plain) max() int {
	if p.MaxSize > 0 {
		return p.MaxSize
	}
	return 0x4000
}

// Decode implements HeaderCodec.
func (p Plain) Decode(b []byte) (Header, int, error) {
	if len(b) < 4 {
		return Header{}, 0, ErrShortRead
	}
	size := int(binary.BigEndian.Uint16(b[0:2]))
	op := binary.BigEndian.Uint16(b[2:4])
	if size < 4 || size > p.max() {
		return Header{}, 0, fmt.Errorf("%w: %d (max %d)", ErrBadSize, size, p.max())
	}
	return Header{Size: size, Opcode: uint32(op), HdrLen: 4}, 4, nil
}

// Encode implements HeaderCodec.
func (p Plain) Encode(opcode uint32, bodyLen int) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint16(buf[0:2], uint16(bodyLen+4))
	binary.BigEndian.PutUint16(buf[2:4], uint16(opcode))
	return buf
}

// Message is a decoded protocol message.
type Message struct {
	Opcode uint32
	Body   []byte
	// Tag is the frame's 12 GCM tag bytes when the codec carries one.
	Tag [TagLen]byte
	// Handshake is true for the connect line that precedes the framed phase.
	// Opcode is zero for it: the line carries no header.
	Handshake bool
}

// MaxHandshakeLine is the longest handshake line the client buffers before it
// drops the connection (`cmp x9, 0x80; b.hs` at 0x1010930c4).
const MaxHandshakeLine = 0x80

// ErrHandshakeTooLong means no newline arrived within MaxHandshakeLine bytes.
var ErrHandshakeTooLong = errors.New("framing: handshake line exceeds 128 bytes")

// ConnectPayload returns the client handshake payload: the connect string with
// no terminator, exactly as shipped (52 bytes).
func ConnectPayload() []byte { return []byte(ClientConnect) }

// ConnectLine returns the client handshake as written to the wire: the connect
// string followed by a newline, with no frame header.
//
// The receive side is observed: the client finds the server's line with
// memchr(buf, '\n', len) and checks the 52 bytes before it. That the client's
// own line also ends in '\n' is inferred from that symmetry; the 52-byte length
// the client passes to its writer (0x103179e10) excludes the terminator.
func ConnectLine() []byte { return append([]byte(ClientConnect), '\n') }

// IsConnectPayload reports whether body is one of the two shipped handshake
// strings. It is used to sanity-check a capture before trusting it.
func IsConnectPayload(body []byte) bool {
	s := string(body)
	return s == ClientConnect || s == ServerConnect
}

// Frame returns one complete framed message.
func Frame(codec HeaderCodec, opcode uint32, body []byte) []byte {
	out := make([]byte, 0, len(body)+codec.HeaderLen())
	out = append(out, codec.Encode(opcode, len(body))...)
	return append(out, body...)
}

// Stream reassembles messages from a byte stream for a given codec.
type Stream struct {
	codec     HeaderCodec
	buf       []byte
	awaitLine bool
}

// NewStream returns a Stream that expects framed messages only.
func NewStream(codec HeaderCodec) *Stream { return &Stream{codec: codec} }

// NewSessionStream returns a Stream that first expects one newline-terminated
// handshake line and then framed messages, the way a world connection starts.
func NewSessionStream(codec HeaderCodec) *Stream {
	return &Stream{codec: codec, awaitLine: true}
}

// Push appends bytes and returns every complete message now available.
func (s *Stream) Push(b []byte) ([]Message, error) {
	s.buf = append(s.buf, b...)
	var out []Message
	if s.awaitLine {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) >= MaxHandshakeLine {
				return out, ErrHandshakeTooLong
			}
			return out, nil
		}
		if i >= MaxHandshakeLine {
			return out, ErrHandshakeTooLong
		}
		line := make([]byte, i)
		copy(line, s.buf[:i])
		out = append(out, Message{Body: line, Handshake: true})
		s.buf = s.buf[i+1:]
		s.awaitLine = false
	}
	for {
		if len(s.buf) < s.codec.HeaderLen() {
			return out, nil
		}
		h, hlen, err := s.codec.Decode(s.buf)
		if err != nil {
			return out, err
		}
		if h.Size > len(s.buf) {
			return out, nil // wait for the rest
		}
		body := make([]byte, h.Size-hlen)
		copy(body, s.buf[hlen:h.Size])
		out = append(out, Message{Opcode: h.Opcode, Body: body, Tag: h.Tag})
		s.buf = s.buf[h.Size:]
	}
}

// Buffered reports how many unparsed bytes are held.
func (s *Stream) Buffered() int { return len(s.buf) }
