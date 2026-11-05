package world

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/framing"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/transport"
)

// The stock 70755 client's world entry on the local fork
// (~/Dev/wow/server/wow-forever-tc/captures/2026-11-04-goldshire-70755.pkt).
const (
	// #190 SMSG_CONNECT_TO, 45 bytes.
	captureConnectTo = "010000001100000001010000009baaecd54600014600000000017f000001951f00000008000000000000000000"
	// #192 CMSG_AUTH_CONTINUED_SESSION, 80 bytes.
	captureContinued = "0000000000000000d19440f7647ce8c7b372c030ba23e0bc33cbae366dcfc2076baa546dc9e43089eaab7c628a42cb35eb75ee8a429cb4d321a407dae3dfa73f010000009baaecd54600014600000000"
)

func TestDecodeConnectToCapture(t *testing.T) {
	body, _ := hex.DecodeString(captureConnectTo)
	c, err := DecodeConnectTo(body)
	if err != nil {
		t.Fatal(err)
	}
	if c.Serial != ConnectToSerialWorld1 || c.Con != 1 {
		t.Errorf("serial %d con %d, want 17 1", c.Serial, c.Con)
	}
	if c.Key != 0xd5ecaa9b00000001 || c.AccountID() != 1 || !c.Instance() {
		t.Errorf("key %#x account %d instance %v", c.Key, c.AccountID(), c.Instance())
	}
	// 70-1-70: region 70, battlegroup 1, realm 70.
	if c.NativeRealmAddress != 0x46010046 || c.Key3 != 0 {
		t.Errorf("native realm %#x key3 %#x", c.NativeRealmAddress, c.Key3)
	}
	if len(c.Payloads) != 1 {
		t.Fatalf("%d payloads", len(c.Payloads))
	}
	p := c.Payloads[0]
	if p.Type != ConnectAddrIPv4 || p.Port != 8085 || (p.Token != BleepToken{}) {
		t.Errorf("payload %+v", p)
	}
	target, err := c.Target(false)
	if err != nil || target != "127.0.0.1:8085" {
		t.Errorf("target %q %v", target, err)
	}
}

func TestDecodeConnectToTruncated(t *testing.T) {
	body, _ := hex.DecodeString(captureConnectTo)
	for n := 0; n < len(body); n++ {
		if _, err := DecodeConnectTo(body[:n]); err == nil {
			t.Fatalf("decoded %d of %d bytes without error", n, len(body))
		}
	}
}

// The continued session echoes CONNECT_TO's key, native realm address and
// key3 in the real client's 80-byte layout.
func TestAuthContinuedSessionCapture(t *testing.T) {
	want, _ := hex.DecodeString(captureContinued)
	a, err := DecodeAuthContinuedSession(want)
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := hex.DecodeString(captureConnectTo)
	c, _ := DecodeConnectTo(ct)
	if a.Key != c.Key || a.NativeRealmAddress != c.NativeRealmAddress || a.Key3 != c.Key3 {
		t.Errorf("echo key %#x realm %#x key3 %#x, CONNECT_TO had %#x %#x %#x",
			a.Key, a.NativeRealmAddress, a.Key3, c.Key, c.NativeRealmAddress, c.Key3)
	}
	got := a.Encode(0x460003)
	if binary.LittleEndian.Uint32(got) != 0x460003 || !bytes.Equal(got[4:], want) {
		t.Fatalf("encode\n got %x\nwant %x", got[4:], want)
	}
}

// forkContinuedDigest is the fork's check, written independently of
// ContinuedSessionDigest (HandleAuthContinuedSessionCallback).
func forkContinuedDigest(sessionKey []byte, key uint64, local, server []byte) []byte {
	m := hmac.New(sha512.New, sessionKey)
	_ = binary.Write(m, binary.LittleEndian, key)
	m.Write(local)
	m.Write(server)
	m.Write(continuedSessionSeed)
	return m.Sum(nil)[:DigestLen]
}

const testAuthContinued = 0x460003

func continuedTable(t *testing.T) *opcodes.Table {
	t.Helper()
	ent := func(name string, dir opcodes.Direction, id uint32) string {
		return `{"name":"` + name + `","direction":"` + string(dir) + `","id":` +
			itoa(id) + `,"confidence":"verified"}`
	}
	raw := `{"protocol":"wowf","product":"wowf","build":70755,"status":"test","entries":[` +
		ent("SMSG_AUTH_CHALLENGE", opcodes.ServerToClient, testAuthChallenge) + `,` +
		ent("SMSG_ENTER_ENCRYPTED_MODE", opcodes.ServerToClient, testEncMode) + `,` +
		ent("CMSG_AUTH_CONTINUED_SESSION", opcodes.ClientToServer, testAuthContinued) + `,` +
		ent("CMSG_ENTER_ENCRYPTED_MODE_ACK", opcodes.ClientToServer, testAck) + `,` +
		ent("CMSG_LOG_DISCONNECT", opcodes.ClientToServer, testDisc) + `,` +
		ent("SMSG_PING", opcodes.ServerToClient, testPing) + `]}`
	tbl, err := opcodes.Load([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func TestConnectContinuedFullExchange(t *testing.T) {
	d := newPipeDialer()
	var sk [SessionKeyLen]byte
	if _, err := rand.Read(sk[:]); err != nil {
		t.Fatal(err)
	}
	ct, _ := hex.DecodeString(captureConnectTo)
	cto, _ := DecodeConnectTo(ct)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	srvErr := make(chan string, 1)
	go func() {
		fail := func(s string) { srvErr <- s; d.serverNet.Close() }
		conn := d.serverNet
		conn.Write(append([]byte(framing.ServerConnect), '\n'))
		line := make([]byte, len(framing.ClientConnect)+1)
		if _, err := io.ReadFull(conn, line); err != nil {
			fail(err.Error())
			return
		}
		dos := bytes.Repeat([]byte{0x33}, 32)
		server := bytes.Repeat([]byte{0x44}, ChallengeLen)
		ch := append(append(append([]byte(nil), dos...), server...), 4)
		conn.Write(framing.Frame(framing.Tagged{}, testAuthChallenge, ch))

		ss := &serverSide{t: t, conn: conn}
		msg, err := ss.readMessage()
		if err != nil {
			fail(err.Error())
			return
		}
		if msg.Opcode != testAuthContinued {
			fail("not AUTH_CONTINUED_SESSION")
			return
		}
		a, err := DecodeAuthContinuedSession(msg.Body)
		if err != nil {
			fail(err.Error())
			return
		}
		if a.Key != cto.Key || a.NativeRealmAddress != cto.NativeRealmAddress || a.Key3 != cto.Key3 {
			fail("CONNECT_TO fields not echoed")
			return
		}
		if !bytes.Equal(a.Digest[:], forkContinuedDigest(sk[:], cto.Key, a.LocalChallenge[:], server)) {
			fail("digest mismatch")
			return
		}
		if ok, _ := DoSCheck(a.LocalChallenge[:], server, sk[:], dos, a.DosResponse, 4); !ok {
			fail("hashcash does not use the session key")
			return
		}
		enc, _ := EncryptionKey(sk[:], a.LocalChallenge[:], server)
		sig, _ := priv.Sign(rand.Reader, EnableEncryptionMessage(enc[:], true), &ed25519.Options{Context: string(EnableEncryptionContext)})
		body := binary.LittleEndian.AppendUint32(nil, 0)
		body = append(append(body, sig...), 0x80)
		conn.Write(framing.Frame(framing.Tagged{}, testEncMode, body))
		if msg, err = ss.readMessage(); err != nil || msg.Opcode != testAck {
			fail("no ACK")
			return
		}
		send, _ := NewCipherAt(enc[:], Recv, 2)
		plain := binary.LittleEndian.AppendUint32(nil, testPing)
		plain = append(plain, "resume"...)
		sealed, _ := send.Seal(nil, plain)
		c, tag := sealed[:len(plain)], sealed[len(plain):]
		conn.Write(append(framing.TaggedCrypt{}.EncodeSealed(c, [framing.TagLen]byte(tag)), c[4:]...))
		srvErr <- ""
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := ConnectContinued(ctx, &ContinuedConfig{
		Dialer: d, Target: transport.Target{Address: "pipe"}, Table: continuedTable(t),
		SessionKey: sk, ConnectTo: cto, Timeout: 5 * time.Second,
		Verifier: Ed25519Verifier{Pub: pub},
	})
	if err != nil {
		t.Fatalf("ConnectContinued: %v (server: %s)", err, <-srvErr)
	}
	defer s.Close()
	m, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if m.Opcode != testPing || string(m.Body) != "resume" {
		t.Fatalf("read %#x %q", m.Opcode, m.Body)
	}
	if e := <-srvErr; e != "" {
		t.Fatal(e)
	}
	if s.SessionKey() != sk {
		t.Error("session key not kept")
	}
}

// The first connection keeps the session key the continued one needs.
func TestConnectKeepsSessionKey(t *testing.T) {
	d := newPipeDialer()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	cfg := testConfig(t, d, Ed25519Verifier{Pub: pub})
	srv := &serverSide{t: t, conn: d.serverNet, key: cfg.KeyData, build: cfg.BuildKey, priv: priv, pub: pub}
	done := make(chan struct{})
	go func() { srv.run(1); close(done) }()
	s, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// the fake server ends with one encrypted message; take it so it returns
	if _, err := s.Read(); err != nil {
		t.Fatal(err)
	}
	<-done
	want, _ := SessionKey(cfg.KeyData, srv.gotLocal[:], bytes.Repeat([]byte{0x22}, ChallengeLen))
	if s.SessionKey() != want {
		t.Fatal("Connect did not keep the session key")
	}
}
