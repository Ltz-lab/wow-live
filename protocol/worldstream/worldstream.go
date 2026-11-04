// Package worldstream decrypts world connections as they are captured: the
// incremental form of tools/worlddecrypt (E45). Feed it TCP segments in
// capture order (Tail reads them from a pcapng file tcpdump is still
// writing) and it hands back each message the moment its frame is complete:
// TCP reassembly per direction, framing, the session's AES-GCM key (found
// from bnetrelay's secrets.jsonl, re-read until a key opens the connection),
// the persistent deflate stream, SMSG_COMPRESSED_PACKET and
// SMSG_MULTIPLE_PACKETS unpacked.
package worldstream

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/framing"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/world"
)

// WorldPort is the world server's TCP port.
const WorldPort = 3724

// Segment is one TCP segment of a world connection.
type Segment struct {
	T            time.Time
	Src, Dst     string
	SPort, DPort uint16
	Seq          uint32
	SYN, FIN     bool
	Payload      []byte
}

// Msg is one decrypted world message.
type Msg struct {
	T      time.Time
	Port   uint16 // the client's port: one per connection
	Dir    opcodes.Direction
	Opcode uint32
	Name   string // opcodes.DescribeBuild's text, "Group/0xN ~SMSG_NAME (confidence)"
	Body   []byte
	Note   string // "decrypt failed", "inflate: ..." when something went wrong
}

var shortName = regexp.MustCompile(`~?([CS]MSG_\w+)`)

// Short is the message's bare name (SMSG_SPELL_GO), or "" when unnamed.
func (m Msg) Short() string {
	if s := shortName.FindStringSubmatch(m.Name); s != nil {
		return s[1]
	}
	return ""
}

// Builds the decoder names messages for. Retail 12.1.5.70783 has no table
// yet; its numbers are Forever 70755's with the group (high 16 bits) two
// lower (the sibling branch's worlddecrypt -build 70783, RETAIL.md), so its
// names come from the 70755 table by that shift.
const (
	BuildForever = 70755
	BuildRetail  = 70783
)

// Decoder follows every world connection in a capture.
type Decoder struct {
	// Build names the messages: BuildForever, BuildRetail, or 0 to tell
	// from the first SMSG_AUTH_CHALLENGE (0x4E0000 Forever, 0x4C0000 retail).
	Build       int
	SecretsPath string

	conns       map[string]*conn
	sessionKeys [][]byte
}

// New returns a decoder that reads keys from bnetrelay's secrets.jsonl.
func New(build int, secretsPath string) *Decoder {
	return &Decoder{Build: build, SecretsPath: secretsPath, conns: map[string]*conn{}}
}

type frame struct {
	t     time.Time
	index uint64 // the frame's position in its direction: the GCM nonce counter
	tag   [framing.TagLen]byte
	clear bool
	op    uint32
	body  []byte
}

type dirState struct {
	next    uint32
	started bool
	pending map[uint32]Segment // out-of-order segments by seq
	buf     []byte             // in-order bytes not yet framed
	bufT    time.Time          // arrival of the newest bytes in buf
	hs      bool               // handshake line consumed
	count   uint64
	held    []frame // frames waiting for the key
	inf     *inflater
}

type conn struct {
	port     uint16
	c2s, s2c dirState
	key      *[world.EncryptionKeyLen]byte
	first    *frame // first client frame: AUTH_SESSION or AUTH_CONTINUED_SESSION
	chal     []byte // server challenge from SMSG_AUTH_CHALLENGE
	sk       []byte // this connection's session key, once found
	dead     bool
}

// Feed takes one segment and returns the messages it completed.
func (d *Decoder) Feed(s Segment) []Msg {
	var key string
	var c2s bool
	switch {
	case s.DPort == WorldPort:
		key, c2s = fmt.Sprintf("%s:%d", s.Dst, s.SPort), true
	case s.SPort == WorldPort:
		key = fmt.Sprintf("%s:%d", s.Src, s.DPort)
	default:
		return nil
	}
	c := d.conns[key]
	if c == nil {
		port := s.DPort
		if c2s {
			port = s.SPort
		}
		c = &conn{port: port}
		d.conns[key] = c
	}
	if c.dead {
		return nil
	}
	ds := &c.s2c
	if c2s {
		ds = &c.c2s
	}
	var fresh []frame
	for _, chunk := range ds.reassemble(s) {
		fresh = append(fresh, ds.frames(chunk)...)
	}
	if len(fresh) == 0 {
		return nil
	}
	return d.handle(c, c2s, fresh)
}

// reassemble returns in-order bytes the segment makes available.
func (ds *dirState) reassemble(s Segment) [][]byte {
	if ds.pending == nil {
		ds.pending = map[uint32]Segment{}
	}
	if s.SYN {
		ds.next, ds.started = s.Seq+1, true
		return nil
	}
	if !ds.started {
		ds.next, ds.started = s.Seq, true // capture began mid-stream
	}
	if len(s.Payload) == 0 {
		return nil
	}
	ds.pending[s.Seq] = s
	var out [][]byte
	for progressed := true; progressed; {
		progressed = false
		for seq, p := range ds.pending {
			off := int32(ds.next - seq) // bytes of p already delivered
			if off < 0 {
				continue
			}
			delete(ds.pending, seq)
			if int(off) < len(p.Payload) {
				out = append(out, p.Payload[off:])
				ds.next += uint32(len(p.Payload) - int(off))
				ds.bufT = p.T
				progressed = true
			}
		}
	}
	return out
}

// frames appends bytes and cuts every complete frame.
func (ds *dirState) frames(b []byte) []frame {
	ds.buf = append(ds.buf, b...)
	if !ds.hs {
		nl := bytes.IndexByte(ds.buf, '\n')
		if nl < 0 {
			return nil
		}
		ds.buf, ds.hs = ds.buf[nl+1:], true
	}
	var out []frame
	for len(ds.buf) >= 4+framing.TagLen {
		size := int(binary.LittleEndian.Uint32(ds.buf))
		end := 4 + framing.TagLen + size
		if size < 4 || size > 1<<20 {
			ds.buf = nil // lost framing; nothing after this can be trusted
			break
		}
		if len(ds.buf) < end {
			break
		}
		f := frame{t: ds.bufT, index: ds.count}
		ds.count++
		copy(f.tag[:], ds.buf[4:])
		f.clear = f.tag == [framing.TagLen]byte{}
		payload := append([]byte(nil), ds.buf[4+framing.TagLen:end]...)
		if f.clear {
			f.op, f.body = binary.LittleEndian.Uint32(payload), payload[4:]
		} else {
			f.body = payload
		}
		out = append(out, f)
		ds.buf = ds.buf[end:]
	}
	return out
}

func (d *Decoder) handle(c *conn, c2s bool, fresh []frame) []Msg {
	ds := &c.s2c
	if c2s {
		ds = &c.c2s
	}
	if c.key == nil {
		for i := range fresh {
			f := fresh[i]
			if f.index == 0 && f.clear {
				if c2s {
					c.first = &f
				} else if len(f.body) >= 64 {
					c.chal = append([]byte(nil), f.body[32:64]...)
				}
			}
		}
		ds.held = append(ds.held, fresh...)
		if d.Build == 0 && !c2s {
			for _, f := range fresh {
				if f.index == 0 && f.clear {
					switch f.op {
					case 0x4E0000:
						d.Build = BuildForever
					case 0x4C0000:
						d.Build = BuildRetail
					}
				}
			}
		}
		if !d.findKey(c) {
			return nil
		}
		var out []Msg
		out = append(out, d.open(c, false, c.s2c.held)...)
		out = append(out, d.open(c, true, c.c2s.held)...)
		c.s2c.held, c.c2s.held = nil, nil
		return out
	}
	return d.open(c, c2s, fresh)
}

// findKey tries every key the secrets allow on the first encrypted server
// frame. It is called again on every new frame until one works.
func (d *Decoder) findKey(c *conn) bool {
	if c.first == nil || c.chal == nil {
		return false
	}
	var probe *frame
	for i := range c.s2c.held {
		if !c.s2c.held[i].clear {
			probe = &c.s2c.held[i]
			break
		}
	}
	if probe == nil {
		return false
	}
	first := d.name(opcodes.ClientToServer, c.first.op)
	if len(c.first.body) == world.AuthContinuedSessionLen {
		first = "CMSG_AUTH_CONTINUED_SESSION" // retail moved even the auth numbers
	}
	type cand struct {
		key [world.EncryptionKeyLen]byte
		sk  []byte
	}
	var cands []cand
	if strings.Contains(first, "CONTINUED") {
		a, err := world.DecodeAuthContinuedSession(c.first.body)
		if err != nil {
			c.dead = true
			return false
		}
		for _, sk := range d.sessionKeys {
			if k, err := world.EncryptionKey(sk, a.LocalChallenge[:], c.chal); err == nil {
				cands = append(cands, cand{k, nil})
			}
		}
	} else {
		a, err := world.DecodeAuthSessionBody(c.first.body)
		if err != nil {
			c.dead = true
			return false
		}
		clients, joins, err := ReadSecrets(d.SecretsPath)
		if err != nil {
			return false
		}
		for _, cs := range clients {
			for _, js := range joins {
				kd := append(append([]byte(nil), cs...), js...)
				sk, err := world.SessionKey(kd, a.LocalChallenge[:], c.chal)
				if err != nil {
					continue
				}
				if k, err := world.EncryptionKey(sk[:], a.LocalChallenge[:], c.chal); err == nil {
					cands = append(cands, cand{k, append([]byte(nil), sk[:]...)})
				}
			}
		}
	}
	for _, k := range cands {
		ci, err := world.NewCipherAt(k.key[:], world.Recv, probe.index)
		if err != nil {
			continue
		}
		if _, err := ci.Open(nil, probe.body, probe.tag[:]); err == nil {
			key := k.key
			c.key = &key
			if k.sk != nil {
				c.sk = k.sk
				d.sessionKeys = append(d.sessionKeys, k.sk)
			}
			return true
		}
	}
	return false
}

func (d *Decoder) open(c *conn, c2s bool, fs []frame) []Msg {
	ds, wdir, odir := &c.s2c, world.Recv, opcodes.ServerToClient
	if c2s {
		ds, wdir, odir = &c.c2s, world.Send, opcodes.ClientToServer
	}
	if ds.inf == nil {
		ds.inf = newInflater()
	}
	var out []Msg
	for _, f := range fs {
		op, body := f.op, f.body
		if !f.clear {
			ci, err := world.NewCipherAt(c.key[:], wdir, f.index)
			if err != nil {
				continue
			}
			plain, err := ci.Open(nil, f.body, f.tag[:])
			if err != nil || len(plain) < 4 {
				out = append(out, Msg{T: f.t, Port: c.port, Dir: odir, Note: "decrypt failed"})
				continue
			}
			op, body = binary.LittleEndian.Uint32(plain), plain[4:]
		}
		var expand func(op uint32, body []byte, depth int)
		expand = func(op uint32, body []byte, depth int) {
			n := d.name(odir, op)
			switch {
			case depth < 3 && strings.Contains(n, "COMPRESSED_PACKET") && len(body) >= 12:
				plain, err := ds.inf.inflate(body)
				if err == nil && len(plain) >= 4 {
					expand(binary.LittleEndian.Uint32(plain), plain[4:], depth+1)
					return
				}
				out = append(out, Msg{T: f.t, Port: c.port, Dir: odir, Opcode: op, Name: n, Body: body, Note: fmt.Sprintf("inflate: %v", err)})
				return
			case depth < 3 && strings.Contains(n, "MULTIPLE_PACKETS"):
				b := body
				for len(b) >= 6 {
					sz := int(binary.LittleEndian.Uint16(b))
					if len(b) < 6+sz {
						break
					}
					expand(binary.LittleEndian.Uint32(b[2:]), b[6:6+sz], depth+1)
					b = b[6+sz:]
				}
				return
			}
			out = append(out, Msg{T: f.t, Port: c.port, Dir: odir, Opcode: op, Name: n, Body: body})
		}
		expand(op, body, 0)
	}
	return out
}

// retailC2S names the retail client messages the combat feed needs, read off
// the retail capture of 2026-11-05 03:02 (C2S does not follow the S2C group
// shift): 0x430016 carries the character's GUID, then 600.0 (the far clip)
// and a bit, once, before world entry, as CMSG_PLAYER_LOGIN (Forever
// 0x440016) does; 0x3E00CA carries one GUID, later an empty one, and the
// player's next 44 hits went to that GUID.
var retailC2S = map[uint32]string{
	0x430016: "CMSG_PLAYER_LOGIN",
	0x3E00CA: "CMSG_SET_SELECTION",
}

// name describes a message number for the decoder's build.
func (d *Decoder) name(dir opcodes.Direction, op uint32) string {
	switch d.Build {
	case BuildRetail:
		if dir == opcodes.ClientToServer {
			if n, ok := retailC2S[op]; ok {
				return n + "~"
			}
		}
		if n := opcodes.DescribeBuild(BuildForever, dir, op+0x20000); !strings.HasPrefix(n, "0x") {
			return n + "~" // named by the group shift: a guess per message
		}
		return fmt.Sprintf("%#08x", op)
	case 0:
		return opcodes.DescribeBuild(BuildForever, dir, op)
	}
	return opcodes.DescribeBuild(d.Build, dir, op)
}

// ReadSecrets reads bnetrelay's secrets.jsonl: every client secret
// (Param_ClientInfo) and join secret (Param_JoinSecret) it has logged.
func ReadSecrets(path string) (clients, joins [][]byte, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	secretRE := regexp.MustCompile(`"secret":\[([0-9, ]+)\]`)
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r struct {
			ClientInfo string `json:"client_info"`
			JoinSecret string `json:"join_secret"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue // a line still being written
		}
		if m := secretRE.FindStringSubmatch(r.ClientInfo); m != nil {
			var b []byte
			for _, s := range strings.Split(m[1], ",") {
				var v int
				fmt.Sscan(strings.TrimSpace(s), &v)
				b = append(b, byte(v))
			}
			clients = append(clients, b)
		}
		if r.JoinSecret != "" {
			if b, err := hex.DecodeString(r.JoinSecret); err == nil {
				joins = append(joins, b)
			}
		}
	}
	if len(clients) == 0 || len(joins) == 0 {
		return nil, nil, errors.New("secrets: need a client secret and a join secret")
	}
	return clients, joins, nil
}

// inflater is one direction's persistent raw-deflate stream
// (SMSG_COMPRESSED_PACKET: u32 size, u32 plain adler, u32 compressed adler,
// deflate bytes).
type inflater struct {
	feed *feeder
	r    io.ReadCloser
}

type feeder struct{ buf []byte }

func (f *feeder) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		return 0, io.ErrNoProgress
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func (f *feeder) ReadByte() (byte, error) {
	if len(f.buf) == 0 {
		return 0, io.ErrNoProgress
	}
	b := f.buf[0]
	f.buf = f.buf[1:]
	return b, nil
}

func newInflater() *inflater {
	fd := &feeder{}
	return &inflater{feed: fd, r: flate.NewReader(fd)}
}

func (in *inflater) inflate(body []byte) ([]byte, error) {
	size := binary.LittleEndian.Uint32(body)
	if size > 1<<20 {
		return nil, fmt.Errorf("claims %d bytes", size)
	}
	in.feed.buf = append(in.feed.buf, body[12:]...)
	plain := make([]byte, size)
	_, err := io.ReadFull(in.r, plain)
	return plain, err
}
