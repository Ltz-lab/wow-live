package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
)

// streamOnly are the types that are moments, not state: a client that joins
// late is not sent the last one.
var streamOnly = map[string]bool{"event": true}

// resetType starts a new session (a login or relog): the state saved before it
// belongs to the last character, so it is dropped.
const resetType = "player"

// hub holds the latest line of each kind of state and the connected clients.
// Every line gets a sequence number; a client is sent the state it missed,
// then every line after it, in order, with nothing skipped: a client that
// cannot keep up is dropped rather than slowing the feed.
type hub struct {
	mu      sync.Mutex
	seq     uint64
	state   map[string]stamped // "type" or "type/unit" → its latest line
	clients map[*client]struct{}
	queue   int  // per-client buffer, in lines
	closed  bool // the feed has ended: new clients get the state, then are closed
}

type stamped struct {
	seq  uint64
	typ  string
	line []byte // with "v" and "seq" spliced in
}

type client struct {
	types map[string]bool // nil: every type
	out   chan []byte
	gone  chan struct{} // closed when the hub drops it for being too slow
	once  sync.Once
}

func newHub(queue int) *hub {
	return &hub{state: map[string]stamped{}, clients: map[*client]struct{}{}, queue: queue}
}

// head is the part of a line the hub looks at.
type head struct {
	Type string `json:"type"`
	Unit string `json:"unit"`
}

// publish stamps one feed line and sends it on. Lines that are not JSON
// objects are ignored (they still went to stdout).
func (h *hub) publish(line []byte) {
	if len(line) < 2 || line[0] != '{' {
		return
	}
	var hd head
	if json.Unmarshal(line, &hd) != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	s := stamp(h.seq, line)
	if hd.Type == resetType {
		clear(h.state)
	}
	if hd.Type != "" && !streamOnly[hd.Type] {
		k := hd.Type
		if hd.Unit != "" {
			k += "/" + hd.Unit
		}
		h.state[k] = stamped{h.seq, hd.Type, s}
	}
	for c := range h.clients {
		if c.wants(hd.Type) {
			h.send(c, s)
		}
	}
}

// stamp splices {"v":1,"seq":N, in front of the line's own fields.
func stamp(seq uint64, line []byte) []byte {
	b := make([]byte, 0, len(line)+32)
	b = append(b, `{"v":1,"seq":`...)
	b = strconv.AppendUint(b, seq, 10)
	if rest := line[1:]; len(rest) > 0 && rest[0] != '}' {
		b = append(b, ',')
	}
	return append(b, line[1:]...)
}

func (c *client) wants(t string) bool { return c.types == nil || c.types[t] }

// send hands a line to a client, or drops the client if its buffer is full.
// Called with h.mu held.
func (h *hub) send(c *client, line []byte) {
	select {
	case c.out <- line:
	default:
		delete(h.clients, c)
		c.once.Do(func() { close(c.gone) })
	}
}

// join registers a client and queues the current state for it, oldest first,
// then a {"type":"hub","live":true} line carrying the latest seq, all under
// the same lock as publish so no line falls between them.
func (h *hub) join(types map[string]bool) *client {
	c := &client{types: types, out: make(chan []byte, h.queue), gone: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.snapshotLocked() {
		if c.wants(s.typ) {
			h.send(c, s.line)
		}
	}
	// the end of the catch-up: seq from here on rises by one per line
	h.send(c, stamp(h.seq, []byte(`{"type":"hub","live":true}`)))
	select {
	case <-c.gone: // the state alone overflowed its buffer
	default:
		if h.closed {
			close(c.out) // the feed already ended: nothing more will come
		} else {
			h.clients[c] = struct{}{}
		}
	}
	return c
}

func (h *hub) leave(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// snapshot is the current state, oldest first.
func (h *hub) snapshot() []stamped {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

func (h *hub) snapshotLocked() []stamped {
	out := make([]stamped, 0, len(h.state))
	for _, s := range h.state {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// closeAll drops every client, for the end of the feed.
func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.clients {
		delete(h.clients, c)
		close(c.out)
	}
}
