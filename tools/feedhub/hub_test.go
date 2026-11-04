package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestStamp(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"player","guid":"x"}`: `{"v":1,"seq":7,"type":"player","guid":"x"}`,
		`{}`:                           `{"v":1,"seq":7}`,
	} {
		got := string(stamp(7, []byte(in)))
		if got != want {
			t.Errorf("stamp(%s) = %s, want %s", in, got, want)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("stamp(%s) is not JSON", in)
		}
	}
}

type msg struct {
	V    int    `json:"v"`
	Seq  uint64 `json:"seq"`
	Type string `json:"type"`
	Unit string `json:"unit"`
	N    int    `json:"n"`
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func read(t *testing.T, c *websocket.Conn) msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m msg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

func server(t *testing.T, h *hub, token string) (*httptest.Server, string) {
	srv := httptest.NewServer(handler(h, []string{"localhost:*"}, token))
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// A late client gets the latest state per type and unit, oldest first, never
// old events, then the live marker and the stream with no gaps.
func TestCatchUpThenStream(t *testing.T) {
	h := newHub(64)
	for _, l := range []string{
		`{"type":"player","guid":"a"}`,
		`{"type":"auras","unit":"player","n":1}`,
		`{"type":"event","sub":"SPELL_DAMAGE"}`,
		`{"type":"auras","unit":"target","n":2}`,
		`{"type":"auras","unit":"player","n":3}`,
		`not json`,
	} {
		h.publish([]byte(l))
	}
	_, url := server(t, h, "")
	c := dial(t, url+"/feed")
	defer c.CloseNow()

	want := []msg{
		{1, 1, "player", "", 0},
		{1, 4, "auras", "target", 2},
		{1, 5, "auras", "player", 3},
		{1, 5, "hub", "", 0},
	}
	for _, w := range want {
		if m := read(t, c); m != w {
			t.Fatalf("got %+v, want %+v", m, w)
		}
	}
	h.publish([]byte(`{"type":"event","n":9}`))
	if m := read(t, c); m.Seq != 6 || m.Type != "event" || m.N != 9 {
		t.Fatalf("stream: got %+v", m)
	}
}

func TestTypesFilter(t *testing.T) {
	h := newHub(64)
	h.publish([]byte(`{"type":"player"}`))
	h.publish([]byte(`{"type":"next","n":1}`))
	_, url := server(t, h, "")
	c := dial(t, url+"/feed?types=next,event")
	defer c.CloseNow()
	if m := read(t, c); m.Type != "next" {
		t.Fatalf("got %+v", m)
	}
	if m := read(t, c); m.Type != "hub" {
		t.Fatalf("got %+v", m)
	}
	h.publish([]byte(`{"type":"cooldowns"}`))
	h.publish([]byte(`{"type":"event","n":2}`))
	if m := read(t, c); m.Type != "event" || m.N != 2 {
		t.Fatalf("got %+v", m)
	}
}

// A client that stops reading is dropped with 1008; the feed is not held up.
func TestSlowClientDropped(t *testing.T) {
	h := newHub(4)
	_, url := server(t, h, "")
	c := dial(t, url+"/feed")
	defer c.CloseNow()
	c.SetReadLimit(-1)
	read(t, c) // the hub line
	big := `{"type":"event","pad":"` + strings.Repeat("x", 256<<10) + `"}`
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			h.publish([]byte(big))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publish blocked on a slow client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("closed with %v, want 1008", err)
			}
			return
		}
	}
}

func TestFeedEndClosesClients(t *testing.T) {
	h := newHub(64)
	_, url := server(t, h, "")
	c := dial(t, url+"/feed")
	defer c.CloseNow()
	read(t, c)
	h.closeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("got %v, want 1000", err)
	}
}

func TestOriginAndToken(t *testing.T) {
	h := newHub(64)
	srv, url := server(t, h, "s3cret")
	ctx := context.Background()

	if _, _, err := websocket.Dial(ctx, url+"/feed", nil); err == nil {
		t.Error("no token: connected")
	}
	evil := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}
	if _, _, err := websocket.Dial(ctx, url+"/feed?token=s3cret", evil); err == nil {
		t.Error("foreign origin: connected")
	}
	local := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://localhost:5173"}}}
	c, _, err := websocket.Dial(ctx, url+"/feed?token=s3cret", local)
	if err != nil {
		t.Fatalf("local dev origin: %v", err)
	}
	c.CloseNow()

	r, err := http.Get(srv.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("/state without token: %d", r.StatusCode)
	}
}

func TestStateEndpoint(t *testing.T) {
	h := newHub(64)
	h.publish([]byte(`{"type":"player"}`))
	h.publish([]byte(`{"type":"event"}`))
	h.publish([]byte(`{"type":"power","n":4}`))
	srv, _ := server(t, h, "")
	r, err := http.Get(srv.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	want := "{\"v\":1,\"seq\":1,\"type\":\"player\"}\n{\"v\":1,\"seq\":3,\"type\":\"power\",\"n\":4}\n"
	if string(b) != want {
		t.Fatalf("got %q", b)
	}
}

// Lines go to stdout unchanged, one write each, non-JSON included.
func TestPumpPassThrough(t *testing.T) {
	in := "{\"type\":\"player\"}\nplain text\n{\"type\":\"next\"}\n"
	var out bytes.Buffer
	h := newHub(64)
	if err := pump(strings.NewReader(in), &out, h); err != nil {
		t.Fatal(err)
	}
	if out.String() != in {
		t.Fatalf("stdout %q", out.String())
	}
	if h.seq != 2 {
		t.Fatalf("published %d lines, want 2", h.seq)
	}
}

// A new player line (a relog) drops the last character's state.
func TestPlayerResetsState(t *testing.T) {
	h := newHub(64)
	for _, l := range []string{
		`{"type":"player","guid":"a"}`,
		`{"type":"auras","unit":"player","n":1}`,
		`{"type":"cooldowns","n":2}`,
		`{"type":"player","guid":"b"}`,
		`{"type":"power","n":3}`,
	} {
		h.publish([]byte(l))
	}
	var got []string
	for _, s := range h.snapshot() {
		got = append(got, s.typ)
	}
	if strings.Join(got, ",") != "player,power" {
		t.Fatalf("state after relog = %v, want [player power]", got)
	}
}

// A client that joins after the feed ended gets the state, the live marker,
// then 1000 "feed ended" instead of hanging.
func TestJoinAfterFeedEnd(t *testing.T) {
	h := newHub(64)
	h.publish([]byte(`{"type":"power","n":1}`))
	h.closeAll()
	_, url := server(t, h, "")
	c := dial(t, url+"/feed")
	defer c.CloseNow()
	if m := read(t, c); m.Type != "power" {
		t.Fatalf("first = %+v, want power", m)
	}
	if m := read(t, c); m.Type != "hub" {
		t.Fatalf("second = %+v, want hub", m)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("got %v, want 1000", err)
	}
}
