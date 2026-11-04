// Package tcp is the plain-stream transport: TCP with Nagle disabled.
//
// This is the route a fallback connection takes and the one the TrinityCore
// "forever" fork speaks, so it is also the route a recorded fixture replays.
// It is a plain stream, so the connection uses the tagged frame layout
// (docs/EVIDENCE.md E15).
package tcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Ltz-lab/wow-live/transport"
)

// Dialer dials TCP. The zero value is usable.
type Dialer struct {
	// KeepAlive is the period of TCP keep-alive probes; zero uses the OS
	// default and a negative value disables them.
	KeepAlive time.Duration
}

// Dial implements transport.Dialer.
func (d Dialer) Dial(ctx context.Context, t transport.Target) (transport.Conn, error) {
	if t.Route != "" && t.Route != transport.RouteTCP {
		return nil, fmt.Errorf("%w: %s", transport.ErrNoRoute, t.Route)
	}
	var nd net.Dialer
	if d.KeepAlive != 0 {
		if d.KeepAlive < 0 {
			nd.KeepAlive = -1
		} else {
			nd.KeepAlive = d.KeepAlive
		}
	}
	c, err := nd.DialContext(ctx, "tcp", t.Address)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		// The client disables Nagle on game sockets; per-message latency
		// matters more than packing.
		_ = tc.SetNoDelay(true)
	}
	return &conn{c: c}, nil
}

type conn struct {
	c      net.Conn
	closed bool
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.c.Read(p)
	if err != nil && errors.Is(err, net.ErrClosed) {
		return n, transport.ErrClosed
	}
	return n, err
}

func (c *conn) Write(p []byte) (int, error) {
	n, err := c.c.Write(p)
	if err != nil && errors.Is(err, net.ErrClosed) {
		return n, transport.ErrClosed
	}
	return n, err
}

func (c *conn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.c.Close()
}

func (c *conn) Describe() string { return "tcp " + c.c.RemoteAddr().String() }

func (c *conn) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

var _ io.ReadWriteCloser = (*conn)(nil)
