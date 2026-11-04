// Package world is the world-connection protocol that sits above transport:
// the handshake line pair, the frame stream, the auth exchange, the cipher and
// the dispatch loop.
//
// Everything here is read off the client's own code for build 1.60.1.70707
// (addresses in the comments, write-up in research/encryption.md and
// docs/EVIDENCE.md E15/E18) and cross-checked byte-for-byte against the
// TrinityCore "forever" fork's server side, which agrees on the layout, the
// constants and the challenge orderings.
package world

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"math/bits"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// Sizes of the auth material. The names follow the client's own field offsets
// (connAuth and the NetClient above it, research/encryption.md §3).
const (
	// KeyDataLen is the Battle.net session-key material delivered with the
	// realm-join ticket (client +0x08, 64 bytes).
	KeyDataLen = 64
	// BuildKeyLen is the per-build auth key folded into the digest.
	BuildKeyLen = 16
	// ChallengeLen is the length of both the local and the server challenge.
	ChallengeLen = 32
	// DigestLen is how much of the auth digest is sent.
	DigestLen = 24
	// SessionKeyLen is the length of the derived session key.
	SessionKeyLen = 40
	// EncryptionKeyLen is the length of the GCM key.
	EncryptionKeyLen = 32
)

// ForeverBuildAuthKey is the 16-byte per-build auth key for wowf 1.60.1.70707,
// folded into the AUTH_SESSION digest (see AuthDigest). It is not in the
// shipped file: the client builds it at runtime in an obfuscated, anti-tamper
// provider (installed through the BSS cell 0x104083778, EVIDENCE.md E22/E28).
// Recovered by reading that provider out of a live process and resolving its
// four movz/movk/add/eor word builds — static and live agree
// (docs/EVIDENCE.md E28, /tmp/wowkey/live). The value belongs to the build, not
// to any account.
var ForeverBuildAuthKey = [BuildKeyLen]byte{
	0xb4, 0x51, 0x5b, 0x53, 0x1d, 0xd2, 0x21, 0x3c,
	0xaf, 0x4f, 0x3b, 0xd2, 0x77, 0xe1, 0x4b, 0x8f,
}

// BuildAuthKey70755 is the per-build auth key for wowf 1.60.1.70755. Its
// provider is 0x102c9797c (installed by 0x102c96f0c, the same
// `mov w0,'WoW'; bl; adrp x0,provider; bl` site as 70707's 0x102c95f08). Read
// statically from the decrypted 70755 image: the four words are
// 0x9b4f1c7f-0x165, (0xea145245^0x9c5)+0x8d2, ((0x2d44be0e-0x49d)^0x1103)+0x17e
// and ((0x7653670a-0x883)+0xc9f)^0x202 (the last behind the same return-address
// gate). The same hand reading of 70707's provider reproduces
// ForeverBuildAuthKey exactly, and a Unicorn run of the 70755 bytes gives the
// first two words; it has not been seen in a live 70755 process
// (docs/EVIDENCE.md E31).
var BuildAuthKey70755 = [BuildKeyLen]byte{
	0x1a, 0x1b, 0x4f, 0x9b, 0x52, 0x64, 0x14, 0xea,
	0xf0, 0xa9, 0x44, 0x2d, 0x24, 0x69, 0x53, 0x76,
}

// BuildAuthKey returns the auth key of a known build.
func BuildAuthKey(build int) ([BuildKeyLen]byte, bool) {
	switch build {
	case 70707:
		return ForeverBuildAuthKey, true
	case 70755:
		return BuildAuthKey70755, true
	}
	return [BuildKeyLen]byte{}, false
}

// Seeds, byte-identical to the client's __const and to the fork's constants.
// The client VAs are the second source: 0x10319cc20, 0x10319cc40, 0x10319cc60,
// 0x10319cc80, 0x10319cca0 (research/encryption.md §3).
var (
	authCheckSeed = []byte{
		0xDE, 0x3A, 0x2A, 0x8E, 0x6B, 0x89, 0x52, 0x66, 0x88, 0x9D, 0x7E, 0x7A, 0x77, 0x1D, 0x5D, 0x1F,
		0x4E, 0xD9, 0x0C, 0x23, 0x9B, 0xCD, 0x0E, 0xDC, 0xD2, 0xE8, 0x04, 0x3A, 0x68, 0x64, 0xC7, 0xB0,
	}
	continuedSessionSeed = []byte{
		0x56, 0x5C, 0x61, 0x9C, 0x48, 0x3A, 0x52, 0x1F, 0x61, 0x5D, 0x05, 0x49, 0xB2, 0x9A, 0x39, 0xBF,
		0x4B, 0x97, 0xB0, 0x1B, 0xF9, 0x6C, 0xDE, 0xD6, 0x80, 0x1D, 0xAB, 0x26, 0x02, 0xA9, 0x9B, 0x9D,
	}
	sessionKeySeed = []byte{
		0xE8, 0x1E, 0x8B, 0x59, 0x27, 0x62, 0x1E, 0xAA, 0x86, 0x15, 0x18, 0xEA, 0xC0, 0xBF, 0x66, 0x8C,
		0x6D, 0xBF, 0x83, 0x93, 0xBC, 0xAA, 0x80, 0x52, 0x5B, 0x1E, 0xDC, 0x23, 0xA0, 0x12, 0xB7, 0x50,
	}
	encryptionKeySeed = []byte{
		0x71, 0xC9, 0xED, 0x5A, 0xA7, 0x0E, 0x4D, 0xFF, 0x4C, 0x36, 0xA6, 0x5A, 0x3E, 0x46, 0x8A, 0x4A,
		0x5D, 0xA1, 0x48, 0xC8, 0x30, 0x47, 0x4A, 0xDE, 0xF6, 0x0D, 0x6C, 0xBE, 0x6F, 0xE4, 0x55, 0x73,
	}
	// enableEncryptionSeed keys the HMAC over the ENTER_ENCRYPTED_MODE
	// signature payload (0x10319cca0).
	enableEncryptionSeed = []byte{
		0x66, 0xBE, 0x29, 0x79, 0xEF, 0xF2, 0xD5, 0xB5, 0x61, 0x53, 0xF6, 0x5F, 0x45, 0xAE, 0x81, 0xCB,
		0x32, 0xEC, 0x94, 0xEC, 0x75, 0xB3, 0x5F, 0x44, 0x6A, 0x63, 0x43, 0x67, 0x17, 0x20, 0x44, 0x34,
	}
	// EnableEncryptionContext is the 16-byte ed25519ctx context (0x10319cc10).
	EnableEncryptionContext = []byte{
		0xA7, 0x1F, 0xB6, 0x9B, 0xC9, 0x7C, 0xDD, 0x96, 0xE9, 0xBB, 0xB8, 0x21, 0x39, 0x8D, 0x5A, 0xD4,
	}
)

// Auth material sizes that do not fit the const block above.
var (
	// ErrDoSUnsolved means the hashcash search hit its nonce cap.
	ErrDoSUnsolved = errors.New("world: DoS proof not found")
	// ErrBadZeroBits means the server asked for more zero bits than the client
	// supports (it caps at 48).
	ErrBadZeroBits = errors.New("world: DoS zero bits above 48")
)

func hmacSHA512(key, msg []byte) []byte {
	m := hmac.New(sha512.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// AuthDigest is the 24-byte digest of CMSG_AUTH_SESSION (client 0x1014b6730):
//
//	HMAC-SHA512(SHA512(KeyData ‖ buildKey), LocalChallenge ‖ ServerChallenge ‖ AuthCheckSeed)[0..24)
//
// buildKey may be nil, in which case it is left out of the hash input; the
// client always passes it, and a nil value only exists for a server that skips
// the check.
func AuthDigest(keyData []byte, buildKey []byte, local, server []byte) ([DigestLen]byte, error) {
	var out [DigestLen]byte
	if len(keyData) != KeyDataLen || len(local) != ChallengeLen || len(server) != ChallengeLen {
		return out, errors.New("world: bad digest input size")
	}
	if buildKey != nil && len(buildKey) != BuildKeyLen {
		return out, errors.New("world: bad build key size")
	}
	inner := sha512.New()
	inner.Write(keyData)
	if buildKey != nil {
		inner.Write(buildKey)
	}
	msg := make([]byte, 0, 2*ChallengeLen+len(authCheckSeed))
	msg = append(msg, local...)
	msg = append(msg, server...)
	msg = append(msg, authCheckSeed...)
	copy(out[:], hmacSHA512(inner.Sum(nil), msg))
	return out, nil
}

// sessionKeyGenerator is the fork's SessionKeyGenerator<SHA512>, inlined
// (client 0x1014b6d98): split the seed, hash each half, then iterate
// SHA512(o1 ‖ o0 ‖ o2) until enough bytes exist.
//
// The first o0 is SHA512(o1 ‖ 0 × 64 ‖ o2), not SHA512(seed): the generator
// starts its state at all zeroes and hashes it with the two halves before the
// first output byte (SessionKeyGenerator.h, `o0 = Hash::GetDigestOf(o1, o0, o2)`
// with `o0 = {}`).
func sessionKeyGenerator(seed []byte, n int) []byte {
	half := len(seed) / 2
	o1 := sha512.Sum512(seed[:half])
	o2 := sha512.Sum512(seed[half:])
	buf := make([]byte, 0, n)
	cur := make([]byte, sha512.Size) // all zeroes: the first o0
	for len(buf) < n {
		h := sha512.New()
		h.Write(o1[:])
		h.Write(cur)
		h.Write(o2[:])
		cur = h.Sum(cur[:0])
		buf = append(buf, cur...)
	}
	return buf[:n]
}

// SessionKey derives the 40-byte session key (client 0x1014b6b70). Note the
// challenge order: server first.
func SessionKey(keyData, local, server []byte) ([SessionKeyLen]byte, error) {
	var out [SessionKeyLen]byte
	if len(keyData) != KeyDataLen || len(local) != ChallengeLen || len(server) != ChallengeLen {
		return out, errors.New("world: bad session key input size")
	}
	inner := sha512.Sum512(keyData)
	msg := make([]byte, 0, 2*ChallengeLen+len(sessionKeySeed))
	msg = append(msg, server...)
	msg = append(msg, local...)
	msg = append(msg, sessionKeySeed...)
	copy(out[:], sessionKeyGenerator(hmacSHA512(inner[:], msg), SessionKeyLen))
	return out, nil
}

// EncryptionKey derives the 32-byte GCM key from the session key (client
// 0x1014b706c). The session key is the HMAC key and the challenge order is
// local first.
func EncryptionKey(sessionKey, local, server []byte) ([EncryptionKeyLen]byte, error) {
	var out [EncryptionKeyLen]byte
	if len(sessionKey) != SessionKeyLen || len(local) != ChallengeLen || len(server) != ChallengeLen {
		return out, errors.New("world: bad encryption key input size")
	}
	msg := make([]byte, 0, 2*ChallengeLen+len(encryptionKeySeed))
	msg = append(msg, local...)
	msg = append(msg, server...)
	msg = append(msg, encryptionKeySeed...)
	copy(out[:], hmacSHA512(sessionKey, msg))
	return out, nil
}

// doSZeroBits counts leading zero bits of h the way the client does
// (0x10109b530): whole zero bytes, then the trailing zero bits of the first
// non-zero byte, i.e. counting up from the low bit of byte 0.
func doSZeroBits(h []byte) int {
	n := 0
	for _, b := range h {
		if b == 0 {
			n += 8
			continue
		}
		return n + bits.TrailingZeros8(b)
	}
	return n
}

// DoSProof solves the auth hashcash (client 0x10109b530): the nonce is the
// lowest value whose SHA-256 over
//
//	LocalChallenge ‖ ServerChallenge ‖ Digest ‖ DosChallenge ‖ uint64le(nonce)
//
// has at least zeroBits leading zero bits. digest may be replaced by the
// 40-byte session key for a continued session.
//
// maxNonce bounds the search; pass 0 for "until found".
func DoSProof(local, server, digest, dosChallenge []byte, zeroBits uint8, maxNonce uint64) (uint64, error) {
	if zeroBits > 48 {
		return 0, ErrBadZeroBits
	}
	h := sha256.New()
	nonceBuf := make([]byte, 8)
	for nonce := uint64(0); maxNonce == 0 || nonce < maxNonce; nonce++ {
		h.Reset()
		h.Write(local)
		h.Write(server)
		h.Write(digest)
		h.Write(dosChallenge)
		binary.LittleEndian.PutUint64(nonceBuf, nonce)
		h.Write(nonceBuf)
		if doSZeroBits(h.Sum(nil)) >= int(zeroBits) {
			return nonce, nil
		}
	}
	return 0, ErrDoSUnsolved
}

// DoSCheck verifies a DoS nonce against the same preimage DoSProof searches.
// A server lets a handful of attempts through per nonce, so callers normally
// accept nonce or nonce+1 ... on the same challenge.
func DoSCheck(local, server, digest, dosChallenge []byte, nonce uint64, zeroBits uint8) (bool, error) {
	if zeroBits > 48 {
		return false, ErrBadZeroBits
	}
	h := sha256.New()
	h.Write(local)
	h.Write(server)
	h.Write(digest)
	h.Write(dosChallenge)
	var nb [8]byte
	binary.LittleEndian.PutUint64(nb[:], nonce)
	h.Write(nb[:])
	return doSZeroBits(h.Sum(nil)) >= int(zeroBits), nil
}

// AuthSession is CMSG_AUTH_SESSION (opcode 0x460001), in the client's writer
// order (0x100731a98 / 0x100731af4).
type AuthSession struct {
	DosResponse    uint64
	RegionID       uint32
	BattlegroupID  uint32
	RealmID        uint32
	LocalChallenge [ChallengeLen]byte
	Digest         [DigestLen]byte
	UseIPv6        bool
	Ticket         []byte
}

// Encode serializes the message body, opcode included.
func (a *AuthSession) Encode(opcode uint32) []byte {
	w := codec.NewWriterCap(24 + 8 + 4 + 4 + 4 + ChallengeLen + DigestLen + 4 + len(a.Ticket))
	w.Uint32(opcode)
	w.Uint64(a.DosResponse)
	w.Uint32(a.RegionID)
	w.Uint32(a.BattlegroupID)
	w.Uint32(a.RealmID)
	w.RawBytes(a.LocalChallenge[:])
	w.RawBytes(a.Digest[:])
	w.Bit(a.UseIPv6)
	w.FlushBits()
	w.Uint32(uint32(len(a.Ticket)))
	w.RawBytes(a.Ticket)
	return w.Bytes()
}

// DecodeAuthSessionBody parses a CMSG_AUTH_SESSION body (opcode already
// stripped) back into its fields. It is the inverse of Encode minus the
// leading opcode, so a recorded real-client packet and ours can be compared
// field by field (see replay fingerprinting).
func DecodeAuthSessionBody(body []byte) (AuthSession, error) {
	var a AuthSession
	r := codec.NewReader(body)
	var err error
	if a.DosResponse, err = r.Uint64(); err != nil {
		return a, err
	}
	if a.RegionID, err = r.Uint32(); err != nil {
		return a, err
	}
	if a.BattlegroupID, err = r.Uint32(); err != nil {
		return a, err
	}
	if a.RealmID, err = r.Uint32(); err != nil {
		return a, err
	}
	lc, err := r.Bytes(ChallengeLen)
	if err != nil {
		return a, err
	}
	copy(a.LocalChallenge[:], lc)
	dg, err := r.Bytes(DigestLen)
	if err != nil {
		return a, err
	}
	copy(a.Digest[:], dg)
	bit, err := r.Bit()
	if err != nil {
		return a, err
	}
	a.UseIPv6 = bit
	r.ResetBits()
	n, err := r.Uint32()
	if err != nil {
		return a, err
	}
	if a.Ticket, err = r.Bytes(int(n)); err != nil {
		return a, err
	}
	return a, nil
}

// AuthContinuedSessionLen is the CMSG_AUTH_CONTINUED_SESSION body size.
const AuthContinuedSessionLen = 8 + ChallengeLen + DigestLen + 8 + 4 + 4

// AuthContinuedSession is CMSG_AUTH_CONTINUED_SESSION (opcode 0x460003), the
// auth message of a connection the server asked for with SMSG_CONNECT_TO. It
// is 80 bytes, in the fork's AuthContinuedSession::Read order and the real
// client's (capture 2026-11-04-goldshire-70755 #192): the hashcash nonce, the
// local challenge, the 24-byte digest, then Key, NativeRealmAddress and Key3
// copied from SMSG_CONNECT_TO. There is no ticket: the server finds the
// session key by the account id inside Key.
type AuthContinuedSession struct {
	DosResponse        uint64
	LocalChallenge     [ChallengeLen]byte
	Digest             [DigestLen]byte
	Key                uint64
	NativeRealmAddress uint32
	Key3               uint32
}

// Encode serializes the message body, opcode included.
func (a *AuthContinuedSession) Encode(opcode uint32) []byte {
	w := codec.NewWriterCap(4 + AuthContinuedSessionLen)
	w.Uint32(opcode)
	w.Uint64(a.DosResponse)
	w.RawBytes(a.LocalChallenge[:])
	w.RawBytes(a.Digest[:])
	w.Uint64(a.Key)
	w.Uint32(a.NativeRealmAddress)
	w.Uint32(a.Key3)
	return w.Bytes()
}

// DecodeAuthContinuedSession parses the body (no opcode).
func DecodeAuthContinuedSession(body []byte) (AuthContinuedSession, error) {
	var a AuthContinuedSession
	if len(body) != AuthContinuedSessionLen {
		return a, fmt.Errorf("world: AUTH_CONTINUED_SESSION is %d bytes, want %d", len(body), AuthContinuedSessionLen)
	}
	a.DosResponse = binary.LittleEndian.Uint64(body)
	copy(a.LocalChallenge[:], body[8:])
	copy(a.Digest[:], body[8+ChallengeLen:])
	o := 8 + ChallengeLen + DigestLen
	a.Key = binary.LittleEndian.Uint64(body[o:])
	a.NativeRealmAddress = binary.LittleEndian.Uint32(body[o+8:])
	a.Key3 = binary.LittleEndian.Uint32(body[o+12:])
	return a, nil
}

// ContinuedSessionDigest is the continued-session variant of AuthDigest
// (client 0x1014b69ac): the session key is the HMAC key and the 8-byte
// reconnection key leads the message.
func ContinuedSessionDigest(sessionKey []byte, key uint64, local, server []byte) ([DigestLen]byte, error) {
	var out [DigestLen]byte
	if len(sessionKey) != SessionKeyLen || len(local) != ChallengeLen || len(server) != ChallengeLen {
		return out, errors.New("world: bad continued digest input size")
	}
	msg := make([]byte, 0, 8+2*ChallengeLen+len(continuedSessionSeed))
	var kb [8]byte
	binary.LittleEndian.PutUint64(kb[:], key)
	msg = append(msg, kb[:]...)
	msg = append(msg, local...)
	msg = append(msg, server...)
	msg = append(msg, continuedSessionSeed...)
	copy(out[:], hmacSHA512(sessionKey, msg))
	return out, nil
}

// EnableEncryptionMessage is the payload the server signs: HMAC-SHA512 over
// one enabled byte and the seed (client 0x1014b7218).
func EnableEncryptionMessage(encryptKey []byte, enabled bool) []byte {
	b := byte(0)
	if enabled {
		b = 1
	}
	msg := make([]byte, 0, 1+len(enableEncryptionSeed))
	msg = append(msg, b)
	msg = append(msg, enableEncryptionSeed...)
	return hmacSHA512(encryptKey, msg)
}

// VerifyEnterEncryptedMode checks a SMSG_ENTER_ENCRYPTED_MODE signature with
// the shipped verifier. verify is the ed25519ctx verify function, injected so
// the package needs no key store.
//
// The client verifies an ed25519ctx signature with EnableEncryptionContext over
// EnableEncryptionMessage and tears the connection down on mismatch
// (0x1014b738c).
func VerifyEnterEncryptedMode(verify func(pub, msg, sig, ctx []byte) bool, pub []byte, signed []byte, sig []byte, enabled bool) bool {
	if len(pub) != 32 || len(sig) != 64 {
		return false
	}
	return verify(pub, EnableEncryptionMessage(signed, enabled), sig, EnableEncryptionContext)
}

// hashFn is exported for tests that want to assert the primitives directly.
func hashFn() hash.Hash { return sha512.New() }
