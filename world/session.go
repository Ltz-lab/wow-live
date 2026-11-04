package world

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/framing"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/transport"
)

// Opcodes the connection itself needs. They are looked up by name in the
// versioned table so a build with a different numbering only needs a new table.
const (
	opAuthChallenge    = "SMSG_AUTH_CHALLENGE"
	opAuthSession      = "CMSG_AUTH_SESSION"
	opAuthContinued    = "CMSG_AUTH_CONTINUED_SESSION"
	opEnterEncrypted   = "SMSG_ENTER_ENCRYPTED_MODE"
	opEncryptedModeAck = "CMSG_ENTER_ENCRYPTED_MODE_ACK"
	opLogDisconnect    = "CMSG_LOG_DISCONNECT"
)

var (
	// ErrHandshake means the server's connect line was not the shipped one.
	ErrHandshake = errors.New("world: unexpected server handshake")
	// ErrServerIdentity means the ENTER_ENCRYPTED_MODE signature did not verify.
	ErrServerIdentity = errors.New("world: server identity check failed")
	// ErrEncoding means the server sent a message the protocol does not allow
	// in the current mode.
	ErrEncoding = errors.New("world: unexpected message for the connection mode")
	// ErrMode means a mode transition was out of order.
	ErrMode = errors.New("world: encryption mode change out of order")
)

// Verifier checks a SMSG_ENTER_ENCRYPTED_MODE signature (ed25519ctx over
// HMAC-SHA512(encryptKey, enabled ‖ seed)) against the shipped key store.
//
// The store is game data (TACT file 0x75E1DA), keyed by region group
// (research/encryption.md §2), so it is a dependency rather than part of this
// package.
type Verifier interface {
	VerifySignature(regionGroup uint32, signed, sig []byte, enabled bool) (bool, error)
}

// Ed25519Verifier verifies with a fixed public key. It is the shape a loaded
// key store implements per region group; tests use it directly.
type Ed25519Verifier struct {
	Pub     ed25519.PublicKey
	Enabled bool
}

// VerifySignature implements Verifier.
func (v Ed25519Verifier) VerifySignature(_ uint32, signed, sig []byte, enabled bool) (bool, error) {
	if len(v.Pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false, nil
	}
	return ed25519.VerifyWithOptions(v.Pub, signed, sig, &ed25519.Options{
		Context: string(EnableEncryptionContext),
	}) == nil, nil
}

// AcceptAllVerifier trusts any signature. It is what a connection to a server
// whose key store is unknown falls back to, and it says so.
type AcceptAllVerifier struct{}

// VerifySignature implements Verifier.
func (AcceptAllVerifier) VerifySignature(_ uint32, _, _ []byte, _ bool) (bool, error) {
	return true, nil
}

// Config is everything one world connection needs.
type Config struct {
	Dialer transport.Dialer
	Target transport.Target
	Table  *opcodes.Table
	// KeyData is the 64-byte Battle.net session-key material from the
	// realm-join ticket, and Ticket the ticket itself.
	KeyData []byte
	Ticket  []byte
	// BuildKey is the 16-byte per-build auth key. Nil means the known key of
	// Table's build (BuildAuthKey; 70707 when there is no table); set
	// SkipBuildKey to send none, which only a server with its check disabled
	// accepts.
	BuildKey *[BuildKeyLen]byte
	// SkipBuildKey omits the build key from the digest entirely.
	SkipBuildKey bool
	Region       uint32
	// Battlegroup and Realm are sent as-is; the client reads all three from its
	// realm identity (client 0x1014b336c, 0x1014b337c, 0x1014b3384).
	Battlegroup uint32
	Realm       uint32
	UseIPv6     bool
	// Timeout bounds the handshake and auth phase. Zero means 30 s.
	Timeout time.Duration
	// Verifier checks the server identity; nil uses AcceptAllVerifier.
	Verifier Verifier
	// DoSMaxNonce bounds the hashcash search; zero means unbounded.
	DoSMaxNonce uint64
}

// Session is a connection past auth. It is not safe for concurrent use; a
// caller reads and writes from one goroutine or serializes itself.
type Session struct {
	conn   transport.Conn
	stream *framing.Stream
	send   *Cipher
	recv   *Cipher

	table *opcodes.Table
	ids   struct {
		ack     uint32
		disc    uint32
		encMode uint32
	}

	// sessionKey is the 40-byte key this connection derived. A continued
	// session (SMSG_CONNECT_TO) authenticates with it and derives its own
	// encryption key from it.
	sessionKey [SessionKeyLen]byte

	encrypted bool
	closed    bool
	buf       []byte
	pending   []framing.Message
	// pendingClear counts the queued messages that arrived before the cipher
	// armed and are therefore still plaintext.
	pendingClear int
	// framesSent/framesRecv count the frames of each direction from the first
	// one, cleartext included. The GCM nonce counter continues across the
	// switch to encrypted mode instead of restarting at 0, so these are the
	// counters the ciphers are armed with (research/encryption.md §1).
	framesSent, framesRecv uint64
	readBuf                [4096]byte
}

func (c *Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

func (c *Config) verifier() Verifier {
	if c.Verifier != nil {
		return c.Verifier
	}
	return AcceptAllVerifier{}
}

// Connect dials, performs the handshake and auth exchange, verifies the server
// identity, sends the encrypted-mode ACK and arms the cipher.
//
// It returns only once the connection is fully established; messages the server
// sends after ENTER_ENCRYPTED_MODE stay buffered and come out of Read.
func Connect(ctx context.Context, c *Config) (*Session, error) {
	if c.Dialer == nil || c.Table == nil {
		return nil, errors.New("world: config needs a dialer and an opcode table")
	}
	if len(c.KeyData) != KeyDataLen {
		return nil, fmt.Errorf("world: key data must be %d bytes", KeyDataLen)
	}
	if !c.SkipBuildKey && c.BuildKey == nil {
		if _, ok := BuildAuthKey(c.Table.Build); !ok {
			return nil, fmt.Errorf("world: no auth key known for build %d; set BuildKey or SkipBuildKey", c.Table.Build)
		}
	}
	_, encID, err := c.Table.RequireID(opEnterEncrypted)
	if err != nil {
		return nil, err
	}
	_, ackID, err := c.Table.RequireID(opEncryptedModeAck)
	if err != nil {
		return nil, err
	}
	_, authID, err := c.Table.RequireID(opAuthSession)
	if err != nil {
		return nil, err
	}
	_, discID, err := c.Table.RequireID(opLogDisconnect)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.Table.RequireID(opAuthChallenge); err != nil {
		return nil, err
	}
	return dialAndAuth(ctx, c.Dialer, c.Target, c.Table, c.timeout(), encID, ackID, discID,
		func(s *Session) error { return s.auth(c, authID) })
}

// dialAndAuth dials, runs the handshake line, then auth, and clears the
// handshake deadline. Connect and ConnectContinued differ only in auth.
func dialAndAuth(ctx context.Context, d transport.Dialer, target transport.Target, table *opcodes.Table,
	timeout time.Duration, encID, ackID, discID uint32, auth func(*Session) error) (*Session, error) {
	conn, err := d.Dial(ctx, target)
	if err != nil {
		return nil, err
	}
	s := &Session{conn: conn, table: table}
	s.ids.encMode, s.ids.ack, s.ids.disc = encID, ackID, discID
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := s.handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	// The line was read straight off the socket, so the frame stream starts
	// already past it.
	s.stream = framing.NewStream(framing.TaggedCrypt{})
	if err := auth(s); err != nil {
		s.disconnect(4)
		conn.Close()
		return nil, err
	}
	// The deadline bounds the handshake only. Left in place it kills the
	// session with an i/o timeout once c.timeout() has passed since the dial,
	// however healthy the connection is; keepalive is the session loop's job.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// handshake reads the server's connect line, answers it, and returns.
func (s *Session) handshake() error {
	line, err := s.readHandshake()
	if err != nil {
		return err
	}
	if string(line) != framing.ServerConnect {
		return fmt.Errorf("%w: %q", ErrHandshake, line)
	}
	if _, err := s.conn.Write(framing.ConnectLine()); err != nil {
		return err
	}
	return nil
}

func (s *Session) readHandshake() ([]byte, error) {
	var one [1]byte
	var line []byte
	for {
		n, err := s.conn.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				// Anything already pushed into the stream is handled by the
				// normal read loop; the line itself is complete.
				return line, nil
			}
			line = append(line, one[0])
			if len(line) > framing.MaxHandshakeLine {
				return nil, framing.ErrHandshakeTooLong
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
	}
}

// auth runs the challenge response up to and including the ACK.
func (s *Session) auth(c *Config, authID uint32) error {
	// SMSG_AUTH_CHALLENGE must be the first framed message.
	msg, err := s.next(ChallengeLen)
	if err != nil {
		return err
	}
	if msg.Opcode != replyOpcode(c.Table, "SMSG_AUTH_CHALLENGE") {
		return fmt.Errorf("%w: first message %#08x is not AUTH_CHALLENGE", ErrEncoding, msg.Opcode)
	}
	var dos [32]byte
	copy(dos[:], msg.Body[:32])
	var server [ChallengeLen]byte
	copy(server[:], msg.Body[32:64])
	zeroBits := msg.Body[64]

	var local [ChallengeLen]byte
	if _, err := rand.Read(local[:]); err != nil {
		return err
	}
	digest, err := AuthDigest(c.KeyData, c.buildKey(), local[:], server[:])
	if err != nil {
		return err
	}
	sessionKey, err := SessionKey(c.KeyData, local[:], server[:])
	if err != nil {
		return err
	}
	encKey, err := EncryptionKey(sessionKey[:], local[:], server[:])
	if err != nil {
		return err
	}
	s.sessionKey = sessionKey
	nonce, err := DoSProof(local[:], server[:], digest[:], dos[:], zeroBits, c.DoSMaxNonce)
	if os.Getenv("WOW_DBG_KEY") != "" {
		fmt.Fprintf(os.Stderr, "DBG server=%x local=%x enc=%x keydata=%x sess=%x\n", server, local, encKey, c.KeyData, sessionKey)
	}
	if err != nil {
		return err
	}

	a := AuthSession{
		DosResponse:   nonce,
		RegionID:      c.Region,
		BattlegroupID: c.Battlegroup,
		RealmID:       c.Realm,
		UseIPv6:       c.UseIPv6,
		Ticket:        c.Ticket,
	}
	a.LocalChallenge = local
	a.Digest = digest
	if _, err := s.conn.Write(framing.Frame(framing.Tagged{}, authID, a.Encode(0)[4:])); err != nil {
		return err
	}
	s.framesSent++

	// The server answers with ENTER_ENCRYPTED_MODE.
	msg, err = s.next(1)
	if err != nil {
		return err
	}
	if msg.Opcode != s.ids.encMode {
		return fmt.Errorf("%w: expected ENTER_ENCRYPTED_MODE, got %#08x", ErrMode, msg.Opcode)
	}
	if err := s.handleEnterEncryptedMode(msg.Body, encKey[:], c.verifier()); err != nil {
		return err
	}
	return nil
}

// ContinuedConfig is everything a continued session needs: where
// SMSG_CONNECT_TO pointed, what it handed over, and the first connection's
// session key.
type ContinuedConfig struct {
	Dialer     transport.Dialer
	Target     transport.Target
	Table      *opcodes.Table
	SessionKey [SessionKeyLen]byte
	// ConnectTo is the decoded SMSG_CONNECT_TO; Key, NativeRealmAddress and
	// Key3 are echoed back.
	ConnectTo ConnectTo
	// Timeout bounds the handshake and auth phase. Zero means 30 s.
	Timeout     time.Duration
	Verifier    Verifier
	DoSMaxNonce uint64
}

// ConnectContinued opens the connection SMSG_CONNECT_TO asked for: the same
// handshake line and AUTH_CHALLENGE as Connect, answered with
// CMSG_AUTH_CONTINUED_SESSION instead of AUTH_SESSION, then the same signed
// ENTER_ENCRYPTED_MODE and ACK.
//
// The digest is HMAC-SHA512(sessionKey, key ‖ local ‖ server ‖
// ContinuedSessionSeed)[:24], the hashcash takes the session key where the
// first connection takes the digest (client 0x101094f9c), and the encryption
// key is derived from the same session key with this connection's challenges
// (the fork's HandleAuthContinuedSessionCallback).
func ConnectContinued(ctx context.Context, c *ContinuedConfig) (*Session, error) {
	if c.Dialer == nil || c.Table == nil {
		return nil, errors.New("world: continued config needs a dialer and an opcode table")
	}
	ids := make([]uint32, 4)
	for i, name := range []string{opEnterEncrypted, opEncryptedModeAck, opAuthContinued, opLogDisconnect} {
		_, id, err := c.Table.RequireID(name)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	if _, _, err := c.Table.RequireID(opAuthChallenge); err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return dialAndAuth(ctx, c.Dialer, c.Target, c.Table, timeout, ids[0], ids[1], ids[3],
		func(s *Session) error { return s.authContinued(c, ids[2]) })
}

func (s *Session) authContinued(c *ContinuedConfig, authID uint32) error {
	msg, err := s.next(ChallengeLen)
	if err != nil {
		return err
	}
	if msg.Opcode != replyOpcode(c.Table, opAuthChallenge) {
		return fmt.Errorf("%w: first message %#08x is not AUTH_CHALLENGE", ErrEncoding, msg.Opcode)
	}
	if len(msg.Body) < 65 {
		return fmt.Errorf("%w: AUTH_CHALLENGE is %d bytes", ErrEncoding, len(msg.Body))
	}
	dos := msg.Body[:32]
	server := msg.Body[32:64]
	zeroBits := msg.Body[64]

	var local [ChallengeLen]byte
	if _, err := rand.Read(local[:]); err != nil {
		return err
	}
	digest, err := ContinuedSessionDigest(c.SessionKey[:], c.ConnectTo.Key, local[:], server)
	if err != nil {
		return err
	}
	encKey, err := EncryptionKey(c.SessionKey[:], local[:], server)
	if err != nil {
		return err
	}
	nonce, err := DoSProof(local[:], server, c.SessionKey[:], dos, zeroBits, c.DoSMaxNonce)
	if err != nil {
		return err
	}
	s.sessionKey = c.SessionKey
	a := AuthContinuedSession{
		DosResponse:        nonce,
		LocalChallenge:     local,
		Digest:             digest,
		Key:                c.ConnectTo.Key,
		NativeRealmAddress: c.ConnectTo.NativeRealmAddress,
		Key3:               c.ConnectTo.Key3,
	}
	if _, err := s.conn.Write(framing.Frame(framing.Tagged{}, authID, a.Encode(0)[4:])); err != nil {
		return err
	}
	s.framesSent++

	msg, err = s.next(1)
	if err != nil {
		return err
	}
	if msg.Opcode != s.ids.encMode {
		return fmt.Errorf("%w: expected ENTER_ENCRYPTED_MODE, got %#08x", ErrMode, msg.Opcode)
	}
	v := c.Verifier
	if v == nil {
		v = AcceptAllVerifier{}
	}
	return s.handleEnterEncryptedMode(msg.Body, encKey[:], v)
}

// SessionKey returns the 40-byte session key the connection derived (or, for
// a continued session, was given).
func (s *Session) SessionKey() [SessionKeyLen]byte { return s.sessionKey }

// handleEnterEncryptedMode verifies the signature, sends the ACK and arms the
// cipher. The ACK is the last cleartext frame, exactly as the client does
// (research/encryption.md §2).
func (s *Session) handleEnterEncryptedMode(body, encKey []byte, v Verifier) error {
	if len(body) < 4+64 {
		return fmt.Errorf("%w: ENTER_ENCRYPTED_MODE body is %d bytes", ErrEncoding, len(body))
	}
	region := uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24
	sig := body[4:68]
	enabled := body[68]&0x80 != 0
	ok, err := v.VerifySignature(region, EnableEncryptionMessage(encKey, enabled), sig, enabled)
	if err != nil {
		return err
	}
	if !ok {
		return ErrServerIdentity
	}
	if _, err := s.conn.Write(framing.Frame(framing.Tagged{}, s.ids.ack, nil)); err != nil {
		return err
	}
	s.framesSent++
	if !enabled {
		return nil
	}
	// The ACK is the last cleartext frame but it still consumes nonce 1; the
	// counters continue, they do not restart at 0, so the cipher starts at the
	// number of frames already written (AUTH_SESSION and the ACK).
	if s.send, err = NewCipherAt(encKey, Send, s.framesSent); err != nil {
		return err
	}
	if s.recv, err = NewCipherAt(encKey, Recv, s.framesRecv); err != nil {
		return err
	}
	s.encrypted = true
	s.pendingClear = len(s.pending)
	return nil
}

// next reads exactly one message with at least minLen body bytes.
func (s *Session) next(minLen int) (framing.Message, error) {
	for {
		msgs, err := s.stream.Push(s.buf)
		s.buf = nil
		if err != nil {
			return framing.Message{}, err
		}
		if len(msgs) > 0 {
			m := msgs[0]
			s.framesRecv++
			for _, extra := range msgs[1:] {
				s.pending = append(s.pending, extra)
			}
			if len(m.Body) < minLen {
				return m, fmt.Errorf("%w: %#08x body is %d bytes, want %d", ErrEncoding, m.Opcode, len(m.Body), minLen)
			}
			return m, nil
		}
		n, err := s.conn.Read(s.readBuf[:])
		if n > 0 {
			s.buf = append(s.buf, s.readBuf[:n]...)
		}
		if err != nil {
			return framing.Message{}, err
		}
	}
}

// Read returns the next message, decrypting it when the cipher is armed.
func (s *Session) Read() (framing.Message, error) {
	if s.closed {
		return framing.Message{}, transport.ErrClosed
	}
	if len(s.pending) > 0 {
		m := s.pending[0]
		s.pending = s.pending[1:]
		s.framesRecv++
		if s.pendingClear > 0 {
			s.pendingClear--
			return m, nil
		}
		return s.unseal(m)
	}
	for {
		msgs, err := s.stream.Push(s.buf)
		s.buf = nil
		if err != nil {
			return framing.Message{}, err
		}
		if len(msgs) > 0 {
			s.pending = append(s.pending, msgs[1:]...)
			s.framesRecv++
			if s.pendingClear > 0 {
				s.pendingClear--
				return msgs[0], nil
			}
			return s.unseal(msgs[0])
		}
		n, err := s.conn.Read(s.readBuf[:])
		if n > 0 {
			s.buf = append(s.buf, s.readBuf[:n]...)
		}
		if err != nil {
			return framing.Message{}, err
		}
	}
}

func (s *Session) unseal(m framing.Message) (framing.Message, error) {
	if !s.encrypted {
		return m, nil
	}
	plain, err := s.recv.Open(nil, m.Body, m.Tag[:])
	if err != nil {
		return m, fmt.Errorf("world: unseal len=%d counter=%d: %w", len(m.Body), s.recv.Counter()-1, err)
	}
	if len(plain) < 4 {
		return m, ErrEncoding
	}
	m.Opcode = uint32(plain[0]) | uint32(plain[1])<<8 | uint32(plain[2])<<16 | uint32(plain[3])<<24
	m.Body = plain[4:]
	return m, nil
}

// Send frames one message, sealing it when the cipher is armed.
func (s *Session) Send(opcode uint32, body []byte) error {
	plain := make([]byte, 0, len(body)+4)
	plain = append(plain, byte(opcode), byte(opcode>>8), byte(opcode>>16), byte(opcode>>24))
	plain = append(plain, body...)

	if !s.encrypted {
		if _, err := s.conn.Write(framing.Frame(framing.Tagged{}, opcode, body)); err != nil {
			return err
		}
		s.framesSent++
		return nil
	}
	sealed, err := s.send.Seal(make([]byte, 0, len(plain)+framing.TagLen), plain)
	if err != nil {
		return err
	}
	overhead := framing.TagLen
	ct, tag := sealed[:len(sealed)-overhead], sealed[len(sealed)-overhead:]
	// The header carries the first four ciphertext bytes, not the opcode: the
	// receiver keeps those in its header struct and prepends them back, so on
	// the wire the payload is one continuous ciphertext behind the tag, and
	// the size counts it whole (opcode included).
	hdr := framing.TaggedCrypt{}.EncodeSealed(ct, [framing.TagLen]byte(tag))
	if os.Getenv("WOW_DBG_FRAME") != "" {
		fmt.Fprintf(os.Stderr, "DBG SEND op=%#x plain=%x ct=%x hdr=%x rest=%x\n", opcode, plain, ct, hdr, ct[4:])
	}
	if _, err := s.conn.Write(hdr); err != nil {
		return err
	}
	_, err = s.conn.Write(ct[4:])
	return err
}

// disconnect sends CMSG_LOG_DISCONNECT with a reason, ignoring errors because
// the caller is already failing.
func (s *Session) disconnect(reason uint32) {
	if s.ids.disc == 0 {
		return
	}
	b := []byte{byte(reason), byte(reason >> 8), byte(reason >> 16), byte(reason >> 24)}
	_ = s.Send(s.ids.disc, b)
}

// Close closes the connection. A second Close is a no-op.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.conn.Close()
}

// Encrypted reports whether the cipher is armed.
func (s *Session) Encrypted() bool { return s.encrypted }

// Describe returns a log-safe description of the route.
func (s *Session) Describe() string { return s.conn.Describe() }

// buildKey resolves the digest's build key: an explicit one, or the known key
// of the table's build (70707's without a table), or none when SkipBuildKey
// is set.
func (c *Config) buildKey() []byte {
	if c.SkipBuildKey {
		return nil
	}
	if c.BuildKey != nil {
		return c.BuildKey[:]
	}
	if c.Table != nil {
		if k, ok := BuildAuthKey(c.Table.Build); ok {
			return k[:]
		}
		return nil
	}
	return ForeverBuildAuthKey[:]
}

func replyOpcode(t *opcodes.Table, name string) uint32 {
	if e, ok := t.ByName(name); ok && e.ID != nil {
		return *e.ID
	}
	return 0
}

// ensure sha512 stays referenced for the documented derivation
var _ = sha512.Size
