package world

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Ltz-lab/wow-live/protocol/framing"
)

// Direction selects which of the two per-connection GCM contexts a Cipher is:
// the client keeps separate send and receive contexts with separate counters
// and separate IV magics (client flags byte bit 0, research/encryption.md §1).
type Direction int

const (
	// Recv decrypts server-to-client frames, IV magic "SRVR".
	Recv Direction = iota
	// Send encrypts client-to-server frames, IV magic "CLNT".
	Send
)

// IV magics, as the client stores them (0x52565253 "SRVR", 0x544E4C43 "CLNT").
var (
	magicRecv = [4]byte{'S', 'R', 'V', 'R'}
	magicSend = [4]byte{'C', 'L', 'N', 'T'}
)

// AEADNonceLen is the GCM nonce length: the 8-byte little-endian counter then
// the 4-byte magic (client 0x101093a5c receive, 0x101093634 send).
const AEADNonceLen = 12

var (
	// ErrBadKeyLen means the key is not 32 bytes (AES-256).
	ErrBadKeyLen = errors.New("world: encryption key must be 32 bytes")
	// ErrBadTag means the frame's tag is the wrong length.
	ErrBadTag = errors.New("world: frame tag must be 12 bytes")
	// ErrAuthFailed means GCM authentication failed.
	ErrAuthFailed = errors.New("world: frame authentication failed")
)

// Cipher is one direction of the post-ENTER_ENCRYPTED_MODE connection cipher:
// AES-256-GCM over the opcode and payload, no AAD, a per-frame counter-derived
// nonce, and the first 12 of the 16 tag bytes on the wire.
//
// A Cipher is not safe for concurrent use; the connection serializes reads and
// writes.
type Cipher struct {
	aead    cipher.AEAD
	magic   [4]byte
	counter uint64
}

// NewCipher arms a direction with the derived key, its nonce counter at 0.
func NewCipher(key []byte, dir Direction) (*Cipher, error) {
	return NewCipherAt(key, dir, 0)
}

// NewCipherAt arms a direction with the nonce counter already at start.
//
// The counter is not reset by the switch to encrypted mode: it counts every
// frame of its direction from the first one, cleartext frames included
// (research/encryption.md \u00a71, "both start at 0 and increment after each
// frame"). A session that already exchanged two cleartext frames therefore
// starts its send cipher at 2, not 0 \u2014 which is what the server's own
// DecryptRecv expects, because it increments its counter for the cleartext
// frames too (the fork: WorldPacketCrypt::DecryptRecv, WorldSocket.cpp
// HandleEnterEncryptedModeAck).
func NewCipherAt(key []byte, dir Direction, start uint64) (*Cipher, error) {
	if len(key) != EncryptionKeyLen {
		return nil, ErrBadKeyLen
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// The wire carries only the first 12 of the 16 tag bytes and the client's
	// own verifier compares exactly those 12 (0x102e4eb7c), which is what a
	// 12-byte GCM tag size produces.
	aead, err := cipher.NewGCMWithTagSize(block, framing.TagLen)
	if err != nil {
		return nil, err
	}
	c := &Cipher{aead: aead, counter: start}
	if dir == Send {
		c.magic = magicSend
	} else {
		c.magic = magicRecv
	}
	return c, nil
}

// Counter reports the next nonce counter.
func (c *Cipher) Counter() uint64 { return c.counter }

func (c *Cipher) nonce() []byte {
	var n [AEADNonceLen]byte
	binary.LittleEndian.PutUint64(n[:8], c.counter)
	copy(n[8:], c.magic[:])
	c.counter++
	return n[:]
}

// Seal encrypts plaintext (opcode included) and returns the ciphertext with
// the 12-byte wire tag after it, i.e. exactly the frame body the wire carries
// once the opcode's header is in front of it.
func (c *Cipher) Seal(dst, plaintext []byte) ([]byte, error) {
	out := c.aead.Seal(dst, c.nonce(), plaintext, nil)
	if len(out) != len(plaintext)+framing.TagLen {
		return nil, fmt.Errorf("world: seal returned %d bytes for %d plus %d", len(out), len(plaintext), framing.TagLen)
	}
	return out, nil
}

// Open authenticates and decrypts a frame body with its wire tag, appending
// the plaintext to dst.
func (c *Cipher) Open(dst, body, tag []byte) ([]byte, error) {
	if len(tag) != framing.TagLen {
		return dst, ErrBadTag
	}
	sealed := make([]byte, 0, len(body)+framing.TagLen)
	sealed = append(sealed, body...)
	sealed = append(sealed, tag...)
	out, err := c.aead.Open(dst, c.nonce(), sealed, nil)
	if err != nil {
		return dst, fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	return out, nil
}
