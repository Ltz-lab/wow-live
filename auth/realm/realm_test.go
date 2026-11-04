package realm

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Ltz-lab/wow-live/auth/bnet"
	"github.com/Ltz-lab/wow-live/world"
)

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

// fakeRealmd plays the Battle.net side of both realm commands over a real
// bgs frame stream.
type fakeRealmd struct {
	t          *testing.T
	ticket     []byte
	secret     []byte
	addrs      []Address
	sawSession []byte
	sawAddr    uint64
	sawSub     string
	mutate     func(*bnet.ProcessTaskResponse) // lets a test break the reply
}

func newFake(t *testing.T) *fakeRealmd {
	return &fakeRealmd{
		t:      t,
		ticket: []byte("realm-list-ticket-bytes"),
		secret: bytes.Repeat([]byte{0x5a}, JoinSecretLen),
		addrs:  []Address{{Family: 1, IP: "10.0.0.7", Port: 8085}, {Family: 1, IP: "10.0.0.8", Port: 8086}, {Family: 2, IP: "::1", Port: 8085}},
	}
}

func (f *fakeRealmd) serve(h bnet.Header, body []byte) ([]byte, uint32) {
	req, err := bnet.DecodeProcessTaskRequest(body)
	if err != nil || len(req.Attributes) == 0 {
		return nil, 0x7d1
	}
	var resp bnet.ProcessTaskResponse
	switch strings.TrimSuffix(req.Attributes[0].Name, nameSuffix) {
	case CmdRealmListTicket:
		id, _ := bnet.Lookup(req.Attributes, ParamIdentity)
		ci, _ := bnet.Lookup(req.Attributes, ParamClientInfo)
		if !bytes.HasPrefix(id.Value.Blob, []byte(TypeIdentity+":{")) || id.Value.Blob[len(id.Value.Blob)-1] != 0 ||
			!bytes.HasPrefix(ci.Value.Blob, []byte(TypeClientInfo+":{")) || ci.Value.Blob[len(ci.Value.Blob)-1] != 0 {
			f.t.Errorf("ticket request blobs are not Type:json NUL: %q %q", id.Value.Blob, ci.Value.Blob)
			return nil, 0x7d2
		}
		var who Identity
		if err := json.Unmarshal(id.Value.Blob[len(TypeIdentity)+1:len(id.Value.Blob)-1], &who); err != nil || who.GameAccountID != 77 {
			f.t.Errorf("identity %+v %v", who, err)
		}
		resp.Results = []bnet.Attribute{{Name: ParamRealmListTicket, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: f.ticket}}}
	case CmdRealmJoin:
		cmd := req.Attributes[0]
		f.sawSub = cmd.Value.String
		if cmd.Value.Kind != bnet.VariantString {
			f.t.Errorf("join command value kind %d, want string", cmd.Value.Kind)
		}
		ra, _ := bnet.Lookup(req.Attributes, ParamRealmAddress)
		if ra.Value.Kind != bnet.VariantUint {
			f.t.Errorf("realm address kind %d, want uint", ra.Value.Kind)
		}
		f.sawAddr = ra.Value.Uint
		tk, _ := bnet.Lookup(req.Attributes, ParamRealmListTicket)
		if !bytes.Equal(tk.Value.Blob, f.ticket) {
			f.t.Errorf("join carried ticket %q", tk.Value.Blob)
		}
		sk, _ := bnet.Lookup(req.Attributes, ParamBnetSessionKey)
		f.sawSession = sk.Value.Blob
		blob, _ := EncodeServerAddresses(f.addrs)
		resp.Results = []bnet.Attribute{
			{Name: ParamRealmJoinTicket, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: []byte("JOIN-TICKET")}},
			{Name: ParamServerAddresses, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: blob}},
			{Name: ParamJoinSecret, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: f.secret}},
		}
	default:
		return nil, 0x7d3
	}
	if f.mutate != nil {
		f.mutate(&resp)
	}
	return resp.Encode(), 0
}

func connect(t *testing.T, f *fakeRealmd) *bnet.Client {
	a, b := net.Pipe()
	p := bnet.NewPeer(b)
	p.Handle(bnet.GameUtilities, bnet.MethodProcessTask, f.serve)
	go p.Serve()
	c := bnet.NewClient(a)
	t.Cleanup(func() { c.Close(); b.Close() })
	return c
}

var clientInfo = json.RawMessage(`{"platform":"Mac","textLocale":"enUS","version":{"versionBuild":70707}}`)

func TestTicketThenJoinRoundTrip(t *testing.T) {
	f := newFake(t)
	c := connect(t, f)

	ticket, err := RealmListTicket(ctx(t), c, TicketRequest{Identity: Identity{GameAccountID: 77, GameAccountRegion: 2}, ClientInfoJSON: clientInfo})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ticket, f.ticket) {
		t.Fatalf("ticket %q", ticket)
	}

	session := bytes.Repeat([]byte{0x33}, 64)
	res, err := RealmJoin(ctx(t), c, JoinRequest{SubRegion: "wow-forever", RealmAddress: 0x1234abcd, Ticket: ticket, SessionKey: session})
	if err != nil {
		t.Fatal(err)
	}
	if f.sawAddr != 0x1234abcd || f.sawSub != "wow-forever" || !bytes.Equal(f.sawSession, session) {
		t.Fatalf("server saw addr=%#x sub=%q session=%x", f.sawAddr, f.sawSub, f.sawSession)
	}
	if string(res.JoinTicket) != "JOIN-TICKET" || !bytes.Equal(res.JoinSecret[:], f.secret) {
		t.Fatalf("result %+v", res)
	}
	if !reflect.DeepEqual(res.Addresses, f.addrs) {
		t.Fatalf("addresses %+v", res.Addresses)
	}

	// The result drops into world.Config.
	half := bytes.Repeat([]byte{0xc3}, 32)
	wa, err := res.WorldAuth(half)
	if err != nil {
		t.Fatal(err)
	}
	var cfg world.Config
	wa.Apply(&cfg)
	if len(cfg.KeyData) != world.KeyDataLen || !bytes.Equal(cfg.KeyData[:32], f.secret) || !bytes.Equal(cfg.KeyData[32:], half) {
		t.Fatalf("KeyData %x", cfg.KeyData)
	}
	if string(cfg.Ticket) != "JOIN-TICKET" {
		t.Fatalf("Ticket %q", cfg.Ticket)
	}
}

func TestKeyDataNeedsTheSecondHalf(t *testing.T) {
	var r JoinResult
	if _, err := r.KeyData(nil); !errors.Is(err, ErrKeyHalfRequired) {
		t.Fatalf("got %v", err)
	}
	if _, err := r.KeyData(make([]byte, 31)); err == nil {
		t.Fatal("31-byte half accepted")
	}
	if _, err := r.WorldAuth(nil); !errors.Is(err, ErrKeyHalfRequired) {
		t.Fatalf("WorldAuth: %v", err)
	}
}

func TestRequestValidation(t *testing.T) {
	c := connect(t, newFake(t))
	if _, err := RealmListTicket(ctx(t), c, TicketRequest{}); err == nil {
		t.Fatal("empty ClientInfoJSON accepted")
	}
}

func TestResponseFailures(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*bnet.ProcessTaskResponse)
		want error
	}{
		{"no join ticket", func(r *bnet.ProcessTaskResponse) { r.Results = r.Results[1:] }, ErrNoJoinTicket},
		{"join ticket not a blob", func(r *bnet.ProcessTaskResponse) {
			r.Results[0].Value = bnet.Variant{Kind: bnet.VariantString, String: "x"}
		}, ErrNoJoinTicket},
		{"no addresses", func(r *bnet.ProcessTaskResponse) { r.Results = append(r.Results[:1], r.Results[2:]...) }, ErrNoAddresses},
		{"addresses garbage", func(r *bnet.ProcessTaskResponse) { r.Results[1].Value.Blob = []byte{1, 2, 3, 4, 5} }, ErrNoAddresses},
		{"secret too short", func(r *bnet.ProcessTaskResponse) { r.Results[2].Value.Blob = make([]byte, 31) }, ErrBadJoinSecret},
		{"secret too long", func(r *bnet.ProcessTaskResponse) { r.Results[2].Value.Blob = make([]byte, 33) }, ErrBadJoinSecret},
		{"no secret", func(r *bnet.ProcessTaskResponse) { r.Results = r.Results[:2] }, ErrBadJoinSecret},
		{"duplicate join ticket reads as absent", func(r *bnet.ProcessTaskResponse) { r.Results = append(r.Results, r.Results[0]) }, ErrNoJoinTicket},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.mutate = tc.mut
			c := connect(t, f)
			_, err := RealmJoin(ctx(t), c, JoinRequest{Ticket: f.ticket, SessionKey: make([]byte, 64)})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTicketResponseWithoutTicket(t *testing.T) {
	f := newFake(t)
	f.mutate = func(r *bnet.ProcessTaskResponse) { r.Results = nil }
	c := connect(t, f)
	_, err := RealmListTicket(ctx(t), c, TicketRequest{Identity: Identity{GameAccountID: 77}, ClientInfoJSON: clientInfo})
	if !errors.Is(err, ErrNoTicket) {
		t.Fatalf("got %v", err)
	}
}

func TestStatusErrorPropagates(t *testing.T) {
	a, b := net.Pipe()
	p := bnet.NewPeer(b)
	p.Handle(bnet.GameUtilities, bnet.MethodProcessTask, func(bnet.Header, []byte) ([]byte, uint32) { return nil, 0xbc5 })
	go p.Serve()
	c := bnet.NewClient(a)
	defer c.Close()
	_, err := RealmJoin(ctx(t), c, JoinRequest{})
	var se *bnet.StatusError
	if !errors.As(err, &se) || se.Status != 0xbc5 {
		t.Fatalf("got %v", err)
	}
}

func TestCommandSuffix(t *testing.T) {
	var saw string
	a, b := net.Pipe()
	p := bnet.NewPeer(b)
	p.Handle(bnet.GameUtilities, bnet.MethodProcessTask, func(_ bnet.Header, body []byte) ([]byte, uint32) {
		req, _ := bnet.DecodeProcessTaskRequest(body)
		saw = req.Attributes[0].Name
		return nil, 0xbc5
	})
	go p.Serve()
	c := bnet.NewClient(a)
	defer c.Close()
	RealmJoin(ctx(t), c, JoinRequest{CommandSuffix: "eu"})
	if saw != "Command_RealmJoinRequest_v1_eu" {
		t.Fatalf("command %q", saw)
	}
}

// blob builds a Param_ServerAddresses blob from raw parts so a test can break
// one rule at a time.
func blob(declared uint32, plain []byte) []byte {
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write(plain)
	zw.Close()
	return append(binary.LittleEndian.AppendUint32(nil, declared), zb.Bytes()...)
}

func TestDecodeServerAddressesRules(t *testing.T) {
	good := append([]byte(TypeServerAddresses+`:{"families":[{"family":1,"addresses":[{"ip":"1.2.3.4","port":9}]}]}`), 0)
	if got, err := DecodeServerAddresses(blob(uint32(len(good)), good)); err != nil || !reflect.DeepEqual(got, []Address{{1, "1.2.3.4", 9}}) {
		t.Fatalf("good blob: %+v %v", got, err)
	}
	tests := []struct {
		name string
		blob []byte
	}{
		{"too short", []byte{1, 2, 3}},
		{"declared length below 3", blob(2, []byte{0, 0})},
		{"declared above the 0x40000 cap", blob(MaxServerAddresses+1, good)},
		{"declared shorter than inflated", blob(uint32(len(good)-1), good)},
		{"declared longer than inflated", blob(uint32(len(good)+1), good)},
		{"not NUL terminated", blob(uint32(len(good)-1), good[:len(good)-1])},
		{"no colon", blob(4, []byte{'a', 'b', 'c', 0})},
		{"wrong type name", blob(uint32(len(good)), []byte(strings.Replace(string(good), "IPAddresses", "IPAddressesX", 1) + "\x00")[:len(good)])},
		{"not zlib", append(binary.LittleEndian.AppendUint32(nil, 10), bytes.Repeat([]byte{0xff}, 10)...)},
		{"bad json", blob(uint32(len(TypeServerAddresses)+4), append([]byte(TypeServerAddresses+":{{x"), 0)[:len(TypeServerAddresses)+4])},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeServerAddresses(tc.blob); !errors.Is(err, ErrBadBlob) {
				t.Fatalf("got %v, want ErrBadBlob", err)
			}
		})
	}
}

func TestJSONBlobFraming(t *testing.T) {
	b, err := JSONBlob(TypeIdentity, Identity{GameAccountID: 5, GameAccountRegion: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := TypeIdentity + `:{"gameAccountID":5,"gameAccountRegion":3}` + "\x00"
	if string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
}

// TestBgsLogonToWorldConfig walks the whole bgs side over one connection:
// Connect, Logon (the AP token as the auth token, the session key coming back
// in OnLogonComplete), the ticket request, the join, and the hand-off into
// world.Config.
func TestBgsLogonToWorldConfig(t *testing.T) {
	f := newFake(t)
	a, b := net.Pipe()
	p := bnet.NewPeer(b)
	sessionKey := bytes.Repeat([]byte{0x77}, 64)
	p.Handle(bnet.ConnectionService, bnet.MethodConnect, func(bnet.Header, []byte) ([]byte, uint32) {
		return bnet.EncodeConnectResponse(bnet.ConnectResponse{ServerID: bnet.ProcessID{Label: 1, Epoch: 1}, UseBindlessRPC: true, CIID: "ciid-1"}), 0
	})
	p.Handle(bnet.AuthenticationService, bnet.MethodLogon, func(h bnet.Header, body []byte) ([]byte, uint32) {
		req, _ := bnet.DecodeLogonRequest(body)
		if h.CIID != "ciid-1" || string(req.Options.AuthToken) != "AP-TOKEN" {
			return nil, 0x7d0
		}
		go p.Notify(bnet.AuthenticationListener, bnet.MethodOnLogonComplete, bnet.LogonComplete{Record: bnet.LogonRecord{
			AccountID: 1, SessionKey: sessionKey, GameAccounts: []bnet.GameAccountHandle{{ID: 77, TitleID: bnet.TitleIDWoW, Region: 2}},
		}}.Encode())
		return nil, 0
	})
	p.Handle(bnet.GameUtilities, bnet.MethodProcessTask, func(h bnet.Header, body []byte) ([]byte, uint32) {
		if h.CIID != "ciid-1" {
			t.Errorf("ProcessTask without the client instance id: %+v", h)
		}
		return f.serve(h, body)
	})
	go p.Serve()
	c := bnet.NewClient(a)
	t.Cleanup(func() { c.Close(); b.Close() })

	if _, err := c.Connect(ctx(t)); err != nil {
		t.Fatal(err)
	}
	rec, err := c.Logon(ctx(t), bnet.LogonRequest{TitleID: bnet.TitleIDWoW, Platform: bnet.PlatformMacARM, Options: bnet.LogonOptions{AuthToken: []byte("AP-TOKEN")}})
	if err != nil {
		t.Fatal(err)
	}
	ga := rec.GameAccounts[0]
	ticket, err := RealmListTicket(ctx(t), c, TicketRequest{Identity: Identity{GameAccountID: ga.ID, GameAccountRegion: ga.Region}, ClientInfoJSON: clientInfo})
	if err != nil {
		t.Fatal(err)
	}
	res, err := RealmJoin(ctx(t), c, JoinRequest{SubRegion: "x", RealmAddress: 1, Ticket: ticket, SessionKey: rec.SessionKey})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.sawSession, sessionKey) {
		t.Fatal("the logon session key did not reach Param_BnetSessionKey")
	}
	wa, err := res.WorldAuth(sessionKey[32:])
	if err != nil {
		t.Fatal(err)
	}
	cfg := world.Config{}
	wa.Apply(&cfg)
	if len(cfg.KeyData) != world.KeyDataLen || len(cfg.Ticket) == 0 {
		t.Fatalf("world.Config auth material %d/%d", len(cfg.KeyData), len(cfg.Ticket))
	}
}
