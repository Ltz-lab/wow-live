package world

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The vectors below are produced by an independent implementation (Python
// hashlib/hmac, /tmp/ref.py) from fixed inputs, so a mistake in the Go port
// shows up as a diff instead of agreeing with itself.
var (
	vectorKeyData = func() []byte {
		b := make([]byte, KeyDataLen)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}()
	vectorBuild = func() []byte {
		b := make([]byte, BuildKeyLen)
		for i := range b {
			b[i] = byte(i + 0x10)
		}
		return b
	}()
	vectorLocal  = bytes.Repeat([]byte{0xAA}, ChallengeLen)
	vectorServer = bytes.Repeat([]byte{0xBB}, ChallengeLen)
	vectorDos    = bytes.Repeat([]byte{0x11}, 32)
)

func TestAuthDigestVector(t *testing.T) {
	got, err := AuthDigest(vectorKeyData, vectorBuild, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("59e8bb8e6d10ad8c33665c8c5df1fb99fd7fff857340c444")
	if !bytes.Equal(got[:], want) {
		t.Fatalf("digest = %x, want %x", got, want)
	}
}

func TestAuthDigestWithoutBuildKey(t *testing.T) {
	with, err := AuthDigest(vectorKeyData, vectorBuild, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	without, err := AuthDigest(vectorKeyData, nil, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	if with == without {
		t.Fatal("build key did not change the digest")
	}
}

func TestSessionKeyVector(t *testing.T) {
	got, err := SessionKey(vectorKeyData, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	// The generator starts its state at 64 zero bytes (SessionKeyGenerator.h,
	// `o0 = {}`), not at SHA512(seed): regenerated from that reference after a
	// live session proved the old vectors wrong (E34).
	want, _ := hex.DecodeString("604277d03b95fda426bcca8881d5f869a46b2c4bf118fe76816b7d5767480e714171a996fa6c227d")
	if !bytes.Equal(got[:], want) {
		t.Fatalf("session key = %x, want %x", got, want)
	}
}

func TestEncryptionKeyVector(t *testing.T) {
	session, err := SessionKey(vectorKeyData, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	got, err := EncryptionKey(session[:], vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("d63f405554a41413f331925920812695371bfdc145e771c30bdf9513b7b74296")
	if !bytes.Equal(got[:], want) {
		t.Fatalf("encryption key = %x, want %x", got, want)
	}
}

func TestDoSProofVector(t *testing.T) {
	digest, err := AuthDigest(vectorKeyData, vectorBuild, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := DoSProof(vectorLocal, vectorServer, digest[:], vectorDos, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nonce != 180 {
		t.Fatalf("nonce = %d, want 180", nonce)
	}
}

func TestDoSZeroBitsCountsFromLowBit(t *testing.T) {
	cases := []struct {
		h    []byte
		want int
	}{
		{[]byte{0x01}, 0},
		{[]byte{0x02}, 1},
		{[]byte{0x80}, 7},
		{[]byte{0x00, 0x01}, 8},
		{[]byte{0x00, 0x00, 0x00, 0x00, 0x10}, 36},
		{[]byte{0, 0, 0}, 24},
	}
	for _, c := range cases {
		if got := doSZeroBits(c.h); got != c.want {
			t.Errorf("doSZeroBits(%x) = %d, want %d", c.h, got, c.want)
		}
	}
}

func TestDoSProofRejectsTooManyZeroBits(t *testing.T) {
	if _, err := DoSProof(vectorLocal, vectorServer, vectorDos, vectorDos, 49, 1); err != ErrBadZeroBits {
		t.Fatalf("err = %v, want ErrBadZeroBits", err)
	}
}

func TestAuthSessionEncoding(t *testing.T) {
	a := AuthSession{
		DosResponse:   0x1122334455667788,
		RegionID:      1,
		BattlegroupID: 2,
		RealmID:       3,
		UseIPv6:       true,
		Ticket:        []byte("ticket"),
	}
	copy(a.LocalChallenge[:], bytes.Repeat([]byte{0xCC}, ChallengeLen))
	copy(a.Digest[:], bytes.Repeat([]byte{0xDD}, DigestLen))
	b := a.Encode(0x460001)

	want := []byte{
		0x01, 0x00, 0x46, 0x00, // opcode
		0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, // DoS response, LE
		0x01, 0x00, 0x00, 0x00, // region
		0x02, 0x00, 0x00, 0x00, // battlegroup
		0x03, 0x00, 0x00, 0x00, // realm
	}
	want = append(want, bytes.Repeat([]byte{0xCC}, ChallengeLen)...)
	want = append(want, bytes.Repeat([]byte{0xDD}, DigestLen)...)
	want = append(want, 0x80)          // one bit UseIPv6, flushed as its own byte
	want = append(want, 0x06, 0, 0, 0) // ticket length
	want = append(want, "ticket"...)   // ticket
	if !bytes.Equal(b, want) {
		t.Fatalf("encoding\n got %x\nwant %x", b, want)
	}
}

func TestContinuedSessionDigestDiffersFromAuthDigest(t *testing.T) {
	session, err := SessionKey(vectorKeyData, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	cont, err := ContinuedSessionDigest(session[:], 0x0102030405060708, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := AuthDigest(vectorKeyData, vectorBuild, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(cont[:], auth[:]) {
		t.Fatal("continued session digest equals the plain digest")
	}
}

func TestEnableEncryptionMessageSignsTheFlag(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, EncryptionKeyLen)
	on := EnableEncryptionMessage(key, true)
	off := EnableEncryptionMessage(key, false)
	if len(on) != 64 || bytes.Equal(on, off) {
		t.Fatalf("enabled flag did not change the signed message (len %d)", len(on))
	}
	if on[0] == 0 && bytes.Equal(on[1:], off[1:]) {
		t.Fatal("only the first byte should differ")
	}
}

func TestVerifyEnterEncryptedModeChecksLengths(t *testing.T) {
	called := false
	verify := func(pub, msg, sig, ctx []byte) bool { called = true; return true }
	if VerifyEnterEncryptedMode(verify, make([]byte, 31), nil, make([]byte, 64), true) {
		t.Fatal("31-byte public key accepted")
	}
	if called {
		t.Fatal("verifier called with a short key")
	}
	if !VerifyEnterEncryptedMode(verify, make([]byte, 32), nil, make([]byte, 64), true) {
		t.Fatal("valid lengths rejected")
	}
}

func TestDoSCheckMatchesProof(t *testing.T) {
	digest, err := AuthDigest(vectorKeyData, vectorBuild, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := DoSProof(vectorLocal, vectorServer, digest[:], vectorDos, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := DoSCheck(vectorLocal, vectorServer, digest[:], vectorDos, nonce, 8)
	if err != nil || !ok {
		t.Fatalf("check rejected the proof: %v %v", ok, err)
	}
	if ok, _ := DoSCheck(vectorLocal, vectorServer, digest[:], vectorDos, nonce+1, 8); ok {
		t.Log("nonce+1 also passes (allowed, servers try a few)")
	}
	if ok, _ := DoSCheck(vectorLocal, vectorServer, digest[:], vectorDos, 0, 30); ok {
		t.Fatal("nonce 0 accepted for 30 zero bits")
	}
}

func TestForeverBuildAuthKeyChangesTheDigest(t *testing.T) {
	withReal, err := AuthDigest(vectorKeyData, ForeverBuildAuthKey[:], vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	none, err := AuthDigest(vectorKeyData, nil, vectorLocal, vectorServer)
	if err != nil {
		t.Fatal(err)
	}
	if withReal == none {
		t.Fatal("the build key did not affect the digest")
	}
	if len(ForeverBuildAuthKey) != BuildKeyLen {
		t.Fatalf("build key is %d bytes, want %d", len(ForeverBuildAuthKey), BuildKeyLen)
	}
}
