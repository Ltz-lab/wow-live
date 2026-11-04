// Package main is bnetrelay: a Battle.net (bgs) relay between the stock
// client and a real bgs server, which decodes and records every message.
//
// The stock client logs in to the portal named in its config and trusts the
// Mac's keychain roots (setup/connect-client.sh sets
// both up for the local fork: portal trinity.actual.battle.net, /etc/hosts to
// 127.0.0.1, the fork's CA trusted). So with the fork's bnetserver stopped,
// this relay takes :1119 with the fork's certificate for that name, and the
// client talks to it. The relay opens its own TLS connection upstream (live
// Battle.net, or the fork on another port) and passes every frame through
// byte for byte, so neither side sees a difference.
//
// It writes, per run, under -out:
//
//	frames.jsonl   every frame: time, connection, direction, service, method,
//	               token, status, and the body in hex; responses are matched
//	               to their request's service and method by token
//	secrets.jsonl  what the world connection's keys come from: the client
//	               secret in Param_ClientInfo (RealmListTicket) and
//	               Param_JoinSecret, Param_ServerAddresses and the join ticket
//	               (RealmJoin), with the command and realm they belong to
//
//	go run ./tools/bnetrelay -upstream us.actual.battle.net:1119 -out /tmp/bnetrelay
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Ltz-lab/wow-live/auth/bnet"
	"github.com/Ltz-lab/wow-live/auth/realm"
)

const certs = "certs" // setup/connect-client.sh makes them; capture-live passes its own

func main() {
	listen := flag.String("listen", "127.0.0.1:1119", "where the client connects (the portal's address)")
	upstream := flag.String("upstream", "us.actual.battle.net:1119", "the real bgs server")
	sni := flag.String("sni", "", "TLS server name upstream (default: the -upstream host)")
	insecure := flag.Bool("insecure", false, "skip upstream certificate checks (the local fork's self-signed chain)")
	cert := flag.String("cert", certs+"/server.chain.pem", "certificate the client sees (valid for the portal name)")
	key := flag.String("key", certs+"/server.key.pem", "its private key")
	out := flag.String("out", "/tmp/bnetrelay", "directory for this run's records")
	flag.Parse()

	pair, err := tls.LoadX509KeyPair(home(*cert), home(*key))
	if err != nil {
		log.Fatalf("bnetrelay: certificate: %v", err)
	}
	host, _, err := net.SplitHostPort(*upstream)
	if err != nil {
		log.Fatalf("bnetrelay: -upstream: %v", err)
	}
	if *sni == "" {
		*sni = host
	}
	dir := filepath.Join(*out, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
	rec, err := newRecorder(dir)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := tls.Listen("tcp", *listen, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		log.Fatalf("bnetrelay: listen: %v (is something else on it? a local bnetserver: tmux kill-session -t tc-bnet)", err)
	}
	log.Printf("bnetrelay: %s -> %s (sni %s); records in %s", *listen, *upstream, *sni, dir)
	up := &tls.Config{ServerName: *sni, MinVersion: tls.VersionTLS12, InsecureSkipVerify: *insecure}
	for id := 1; ; id++ {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go relay(id, c, *upstream, up, rec)
	}
}

func home(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

// relay passes one client connection through to its own upstream connection.
func relay(id int, client net.Conn, upstream string, cfg *tls.Config, rec *recorder) {
	defer client.Close()
	server, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", upstream, cfg)
	if err != nil {
		log.Printf("c%d: upstream: %v", id, err)
		return
	}
	defer server.Close()
	log.Printf("c%d: client %s, upstream %s", id, client.RemoteAddr(), server.RemoteAddr())
	pending := &tokens{m: map[tokenKey]call{}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(id, "c2s", client, server, pending, rec); server.CloseWrite() }()
	go func() { defer wg.Done(); pump(id, "s2c", server, client, pending, rec); client.Close() }()
	wg.Wait()
	log.Printf("c%d: closed", id)
}

// call is what a request was, kept until its response comes back.
type call struct {
	hash, method uint32
	body         []byte
}

// tokenKey: a response travels the other way, so the requester's direction
// keys its token.
type tokenKey struct {
	dir   string
	token uint32
}

type tokens struct {
	mu sync.Mutex
	m  map[tokenKey]call
}

func (t *tokens) put(k tokenKey, c call) { t.mu.Lock(); t.m[k] = c; t.mu.Unlock() }
func (t *tokens) take(k tokenKey) (call, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.m[k]
	delete(t.m, k)
	return c, ok
}

func other(dir string) string {
	if dir == "c2s" {
		return "s2c"
	}
	return "c2s"
}

// pump copies frames from src to dst unchanged, recording each.
func pump(id int, dir string, src io.Reader, dst io.Writer, pending *tokens, rec *recorder) {
	r := bufio.NewReaderSize(src, 64<<10)
	for {
		raw, err := readRaw(r)
		if err != nil {
			if err != io.EOF {
				log.Printf("c%d %s: %v", id, dir, err)
			}
			return
		}
		if _, err := dst.Write(raw); err != nil {
			log.Printf("c%d %s: write: %v", id, dir, err)
			return
		}
		f, _, err := bnet.Decode(raw)
		if err != nil {
			rec.frame(entry{Conn: id, Dir: dir, Error: err.Error(), Raw: hex.EncodeToString(raw)})
			continue
		}
		h := f.Header
		e := entry{Conn: id, Dir: dir, Token: h.Token, Method: h.MethodID, Status: h.Status, Size: len(f.Body), Body: hex.EncodeToString(f.Body)}
		var req call
		if h.ServiceID == 254 || h.IsResponse {
			e.Response = true
			if c, ok := pending.take(tokenKey{other(dir), h.Token}); ok {
				req = c
				e.Method = c.method
				e.Hash = fmt.Sprintf("%#08x", c.hash)
				e.Service = serviceName(c.hash)
			}
		} else {
			e.Hash = fmt.Sprintf("%#08x", h.ServiceHash)
			e.Service = serviceName(h.ServiceHash)
			pending.put(tokenKey{dir, h.Token}, call{hash: h.ServiceHash, method: h.MethodID, body: f.Body})
		}
		rec.frame(e)
		if e.Response {
			rec.secrets(id, req, f.Body)
		}
	}
}

// readRaw reads one whole frame: u16be header length, the header, the body
// the header's size names.
func readRaw(r *bufio.Reader) ([]byte, error) {
	var pre [2]byte
	if _, err := io.ReadFull(r, pre[:]); err != nil {
		return nil, err
	}
	hlen := int(binary.BigEndian.Uint16(pre[:]))
	buf := make([]byte, 2+hlen)
	copy(buf, pre[:])
	if _, err := io.ReadFull(r, buf[2:]); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	size, ok := headerSize(buf)
	if !ok {
		return nil, fmt.Errorf("unreadable header (%d bytes)", hlen)
	}
	if size >= bnet.MaxPayload {
		return nil, fmt.Errorf("body of %d bytes", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return append(buf, body...), nil
}

// headerSize reads Header.size (field 5, varint) straight from the header.
func headerSize(frame []byte) (int, bool) {
	b := frame[2:]
	for i := 0; i < len(b); {
		tag, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return 0, false
		}
		i += n
		field, wire := tag>>3, tag&7
		switch wire {
		case 0:
			v, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return 0, false
			}
			i += n
			if field == 5 {
				return int(v), true
			}
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			l, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return 0, false
			}
			i += n + int(l)
		default:
			return 0, false
		}
	}
	return 0, true // no size field: an empty body
}

var names = func() map[uint32]string {
	m := map[uint32]string{}
	for _, s := range []bnet.Service{bnet.ConnectionService, bnet.AuthenticationService, bnet.AuthenticationListener, bnet.GameUtilities, bnet.AccountService} {
		m[s.Hash] = s.Name
	}
	m[bnet.GameUtilitiesV1Hash] = "bgs.protocol.game_utilities.v1.GameUtilitiesService"
	m[bnet.GameUtilitiesV2Hash] = "bgs.protocol.game_utilities.v2.GameUtilitiesService"
	for _, n := range []string{
		"bnet.protocol.challenge.ChallengeListener",
		"bnet.protocol.friends.FriendsService", "bnet.protocol.friends.FriendsListener",
		"bnet.protocol.presence.PresenceService", "bnet.protocol.presence.PresenceListener",
		"bnet.protocol.resources.Resources",
		"bnet.protocol.account.AccountListener",
		"bnet.protocol.report.ReportService",
		"bnet.protocol.user_manager.UserManagerService", "bnet.protocol.user_manager.UserManagerListener",
		"bnet.protocol.club.v1.membership.ClubMembershipService", "bnet.protocol.club.v1.membership.ClubMembershipListener",
		"bnet.protocol.club.v1.ClubService", "bnet.protocol.club.v1.ClubListener",
		"bnet.protocol.session.SessionService", "bnet.protocol.session.SessionListener",
		"bnet.protocol.notification.NotificationService", "bnet.protocol.notification.NotificationListener",
		"bnet.protocol.whisper.v1.WhisperService", "bnet.protocol.whisper.v1.WhisperListener",
		"bnet.protocol.game_utilities.GameUtilities",
	} {
		m[bnet.ServiceHash(n)] = n
	}
	return m
}()

func serviceName(h uint32) string {
	if n, ok := names[h]; ok {
		return n
	}
	return fmt.Sprintf("service %#08x", h)
}

type entry struct {
	T        string `json:"t"`
	Conn     int    `json:"conn"`
	Dir      string `json:"dir"`
	Service  string `json:"service,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Method   uint32 `json:"method"`
	Token    uint32 `json:"token"`
	Response bool   `json:"response,omitempty"`
	Status   uint32 `json:"status,omitempty"`
	Size     int    `json:"size"`
	Body     string `json:"body,omitempty"`
	Raw      string `json:"raw,omitempty"`
	Error    string `json:"error,omitempty"`
}

type recorder struct {
	mu            sync.Mutex
	frames, found *json.Encoder
	start         time.Time
}

func newRecorder(dir string) (*recorder, error) {
	f, err := os.OpenFile(filepath.Join(dir, "frames.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s, err := os.OpenFile(filepath.Join(dir, "secrets.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &recorder{frames: json.NewEncoder(f), found: json.NewEncoder(s), start: time.Now()}, nil
}

func (r *recorder) frame(e entry) {
	e.T = fmt.Sprintf("%.3f", time.Since(r.start).Seconds())
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.frames.Encode(e)
	what := e.Service
	if what == "" {
		what = "?"
	}
	kind := "req"
	if e.Response {
		kind = "resp"
	}
	log.Printf("c%d %s %-4s %s/%d token %d status %#x, %d B", e.Conn, e.Dir, kind, short(what), e.Method, e.Token, e.Status, e.Size)
}

func short(n string) string {
	if i := strings.LastIndex(n, "."); i >= 0 {
		return n[i+1:]
	}
	return n
}

// secrets pulls out of a ProcessTask round trip what the world keys need.
func (r *recorder) secrets(conn int, req call, resp []byte) {
	if req.hash != bnet.GameUtilities.Hash && req.hash != bnet.GameUtilitiesV2Hash {
		return
	}
	q, err := bnet.DecodeProcessTaskRequest(req.body)
	if err != nil {
		return
	}
	cmd := ""
	for _, a := range q.Attributes {
		if strings.HasPrefix(a.Name, "Command_") {
			cmd = a.Name
		}
	}
	rec := map[string]any{"t": fmt.Sprintf("%.3f", time.Since(r.start).Seconds()), "conn": conn, "command": cmd}
	if a, ok := bnet.Lookup(q.Attributes, realm.ParamClientInfo); ok {
		rec["client_info"] = blobText(a.Value)
	}
	if a, ok := bnet.Lookup(q.Attributes, realm.ParamRealmAddress); ok {
		rec["realm_address"] = fmt.Sprintf("%#x", a.Value.Uint)
	}
	p, err := bnet.DecodeProcessTaskResponse(resp)
	if err == nil {
		if a, ok := bnet.Lookup(p.Results, realm.ParamJoinSecret); ok {
			rec["join_secret"] = hex.EncodeToString(a.Value.Blob)
		}
		if a, ok := bnet.Lookup(p.Results, realm.ParamRealmJoinTicket); ok {
			rec["join_ticket"] = hex.EncodeToString(a.Value.Blob)
		}
		if a, ok := bnet.Lookup(p.Results, realm.ParamServerAddresses); ok {
			if addrs, err := realm.DecodeServerAddresses(a.Value.Blob); err == nil {
				rec["server_addresses"] = addrs
			}
		}
	}
	if len(rec) == 3 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.found.Encode(rec)
	log.Printf("c%d secrets: %s (%d fields)", conn, cmd, len(rec)-3)
}

func blobText(v bnet.Variant) string {
	if v.Kind == bnet.VariantBlob {
		return strings.TrimRight(string(v.Blob), "\x00")
	}
	return v.String
}
