package bnet

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

// TestServiceHashes recomputes every service hash from its descriptor_name.
// The literals are the immediates found in the client (addresses in
// services.go), so FNV-1a over the descriptor name is confirmed against the
// binary, not assumed.
func TestServiceHashes(t *testing.T) {
	tests := []struct {
		svc  Service
		want uint32
		at   string
	}{
		{ConnectionService, 0x65446991, "0x10238f4bc"},
		{AuthenticationService, 0xc02f8216, "0x1023928c4"},
		{AuthenticationListener, 0x9da8116b, "0x10238e6a0"},
		{GameUtilities, 0x5dbb51c2, "0x102394498"},
		{AccountService, 0x22dc2464, "0x10238fcd8"},
	}
	for _, tc := range tests {
		if tc.svc.Hash != tc.want {
			t.Errorf("%s: table says %#x, binary immediate at %s is %#x", tc.svc.Name, tc.svc.Hash, tc.at, tc.want)
		}
		if got := ServiceHash(tc.svc.Name); got != tc.want {
			t.Errorf("%s: FNV-1a = %#x, want %#x (binary %s)", tc.svc.Name, got, tc.want, tc.at)
		}
	}
	if len(Services) != len(tests) {
		t.Errorf("Services has %d entries, test covers %d", len(Services), len(tests))
	}
}

func TestRouter(t *testing.T) {
	r := NewRouter()
	if got := r.ServiceID(GameUtilities.Hash); got != 0 {
		t.Fatalf("bindless id = %d, want 0", got)
	}
	if h, ok := r.Hash(Header{ServiceHash: 7}); !ok || h != 7 {
		t.Fatalf("explicit hash: %v %v", h, ok)
	}
	if _, ok := r.Hash(Header{ServiceID: 3}); ok {
		t.Fatal("unbound id must not resolve")
	}
	r.Set(AuthenticationListener.Hash, 3)
	if h, ok := r.Hash(Header{ServiceID: 3}); !ok || h != AuthenticationListener.Hash {
		t.Fatalf("bound id: %#x %v", h, ok)
	}
	if got := r.ServiceID(AuthenticationListener.Hash); got != 3 {
		t.Fatalf("ServiceID = %d", got)
	}
}

func TestConnectRequestRoundTrip(t *testing.T) {
	req := DefaultConnectRequest()
	got, err := DecodeConnectRequest(req.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("got %+v want %+v", got, req)
	}
	if req.ClientSDK.SDKName != "BGS C++ SDK" ||
		req.ClientSDK.SDKVersion != (SemanticVersion{11, 5, 2, "11.5.2"}) ||
		req.ClientSDK.ProtocolVersion != (SemanticVersion{3, 33, 19, "3.33.19-2ddb4a936f"}) ||
		!req.UseBindlessRPC || req.MeteringLevel != 1 {
		t.Fatalf("default request drifted from 0x1022c0ae4: %+v", req)
	}
}

func TestConnectResponseRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		r    ConnectResponse
	}{
		{"minimal", ConnectResponse{ServerID: ProcessID{1, 2}}},
		{"full", ConnectResponse{
			ServerID: ProcessID{7, 9}, ClientID: &ProcessID{3, 4}, BindResult: 1,
			BindResponse: &BindResponse{ImportedServiceIDs: []uint32{5, 6}},
			ServerTime:   1700000000, UseBindlessRPC: true, CIID: "c-1", ConnectedRegion: 2,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeConnectResponse(EncodeConnectResponse(tc.r))
			if err != nil || !reflect.DeepEqual(got, tc.r) {
				t.Fatalf("got %+v err %v want %+v", got, err, tc.r)
			}
		})
	}
	if _, err := DecodeConnectResponse(nil); !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("empty response: %v", err)
	}
}

func TestAttributeRoundTrip(t *testing.T) {
	req := ProcessTaskRequest{Attributes: []Attribute{
		{Name: "none"},
		{Name: "b", Value: Variant{Kind: VariantBool, Bool: true}},
		{Name: "i", Value: Variant{Kind: VariantInt, Int: -5}},
		{Name: "f", Value: Variant{Kind: VariantFloat, Float: 2.5}},
		{Name: "s", Value: Variant{Kind: VariantString, String: "x"}},
		{Name: "blob", Value: Variant{Kind: VariantBlob, Blob: []byte{0, 1, 2}}},
		{Name: "u", Value: Variant{Kind: VariantUint, Uint: 1 << 40}},
	}, Payload: []Attribute{{Name: "p"}}}
	got, err := DecodeProcessTaskRequest(req.Encode())
	if err != nil {
		t.Fatal(err)
	}
	// Blob nil vs empty does not matter for the compared members.
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("got %+v\nwant %+v", got, req)
	}
	resp := ProcessTaskResponse{Results: req.Attributes}
	back, err := DecodeProcessTaskResponse(resp.Encode())
	if err != nil || !reflect.DeepEqual(back, resp) {
		t.Fatalf("response: %+v %v", back, err)
	}
}

func TestLookupTreatsDuplicatesAsAbsent(t *testing.T) {
	attrs := []Attribute{{Name: "a"}, {Name: "b"}, {Name: "a"}}
	if _, ok := Lookup(attrs, "a"); ok {
		t.Fatal("duplicate name resolved (client 0x10139ee2c returns none)")
	}
	if _, ok := Lookup(attrs, "b"); !ok {
		t.Fatal("unique name not found")
	}
	if _, ok := Lookup(attrs, "c"); ok {
		t.Fatal("missing name found")
	}
}

func TestLogonMessagesRoundTrip(t *testing.T) {
	req := LogonRequest{TitleID: TitleIDWoW, Platform: PlatformMacARM, Locale: "enUS", ApplicationVersion: 70707,
		Options: LogonOptions{AuthToken: []byte("tok"), UserAgent: "ua", DeviceID: "dev"}}
	got, err := DecodeLogonRequest(req.Encode())
	if err != nil || !reflect.DeepEqual(got, req) {
		t.Fatalf("request %+v %v", got, err)
	}
	n := LogonComplete{Record: LogonRecord{
		AccountID: 99, GameAccounts: []GameAccountHandle{{ID: 1, TitleID: TitleIDWoW, Region: 3}},
		BattleTag: "A#1", GeoIPCountry: "AU", SessionKey: bytes.Repeat([]byte{9}, 64), LoginTicket: "TICKET",
	}}
	back, err := DecodeLogonComplete(n.Encode())
	if err != nil || !reflect.DeepEqual(back, n) {
		t.Fatalf("complete %+v %v", back, err)
	}
}

// pair returns a Client connected to a Peer over an in-memory pipe.
func pair(t *testing.T) (*Client, *Peer) {
	t.Helper()
	a, b := net.Pipe()
	p := NewPeer(b)
	go p.Serve()
	c := NewClient(a)
	t.Cleanup(func() { c.Close(); b.Close() })
	return c, p
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestConnectBootstrap(t *testing.T) {
	c, p := pair(t)
	var seen Header
	var sawReq ConnectRequest
	p.Handle(ConnectionService, MethodConnect, func(h Header, body []byte) ([]byte, uint32) {
		seen = h
		sawReq, _ = DecodeConnectRequest(body)
		return EncodeConnectResponse(ConnectResponse{ServerID: ProcessID{1, 1}, UseBindlessRPC: true, CIID: "ciid-from-server"}), 0
	})
	resp, err := c.Connect(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if resp.CIID != "ciid-from-server" {
		t.Fatalf("resp %+v", resp)
	}
	if seen.ServiceHash != 0x65446991 || seen.MethodID != 1 || seen.ServiceID != 0 {
		t.Fatalf("connect header %+v", seen)
	}
	if !reflect.DeepEqual(sawReq, DefaultConnectRequest()) {
		t.Fatalf("server saw %+v", sawReq)
	}
}

// TestConnectWithoutBindlessFlag: live Battle.net omits use_bindless_rpc
// from ConnectResponse and still serves bindless requests (EVIDENCE E32).
func TestConnectWithoutBindlessFlag(t *testing.T) {
	c, p := pair(t)
	p.Handle(ConnectionService, MethodConnect, func(Header, []byte) ([]byte, uint32) {
		return EncodeConnectResponse(ConnectResponse{ServerID: ProcessID{1, 1}, CIID: "x"}), 0
	})
	if _, err := c.Connect(ctx(t)); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestBindFillsRouter(t *testing.T) {
	c, p := pair(t)
	p.Handle(ConnectionService, MethodBind, func(h Header, body []byte) ([]byte, uint32) {
		var w pbWriter
		w.varint(1, 4)
		w.varint(1, 5)
		return w.b, 0
	})
	if err := c.Bind(ctx(t), GameUtilities, AuthenticationService); err != nil {
		t.Fatal(err)
	}
	if c.Router().ServiceID(GameUtilities.Hash) != 4 || c.Router().ServiceID(AuthenticationService.Hash) != 5 {
		t.Fatal("router not filled in request order")
	}
}

func TestCallStatusAndUnknownService(t *testing.T) {
	c, p := pair(t)
	p.Handle(GameUtilities, MethodProcessTask, func(Header, []byte) ([]byte, uint32) { return nil, 0x3ee })
	_, err := c.ProcessTask(ctx(t), ProcessTaskRequest{})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 0x3ee || se.ServiceHash != GameUtilities.Hash {
		t.Fatalf("got %v", err)
	}
	// No handler for Echo: the peer answers with its "no such service" status.
	_, err = c.Call(ctx(t), ConnectionService, MethodEcho, nil)
	if !errors.As(err, &se) || se.Status != 0xbd3 {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentCallsMatchByToken(t *testing.T) {
	c, p := pair(t)
	p.Handle(GameUtilities, MethodProcessTask, func(h Header, body []byte) ([]byte, uint32) {
		req, _ := DecodeProcessTaskRequest(body)
		return ProcessTaskResponse{Results: req.Attributes}.Encode(), 0
	})
	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			name := string(rune('a' + i))
			resp, err := c.ProcessTask(ctx(t), ProcessTaskRequest{Attributes: []Attribute{{Name: name}}})
			if err == nil && (len(resp.Results) != 1 || resp.Results[0].Name != name) {
				err = errors.New("response matched to the wrong request")
			}
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientIDOnlyForClientIDServices(t *testing.T) {
	c, p := pair(t)
	got := map[uint32]string{}
	rec := func(h Header, _ []byte) ([]byte, uint32) { got[h.ServiceHash] = h.CIID; return nil, 0 }
	p.Handle(ConnectionService, MethodEcho, rec)
	p.Handle(GameUtilities, MethodProcessTask, rec)
	c.SetCIID("abc")
	c.Call(ctx(t), ConnectionService, MethodEcho, nil)
	c.ProcessTask(ctx(t), ProcessTaskRequest{})
	if got[ConnectionService.Hash] != "" || got[GameUtilities.Hash] != "abc" {
		t.Fatalf("ciid by service: %v", got)
	}
}

func TestLogonWaitsForCallback(t *testing.T) {
	c, p := pair(t)
	key := bytes.Repeat([]byte{0x42}, 64)
	p.Handle(AuthenticationService, MethodLogon, func(h Header, body []byte) ([]byte, uint32) {
		req, err := DecodeLogonRequest(body)
		if err != nil || string(req.Options.AuthToken) != "ap-token" {
			return nil, 0x7d0
		}
		go p.Notify(AuthenticationListener, MethodOnLogonComplete, LogonComplete{Record: LogonRecord{
			AccountID: 5, SessionKey: key, LoginTicket: "LT",
		}}.Encode())
		return nil, 0
	})
	rec, err := c.Logon(ctx(t), LogonRequest{TitleID: TitleIDWoW, Platform: PlatformMacARM, Options: LogonOptions{AuthToken: []byte("ap-token")}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.SessionKey, key) || rec.LoginTicket != "LT" || rec.AccountID != 5 {
		t.Fatalf("record %+v", rec)
	}
}

func TestLogonErrorCode(t *testing.T) {
	c, p := pair(t)
	p.Handle(AuthenticationService, MethodLogon, func(Header, []byte) ([]byte, uint32) {
		go p.Notify(AuthenticationListener, MethodOnLogonComplete, LogonComplete{ErrorCode: 12}.Encode())
		return nil, 0
	})
	_, err := c.Logon(ctx(t), LogonRequest{})
	var le *LogonError
	if !errors.As(err, &le) || le.Code != 12 {
		t.Fatalf("got %v", err)
	}
}

func TestClientFailsPendingCallsWhenStreamEnds(t *testing.T) {
	a, b := net.Pipe()
	c := NewClient(a)
	go func() { // swallow the request, then hang up
		buf := make([]byte, 64)
		b.Read(buf)
		b.Close()
	}()
	if _, err := c.Call(ctx(t), GameUtilities, MethodProcessTask, nil); err == nil {
		t.Fatal("call must fail when the peer hangs up")
	}
	if c.Err() == nil {
		t.Fatal("Err must report the failure")
	}
}
