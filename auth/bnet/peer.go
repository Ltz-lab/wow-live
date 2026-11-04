package bnet

import (
	"io"
	"sync"
)

// PeerFunc handles one request on a Peer and returns the response body and
// Header.status (0 for success).
type PeerFunc func(h Header, body []byte) (resp []byte, status uint32)

// Peer is a minimal in-process bgs server: it answers requests by service
// hash and method, and can push server-initiated requests. It exists so the
// client, login and realm layers can be exercised end to end without a
// network, and so a replay harness can stand in for Battle.net. It does not
// model any server behaviour beyond what a test scripts.
type Peer struct {
	conn *Conn

	mu       sync.Mutex
	handlers map[handlerKey]PeerFunc
}

// NewPeer wraps the server end of a stream.
func NewPeer(rw io.ReadWriter) *Peer {
	return &Peer{conn: NewConn(rw), handlers: map[handlerKey]PeerFunc{}}
}

// Handle registers the handler for a service method.
func (p *Peer) Handle(svc Service, method uint32, fn PeerFunc) {
	p.mu.Lock()
	p.handlers[handlerKey{svc.Hash, method}] = fn
	p.mu.Unlock()
}

// Notify sends a server-initiated request (token 0, no response expected).
func (p *Peer) Notify(svc Service, method uint32, body []byte) error {
	return p.conn.WriteFrame(Frame{
		Header: Header{MethodID: method, ServiceHash: svc.Hash},
		Body:   body,
	})
}

// Serve answers requests until the stream ends. A clean end returns nil.
func (p *Peer) Serve() error {
	for {
		f, err := p.conn.ReadFrame()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if f.Header.IsResponse {
			continue
		}
		p.mu.Lock()
		fn := p.handlers[handlerKey{f.Header.ServiceHash, f.Header.MethodID}]
		p.mu.Unlock()
		var body []byte
		status := uint32(0xbd3) // 0xbd3: the client's own code for "no such service" (0x102394a38)
		if fn != nil {
			body, status = fn(f.Header, f.Body)
		}
		resp := Frame{
			Header: Header{Token: f.Header.Token, IsResponse: true, Status: status},
			Body:   body,
		}
		if err := p.conn.WriteFrame(resp); err != nil {
			return err
		}
	}
}
