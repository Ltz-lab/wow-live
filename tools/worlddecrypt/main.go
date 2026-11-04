// Package main is worlddecrypt: the stock client's world connections,
// decrypted from a packet capture with the secrets tools/bnetrelay recorded.
//
// The world keys come from two Battle.net values (EVIDENCE E27, E34): the
// client secret the client puts in Param_ClientInfo, and Param_JoinSecret
// from the realm join. KeyData is client secret ‖ join secret; with the
// challenges each side sends in the clear (SMSG_AUTH_CHALLENGE's server
// challenge, CMSG_AUTH_SESSION's local challenge) that gives the session key
// and the GCM key (world.SessionKey, world.EncryptionKey). An instance
// connection (CMSG_AUTH_CONTINUED_SESSION) keys from the realm connection's
// session key and its own challenges. A frame's nonce is its index in its
// direction, cleartext frames included.
//
//	sudo tcpdump -i any -s 0 -U -w world.pcap 'tcp port 3724'   # while the client plays
//	go run ./tools/worlddecrypt -pcap world.pcap -secrets <relay run>/secrets.jsonl -out <dir>
//
// It writes <dir>/conn-<port>.jsonl per connection: every message with its
// time, direction, opcode, name and body in hex, compressed packets inflated.
package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/framing"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/world"
)

// build names the opcodes; -build 70783 for a retail capture (no table yet: numbers only).
var build = 70755

func main() {
	pcap := flag.String("pcap", "", "tcpdump capture of the world port")
	secrets := flag.String("secrets", "", "bnetrelay's secrets.jsonl")
	out := flag.String("out", "", "output directory (default: next to the capture)")
	flag.IntVar(&build, "build", build, "client build the opcode names come from")
	flag.Parse()
	if *pcap == "" || *secrets == "" {
		log.Fatal("worlddecrypt: need -pcap and -secrets")
	}
	if *out == "" {
		*out = strings.TrimSuffix(*pcap, filepath.Ext(*pcap)) + "-decrypted"
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		log.Fatal(err)
	}
	clientSecrets, joinSecrets, err := readSecrets(*secrets)
	if err != nil {
		log.Fatal(err)
	}
	segs, err := readPcapng(*pcap)
	if err != nil {
		log.Fatal(err)
	}
	conns := assemble(segs)
	log.Printf("%d TCP connections; %d client secret(s), %d join secret(s)", len(conns), len(clientSecrets), len(joinSecrets))

	// Realm connections first: an instance connection needs their session key.
	sort.SliceStable(conns, func(i, j int) bool { return conns[i].start.Before(conns[j].start) })
	var sessionKeys [][]byte
	for pass := 0; pass < 2; pass++ {
		for _, c := range conns {
			if c.done {
				continue
			}
			if err := decrypt(c, clientSecrets, joinSecrets, &sessionKeys, *out); err != nil {
				if pass == 1 {
					log.Printf("port %d: %v", c.clientPort, err)
				}
				continue
			}
			c.done = true
		}
	}
}

func readSecrets(path string) (clients, joins [][]byte, err error) {
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
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, nil, err
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
			b, err := hex.DecodeString(r.JoinSecret)
			if err != nil {
				return nil, nil, err
			}
			joins = append(joins, b)
		}
	}
	if len(clients) == 0 || len(joins) == 0 {
		return nil, nil, errors.New("secrets: need a client secret and a join secret")
	}
	return clients, joins, nil
}

// ---- pcapng and TCP ----

type segment struct {
	t            time.Time
	src, dst     string
	sport, dport uint16
	seq          uint32
	syn, fin     bool
	payload      []byte
}

func readPcapng(path string) ([]segment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var segs []segment
	var links []uint16
	le := binary.LittleEndian
	for p := 0; p+12 <= len(raw); {
		typ, blen := le.Uint32(raw[p:]), int(le.Uint32(raw[p+4:]))
		if blen < 12 || p+blen > len(raw) {
			break
		}
		b := raw[p+8 : p+blen-4]
		switch typ {
		case 1: // interface description
			links = append(links, le.Uint16(b))
		case 6: // enhanced packet
			iface := le.Uint32(b)
			ts := uint64(le.Uint32(b[4:]))<<32 | uint64(le.Uint32(b[8:]))
			capLen := int(le.Uint32(b[12:]))
			data := b[20 : 20+capLen]
			link := uint16(1)
			if int(iface) < len(links) {
				link = links[iface]
			}
			if s, ok := parsePacket(link, data); ok {
				s.t = time.UnixMicro(int64(ts))
				segs = append(segs, s)
			}
		}
		p += blen
	}
	if len(segs) == 0 {
		return nil, errors.New("pcap: no TCP segments (is it pcapng?)")
	}
	return segs, nil
}

func parsePacket(link uint16, d []byte) (segment, bool) {
	var s segment
	switch link {
	case 258: // PKTAP: its own header, then the inner link
		if len(d) < 8 {
			return s, false
		}
		hl := int(binary.LittleEndian.Uint32(d))
		inner := binary.LittleEndian.Uint32(d[4:])
		if hl > len(d) {
			return s, false
		}
		return parsePacket(uint16(inner), d[hl:])
	case 1: // Ethernet
		if len(d) < 14 {
			return s, false
		}
		d = d[14:]
	case 0: // BSD loopback
		if len(d) < 4 {
			return s, false
		}
		d = d[4:]
	case 12, 101: // raw IP
	default:
		return s, false
	}
	if len(d) < 20 {
		return s, false
	}
	var tcp []byte
	switch d[0] >> 4 {
	case 4:
		ihl := int(d[0]&0xf) * 4
		total := int(binary.BigEndian.Uint16(d[2:]))
		if d[9] != 6 || total > len(d) || ihl > total {
			return s, false
		}
		s.src = fmt.Sprintf("%d.%d.%d.%d", d[12], d[13], d[14], d[15])
		s.dst = fmt.Sprintf("%d.%d.%d.%d", d[16], d[17], d[18], d[19])
		tcp = d[ihl:total]
	case 6:
		if len(d) < 40 || d[6] != 6 {
			return s, false
		}
		plen := int(binary.BigEndian.Uint16(d[4:]))
		s.src, s.dst = hex.EncodeToString(d[8:24]), hex.EncodeToString(d[24:40])
		if 40+plen > len(d) {
			return s, false
		}
		tcp = d[40 : 40+plen]
	default:
		return s, false
	}
	if len(tcp) < 20 {
		return s, false
	}
	s.sport, s.dport = binary.BigEndian.Uint16(tcp), binary.BigEndian.Uint16(tcp[2:])
	s.seq = binary.BigEndian.Uint32(tcp[4:])
	off := int(tcp[12]>>4) * 4
	flags := tcp[13]
	s.syn, s.fin = flags&0x02 != 0, flags&0x01 != 0
	if off > len(tcp) {
		return s, false
	}
	s.payload = append([]byte(nil), tcp[off:]...)
	return s, true
}

type stream struct {
	isn     uint32
	haveISN bool
	parts   map[uint32][]byte // relative seq -> bytes
	times   map[uint32]time.Time
}

type conn struct {
	clientPort uint16
	server     string
	start      time.Time
	c2s, s2c   *stream
	done       bool
}

func assemble(segs []segment) []*conn {
	byKey := map[string]*conn{}
	var order []*conn
	for _, s := range segs {
		var c *conn
		var st **stream
		if s.dport == 3724 {
			key := fmt.Sprintf("%s:%d", s.dst, s.sport)
			if c = byKey[key]; c == nil {
				c = &conn{clientPort: s.sport, server: s.dst, start: s.t}
				byKey[key] = c
				order = append(order, c)
			}
			st = &c.c2s
		} else if s.sport == 3724 {
			key := fmt.Sprintf("%s:%d", s.src, s.dport)
			if c = byKey[key]; c == nil {
				c = &conn{clientPort: s.dport, server: s.src, start: s.t}
				byKey[key] = c
				order = append(order, c)
			}
			st = &c.s2c
		} else {
			continue
		}
		if *st == nil {
			*st = &stream{parts: map[uint32][]byte{}, times: map[uint32]time.Time{}}
		}
		x := *st
		if s.syn {
			x.isn, x.haveISN = s.seq+1, true
			continue
		}
		if !x.haveISN {
			// capture started mid-stream: take the first seen seq as the origin
			x.isn, x.haveISN = s.seq, true
		}
		if len(s.payload) == 0 {
			continue
		}
		rel := s.seq - x.isn
		if old, ok := x.parts[rel]; !ok || len(s.payload) > len(old) {
			x.parts[rel] = s.payload
			x.times[rel] = s.t
		}
	}
	return order
}

// bytes returns the stream in order, with the time each offset arrived.
func (x *stream) bytes() ([]byte, []int, []time.Time) {
	if x == nil {
		return nil, nil, nil
	}
	keys := make([]uint32, 0, len(x.parts))
	for k := range x.parts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var out []byte
	var offs []int
	var ts []time.Time
	for _, k := range keys {
		p := x.parts[k]
		if int(k) > len(out) {
			log.Printf("gap of %d bytes at %d", int(k)-len(out), len(out))
			break
		}
		if int(k)+len(p) <= len(out) {
			continue
		}
		offs = append(offs, len(out))
		ts = append(ts, x.times[k])
		out = append(out, p[len(out)-int(k):]...)
	}
	return out, offs, ts
}

func timeAt(offs []int, ts []time.Time, pos int) time.Time {
	i := sort.Search(len(offs), func(i int) bool { return offs[i] > pos }) - 1
	if i < 0 {
		i = 0
	}
	if len(ts) == 0 {
		return time.Time{}
	}
	return ts[i]
}

// ---- world frames ----

type frame struct {
	t      time.Time
	tag    [framing.TagLen]byte
	clear  bool
	opcode uint32
	body   []byte // cleartext: the payload; encrypted: ciphertext (opcode included)
}

// frames splits one direction after its handshake line.
func frames(b []byte, offs []int, ts []time.Time) ([]frame, error) {
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return nil, errors.New("no handshake line")
	}
	var fs []frame
	for p := nl + 1; p+4+framing.TagLen <= len(b); {
		size := int(binary.LittleEndian.Uint32(b[p:]))
		if size < 4 || size > 1<<20 {
			return fs, fmt.Errorf("frame at %d: size %d", p, size)
		}
		end := p + 4 + framing.TagLen + size
		if end > len(b) {
			break
		}
		var f frame
		f.t = timeAt(offs, ts, p)
		copy(f.tag[:], b[p+4:])
		f.clear = f.tag == [framing.TagLen]byte{}
		payload := b[p+4+framing.TagLen : end]
		if f.clear {
			f.opcode = binary.LittleEndian.Uint32(payload)
			f.body = payload[4:]
		} else {
			f.body = payload
		}
		fs = append(fs, f)
		p = end
	}
	return fs, nil
}

func name(dir opcodes.Direction, op uint32) string {
	if build == 70783 {
		// Retail 12.1.5.70783 has no table yet. Its opcodes look like Forever
		// 70755's with the group (high 16 bits) two lower: 0x4E000E
		// COMPRESSED_PACKET is 0x4C000E there, AUTH_SESSION 0x460001 is
		// 0x440001. Names found that way end in "~" (a guess, see RETAIL.md).
		if n := opcodes.DescribeBuild(70755, dir, op+0x20000); !strings.HasPrefix(n, "0x") {
			return n + "~"
		}
	}
	return opcodes.DescribeBuild(build, dir, op)
}

func decrypt(c *conn, clientSecrets, joinSecrets [][]byte, sessionKeys *[][]byte, outDir string) error {
	cb, coffs, cts := c.c2s.bytes()
	sb, soffs, sts := c.s2c.bytes()
	cf, err := frames(cb, coffs, cts)
	if err != nil && len(cf) == 0 {
		return fmt.Errorf("client side: %w", err)
	}
	sf, err := frames(sb, soffs, sts)
	if err != nil && len(sf) == 0 {
		return fmt.Errorf("server side: %w", err)
	}
	if len(cf) == 0 || len(sf) == 0 || !sf[0].clear || !cf[0].clear {
		return errors.New("no cleartext handshake")
	}
	if len(sf[0].body) < 64 {
		return errors.New("first server frame is not AUTH_CHALLENGE")
	}
	server := sf[0].body[32:64]
	first := name(opcodes.ClientToServer, cf[0].opcode)
	// Without a table for -build (retail 70783 moved even the auth opcodes),
	// tell the continued session by its fixed size.
	if len(cf[0].body) == world.AuthContinuedSessionLen {
		first = "CMSG_AUTH_CONTINUED_SESSION"
	}
	var keys [][world.EncryptionKeyLen]byte
	var sess [][]byte
	if strings.Contains(first, "CONTINUED") {
		a, err := world.DecodeAuthContinuedSession(cf[0].body)
		if err != nil {
			return err
		}
		if len(*sessionKeys) == 0 {
			return errors.New("instance connection before any realm connection decrypted")
		}
		for _, sk := range *sessionKeys {
			k, err := world.EncryptionKey(sk, a.LocalChallenge[:], server)
			if err != nil {
				return err
			}
			keys, sess = append(keys, k), append(sess, sk)
		}
	} else {
		a, err := world.DecodeAuthSessionBody(cf[0].body)
		if err != nil {
			return fmt.Errorf("%s: %w", first, err)
		}
		for _, cs := range clientSecrets {
			for _, js := range joinSecrets {
				kd := append(append([]byte(nil), cs...), js...)
				sk, err := world.SessionKey(kd, a.LocalChallenge[:], server)
				if err != nil {
					return err
				}
				k, err := world.EncryptionKey(sk[:], a.LocalChallenge[:], server)
				if err != nil {
					return err
				}
				keys, sess = append(keys, k), append(sess, append([]byte(nil), sk[:]...))
			}
		}
	}
	// The right key opens the first encrypted server frame.
	for i, k := range keys {
		if out, ok := open(c, k, cf, sf); ok {
			if !strings.Contains(first, "CONTINUED") {
				*sessionKeys = append(*sessionKeys, sess[i])
			}
			return write(c, out, outDir)
		}
	}
	return fmt.Errorf("no key opens it (%d tried): not a session from these secrets", len(keys))
}

type msg struct {
	T      string `json:"t"`
	Dir    string `json:"dir"`
	Opcode string `json:"opcode"`
	Name   string `json:"name"`
	Size   int    `json:"size"`
	Body   string `json:"body"`
	Note   string `json:"note,omitempty"`
}

func open(c *conn, key [world.EncryptionKeyLen]byte, cf, sf []frame) ([]msg, bool) {
	var out []msg
	ok := false
	for _, side := range []struct {
		fs  []frame
		dir world.Direction
		od  opcodes.Direction
		tag string
	}{{sf, world.Recv, opcodes.ServerToClient, "s2c"}, {cf, world.Send, opcodes.ClientToServer, "c2s"}} {
		inf := newInflater()
		for i, f := range side.fs {
			op, body := f.opcode, f.body
			note := ""
			if !f.clear {
				// world.Recv/Send are the client's own view: it receives SRVR.
				ci, err := world.NewCipherAt(key[:], side.dir, uint64(i))
				if err != nil {
					return nil, false
				}
				plain, err := ci.Open(nil, f.body, f.tag[:])
				if err != nil {
					if side.tag == "s2c" && !ok {
						return nil, false
					}
					note = "decrypt failed"
					out = append(out, msg{T: f.t.Format("15:04:05.000"), Dir: side.tag, Size: len(f.body), Note: note})
					continue
				}
				ok = true
				op, body = binary.LittleEndian.Uint32(plain), plain[4:]
			}
			t := f.t.Format("15:04:05.000")
			var expand func(op uint32, body []byte, note string, depth int)
			expand = func(op uint32, body []byte, note string, depth int) {
				n := name(side.od, op)
				switch {
				case depth < 3 && strings.Contains(n, "COMPRESSED_PACKET") && len(body) >= 12:
					plain, err := inf.inflate(body)
					if err == nil && len(plain) >= 4 {
						expand(binary.LittleEndian.Uint32(plain), plain[4:], strings.TrimPrefix(note+" inflated", " "), depth+1)
						return
					}
					note = fmt.Sprintf("inflate: %v", err)
				case depth < 3 && strings.Contains(n, "MULTIPLE_PACKETS"):
					// u16 size, u32 opcode, payload, repeated (session.handleMultiple)
					b := body
					for len(b) >= 6 {
						sz := int(binary.LittleEndian.Uint16(b))
						if len(b) < 6+sz {
							break
						}
						expand(binary.LittleEndian.Uint32(b[2:]), b[6:6+sz], strings.TrimPrefix(note+" multiple", " "), depth+1)
						b = b[6+sz:]
					}
					if len(b) == 0 {
						return
					}
					note += fmt.Sprintf(" (%d bytes left unsplit)", len(b))
				}
				out = append(out, msg{T: t, Dir: side.tag, Opcode: fmt.Sprintf("%#08x", op), Name: n, Size: len(body), Body: hex.EncodeToString(body), Note: note})
			}
			expand(op, body, note, 0)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, ok
}

func write(c *conn, out []msg, dir string) error {
	path := filepath.Join(dir, fmt.Sprintf("conn-%d.jsonl", c.clientPort))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	fails := 0
	for _, m := range out {
		if m.Note == "decrypt failed" {
			fails++
		}
		_ = enc.Encode(m)
	}
	log.Printf("port %d (%s): %d messages decrypted, %d failed -> %s", c.clientPort, c.server, len(out)-fails, fails, path)
	return nil
}

// inflater is one connection direction's persistent raw-deflate stream
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
