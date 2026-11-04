package world

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

// Vector from an independent AES-256-GCM implementation (Python `cryptography`,
// /tmp/gcmref.py) with key = 0x00..0x1f, nonce = counter 0 ‖ "CLNT" and the
// plaintext 0x01004600 ‖ "hello world".
func TestSealVectorMatchesTagAndBody(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := NewCipher(key, Send)
	if err != nil {
		t.Fatal(err)
	}
	plain := append([]byte{0x01, 0x00, 0x46, 0x00}, "hello world"...)
	sealed, err := c.Seal(make([]byte, 0, len(plain)+16), plain)
	if err != nil {
		t.Fatal(err)
	}
	tag := [12]byte(sealed[len(plain):])
	body := sealed[:len(plain)]
	wantBody, _ := hex.DecodeString("defef3098ab8e9f494d0021657748e")
	wantTag, _ := hex.DecodeString("46102c06b63a74af29389168")
	if !bytes.Equal(tag[:], wantTag) {
		t.Fatalf("tag = %x, want %x", tag, wantTag)
	}
	// A second cipher at counter 0 must produce byte-identical output.
	body, err = c0Body(t, key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, wantBody) {
		t.Fatalf("body = %x, want %x", body, wantBody)
	}
	if c.Counter() != 1 {
		t.Fatalf("counter = %d, want 1", c.Counter())
	}
}

func c0Body(t *testing.T, key, plain []byte) ([]byte, error) {
	t.Helper()
	c, err := NewCipher(key, Send)
	if err != nil {
		return nil, err
	}
	sealed, err := c.Seal(make([]byte, 0, len(plain)+16), plain)
	if err != nil {
		return nil, err
	}
	buf, tag := sealed[:len(plain)], [12]byte(sealed[len(plain):])
	rc, err := NewCipher(key, Recv)
	if err != nil {
		return nil, err
	}
	// The receive side uses the SRVR magic, so recompute the nonce by hand to
	// compare bytes with the reference that also used CLNT.
	if _, err := rcvOpenWithNonce(t, rc, buf, tag, 0, magicSend); err != nil {
		return nil, err
	}
	return buf, nil
}

func rcvOpenWithNonce(t *testing.T, c *Cipher, body []byte, tag [12]byte, counter uint64, magic [4]byte) ([]byte, error) {
	t.Helper()
	saved := c.magic
	savedN := c.counter
	c.magic = magic
	c.counter = counter
	out, err := c.Open(nil, body, tag[:])
	c.magic, c.counter = saved, savedN
	return out, err
}

func TestSerpentBadKeyLength(t *testing.T) {
	if _, err := NewCipher(make([]byte, 16), Send); !errors.Is(err, ErrBadKeyLen) {
		t.Fatalf("err = %v, want ErrBadKeyLen", err)
	}
}

func TestRecvAndSendUseDifferentMagics(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	s, err := NewCipher(key, Send)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewCipher(key, Recv)
	if err != nil {
		t.Fatal(err)
	}
	if s.magic != magicSend || r.magic != magicRecv {
		t.Fatalf("magics = %q, %q", s.magic, r.magic)
	}
}

func TestRoundTripAndCounterAdvance(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	s, _ := NewCipher(key, Send)
	r, _ := NewCipher(key, Recv)

	for i := 0; i < 3; i++ {
		plain := append([]byte{byte(i), 0, 0, 0}, bytes.Repeat([]byte{0x5A}, 40)...)
		sealed, err := s.Seal(make([]byte, 0, len(plain)+16), plain)
		if err != nil {
			t.Fatal(err)
		}
		buf, tag := sealed[:len(plain)], [12]byte(sealed[len(plain):])
		// Mirror the send magic on the receive cipher: both sides of one
		// direction share the magic, the test's receive cipher is the same
		// direction's peer.
		r.magic = magicSend
		out, err := r.Open(nil, buf, tag[:])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, plain) {
			t.Fatalf("round trip %d mismatch", i)
		}
		if s.Counter() != uint64(i+1) || r.Counter() != uint64(i+1) {
			t.Fatalf("counters = %d, %d", s.Counter(), r.Counter())
		}
	}
}

func TestOpenRejectsTamperedBodyAndTag(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	s, _ := NewCipher(key, Send)
	r, _ := NewCipher(key, Recv)
	r.magic = magicSend

	plain := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	sealed, _ := s.Seal(make([]byte, 0, len(plain)+16), plain)
	buf, tag := sealed[:len(plain)], [12]byte(sealed[len(plain):])

	badBody := append([]byte(nil), buf...)
	badBody[2] ^= 0xFF
	if _, err := r.Open(nil, badBody, tag[:]); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("tampered body err = %v", err)
	}
	r.counter = 0
	badTag := tag
	badTag[0] ^= 0xFF
	if _, err := r.Open(nil, buf, badTag[:]); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("tampered tag err = %v", err)
	}
	r.counter = 0
	if _, err := r.Open(nil, buf, tag[:3]); !errors.Is(err, ErrBadTag) {
		t.Fatalf("short tag err = %v", err)
	}
}
