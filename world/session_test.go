package world

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/framing"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/transport"
)

const (
	testAuthChallenge = 0x4E0000
	testEncMode       = 0x4E0004
	testAuthSession   = 0x460001
	testAck           = 0x460005
	testDisc          = 0x460007
	testPing          = 0x470001
)

func testTable(t *testing.T) *opcodes.Table {
	t.Helper()
	ent := func(name string, dir opcodes.Direction, id uint32) string {
		return `{"name":"` + name + `","direction":"` + string(dir) + `","id":` +
			itoa(id) + `,"confidence":"verified"}`
	}
	raw := `{"protocol":"wowf","product":"wowf","build":70707,"status":"test","entries":[` +
		ent("SMSG_AUTH_CHALLENGE", opcodes.ServerToClient, testAuthChallenge) + `,` +
		ent("SMSG_ENTER_ENCRYPTED_MODE", opcodes.ServerToClient, testEncMode) + `,` +
		ent("CMSG_AUTH_SESSION", opcodes.ClientToServer, testAuthSession) + `,` +
		ent("CMSG_ENTER_ENCRYPTED_MODE_ACK", opcodes.ClientToServer, testAck) + `,` +
		ent("CMSG_LOG_DISCONNECT", opcodes.ClientToServer, testDisc) + `,` +
		ent("SMSG_PING", opcodes.ServerToClient, testPing) + `]}`
	tbl, err := opcodes.Load([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// pipeDialer hands the client one end of an in-memory connection.
type pipeDialer struct {
	client    transport.Conn
	serverNet net.Conn
}

func newPipeDialer() *pipeDialer {
	c, s := net.Pipe()
	return &pipeDialer{client: &pipeConn{c}, serverNet: s}
}

func (d *pipeDialer) Dial(ctx context.Context, _ transport.Target) (transport.Conn, error) {
	return d.client, nil
}

type pipeConn struct{ net.Conn }

func (c *pipeConn) Describe() string              { return "pipe" }
func (c *pipeConn) SetDeadline(t time.Time) error { return c.Conn.SetDeadline(t) }

func testConfig(t *testing.T, d transport.Dialer, v Verifier) *Config {
	t.Helper()
	key := make([]byte, KeyDataLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	var build [BuildKeyLen]byte
	copy(build[:], []byte("0123456789abcdef"))
	return &Config{
		Dialer:   d,
		Target:   transport.Target{Address: "pipe"},
		Table:    testTable(t),
		KeyData:  key,
		Ticket:   []byte("ticket-bytes"),
		BuildKey: &build,
		Region:   1, Battlegroup: 2, Realm: 3,
		Timeout:  5 * time.Second,
		Verifier: v,
	}
}

// serverSide is the fork's half of the exchange, enough of it to check the
// client end.
var dosBody []byte

type serverSide struct {
	t     *testing.T
	conn  net.Conn
	key   []byte
	build *[BuildKeyLen]byte
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	// results
	gotLocal  [ChallengeLen]byte
	gotNonce  uint64
	gotTicket string
	encKey    [EncryptionKeyLen]byte
	acked     bool
}

func (s *serverSide) readMessage() (framing.Message, error) {
	stream := framing.NewStream(framing.Tagged{})
	var buf []byte
	one := make([]byte, 4096)
	for {
		msgs, err := stream.Push(buf)
		buf = nil
		if err != nil {
			return framing.Message{}, err
		}
		if len(msgs) > 0 {
			return msgs[0], nil
		}
		n, err := s.conn.Read(one)
		if n > 0 {
			buf = append(buf, one[:n]...)
		}
		if err != nil {
			return framing.Message{}, err
		}
	}
}

func (s *serverSide) run(challengeZeroBits byte) {
	// server handshake line first
	if _, err := s.conn.Write(append([]byte(framing.ServerConnect), '\n')); err != nil {
		s.t.Error(err)
		return
	}
	readExact := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(s.conn, b); err != nil {
			s.t.Error(err)
		}
		return b
	}
	line := readExact(len(framing.ClientConnect) + 1)
	if string(line) != framing.ClientConnect+"\n" {
		s.t.Errorf("client line = %q", line)
		return
	}

	body := make([]byte, 0, 65)
	dos := bytes.Repeat([]byte{0x11}, 32)
	server := bytes.Repeat([]byte{0x22}, ChallengeLen)
	body = append(body, dos...)
	body = append(body, server...)
	body = append(body, challengeZeroBits)
	if _, err := s.conn.Write(framing.Frame(framing.Tagged{}, testAuthChallenge, body)); err != nil {
		s.t.Error(err)
		return
	}

	msg, err := s.readMessage()
	if err != nil {
		s.t.Error(err)
		return
	}
	if msg.Opcode != testAuthSession {
		s.t.Errorf("got opcode %#08x, want AUTH_SESSION", msg.Opcode)
		return
	}
	b := msg.Body
	s.gotNonce = binary.LittleEndian.Uint64(b[0:8])
	copy(s.gotLocal[:], b[20:52])
	digest := b[52:76]
	ticketLen := binary.LittleEndian.Uint32(b[77:81])
	s.gotTicket = string(b[81 : 81+ticketLen])

	want, err := AuthDigest(s.key, s.build[:], s.gotLocal[:], server)
	if err != nil {
		s.t.Error(err)
		return
	}
	if !bytes.Equal(digest, want[:]) {
		s.t.Errorf("digest = %x, want %x", digest, want)
	}
	session, err := SessionKey(s.key, s.gotLocal[:], server)
	if err != nil {
		s.t.Error(err)
		return
	}
	s.encKey, err = EncryptionKey(session[:], s.gotLocal[:], server)
	if err != nil {
		s.t.Error(err)
		return
	}

	signed := EnableEncryptionMessage(s.encKey[:], true)
	sig, err := s.priv.Sign(rand.Reader, signed, &ed25519.Options{Context: string(EnableEncryptionContext)})
	if err != nil {
		s.t.Error(err)
		return
	}
	enc := make([]byte, 0, 4+64+1)
	enc = binary.LittleEndian.AppendUint32(enc, 1) // region group
	enc = append(enc, sig...)
	enc = append(enc, 0x80) // Enabled, as one high bit
	if _, err := s.conn.Write(framing.Frame(framing.Tagged{}, testEncMode, enc)); err != nil {
		s.t.Error(err)
		return
	}

	msg, err = s.readMessage()
	if err != nil {
		s.t.Error(err)
		return
	}
	if msg.Opcode == testDisc {
		return // a rejection path: the client logs the disconnect
	}
	if msg.Opcode != testAck || len(msg.Body) != 0 {
		s.t.Errorf("got %#08x len %d, want the empty ACK", msg.Opcode, len(msg.Body))
		return
	}
	s.acked = true

	// One encrypted message. The server's send direction is the client's
	// receive direction, so it uses the "SRVR" magic.
	// Its send counter continues from the two cleartext frames it already sent
	// (AUTH_CHALLENGE, ENTER_ENCRYPTED_MODE), exactly as the client's receive
	// counter does.
	send, err := NewCipherAt(s.encKey[:], Recv, 2)
	if err != nil {
		s.t.Error(err)
		return
	}
	plain := binary.LittleEndian.AppendUint32(nil, testPing)
	plain = append(plain, []byte("pong")...)
	sealed, err := send.Seal(make([]byte, 0, len(plain)+16), plain)
	if err != nil {
		s.t.Error(err)
		return
	}
	ct, tag := sealed[:len(plain)], sealed[len(plain):]
	hdr := framing.TaggedCrypt{}.EncodeSealed(ct, [framing.TagLen]byte(tag))
	if _, err := s.conn.Write(append(hdr, ct[4:]...)); err != nil {
		s.t.Error(err)
	}
}

func TestConnectFullExchange(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := newPipeDialer()
	cfg := testConfig(t, d, Ed25519Verifier{Pub: pub, Enabled: true})
	srv := &serverSide{t: t, key: cfg.KeyData, build: cfg.BuildKey, priv: priv, pub: pub}
	dosBody = bytes.Repeat([]byte{0x11}, 32)

	done := make(chan struct{})
	srv.conn = d.serverNet
	go func() {
		defer close(done)
		srv.run(1)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()
	if !sess.Encrypted() {
		t.Fatal("cipher not armed")
	}

	msg, err := sess.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg.Opcode != testPing || string(msg.Body) != "pong" {
		t.Fatalf("got %#08x %q", msg.Opcode, msg.Body)
	}
	<-done

	if !srv.acked {
		t.Fatal("server never saw the ACK")
	}
	if srv.gotTicket != "ticket-bytes" {
		t.Fatalf("ticket = %q", srv.gotTicket)
	}
	// The nonce is the lowest value with the requested zero bits, and the
	// server-side recheck must accept it.
	digest, err := AuthDigest(srv.key, srv.build[:], srv.gotLocal[:], bytes.Repeat([]byte{0x22}, ChallengeLen))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := DoSCheck(srv.gotLocal[:], bytes.Repeat([]byte{0x22}, ChallengeLen), digest[:], dosBody, srv.gotNonce, 1)
	if err != nil || !ok {
		t.Fatalf("server rejected the proof: %v %v", ok, err)
	}
}

func TestConnectRejectsWrongHandshake(t *testing.T) {
	d := newPipeDialer()
	cfg := testConfig(t, d, AcceptAllVerifier{})
	go func() {
		d.serverNet.Write([]byte("NOT THE LINE\n"))
		io.ReadFull(d.serverNet, make([]byte, len(framing.ClientConnect)+1))
	}()
	ctx := context.Background()
	_, err := Connect(ctx, cfg)
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
}

func TestConnectRejectsBadSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := newPipeDialer()
	cfg := testConfig(t, d, Ed25519Verifier{Pub: otherPub})
	srv := &serverSide{t: t, key: cfg.KeyData, build: cfg.BuildKey, priv: priv, pub: pub}
	srv.conn = d.serverNet
	go func() { srv.run(1) }()

	_, err = Connect(context.Background(), cfg)
	if !errors.Is(err, ErrServerIdentity) {
		t.Fatalf("err = %v, want ErrServerIdentity", err)
	}
}

func TestConfigBuildKeyDefaultsToForeverKey(t *testing.T) {
	c := &Config{}
	if got := c.buildKey(); string(got) != string(ForeverBuildAuthKey[:]) {
		t.Fatalf("default build key = %x, want %x", got, ForeverBuildAuthKey)
	}
	c.SkipBuildKey = true
	if c.buildKey() != nil {
		t.Fatal("SkipBuildKey should yield a nil build key")
	}
	for _, b := range []int{70707, 70755} {
		tbl, err := opcodes.ForBuild(b)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := BuildAuthKey(b)
		if got := (&Config{Table: tbl}).buildKey(); string(got) != string(want[:]) {
			t.Fatalf("build %d: default key %x, want %x", b, got, want)
		}
	}
	if BuildAuthKey70755 == ForeverBuildAuthKey {
		t.Fatal("70755 and 70707 keys must differ")
	}
	var explicit [BuildKeyLen]byte
	explicit[0] = 0x99
	c2 := &Config{BuildKey: &explicit}
	if got := c2.buildKey(); got[0] != 0x99 {
		t.Fatalf("explicit build key not used: %x", got)
	}
}

// world.Connect resolves its five opcodes by name; the shipped tables of every
// known build must have them.
func TestShippedTablesHaveWorldOpcodes(t *testing.T) {
	for _, b := range []int{70707, 70755} {
		tbl, err := opcodes.ForBuild(b)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]uint32{
			opAuthChallenge: 0x4E0000, opAuthSession: 0x460001, opEnterEncrypted: 0x4E0004,
			opEncryptedModeAck: 0x460005, opLogDisconnect: 0x460007,
		} {
			if _, id, err := tbl.RequireID(name); err != nil || id != want {
				t.Errorf("build %d %s = %#x, %v; want %#x", b, name, id, err, want)
			}
		}
	}
}

// deadlineConn records the last deadline set on it.
type deadlineConn struct {
	pipeConn
	last time.Time
}

func (c *deadlineConn) SetDeadline(t time.Time) error { c.last = t; return c.pipeConn.SetDeadline(t) }

// TestConnectClearsDeadline: the handshake deadline is lifted once Connect
// returns. Left in place it killed live sessions with an i/o timeout after
// -timeout, however healthy they were.
func TestConnectClearsDeadline(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := newPipeDialer()
	dc := &deadlineConn{pipeConn: *d.client.(*pipeConn)}
	d.client = dc
	cfg := testConfig(t, d, Ed25519Verifier{Pub: pub, Enabled: true})
	srv := &serverSide{t: t, key: cfg.KeyData, build: cfg.BuildKey, priv: priv, pub: pub, conn: d.serverNet}
	dosBody = bytes.Repeat([]byte{0x11}, 32)
	done := make(chan struct{})
	go func() { defer close(done); srv.run(1) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()
	if !dc.last.IsZero() {
		t.Fatalf("deadline left at %v after Connect", dc.last)
	}
	_, _ = sess.Read()
	<-done
}
