// Package transport is the only thing in the tree that touches a socket.
//
// A transport carries bytes and nothing else: it never sees an opcode, a
// message or a handshake line. Everything above it (framing, auth, dispatch)
// works through Conn, so a QUIC, TCP or BLEEP route is interchangeable and a
// recorded route can be replayed in a test.
//
// The client itself prefers QUIC and falls back to TCP
// (docs/EVIDENCE.md E4/E7: `maximumConnectionAttempts`
// "Maximum number of times to try to connect to a server using QUIC before
// doing TCP"). Only TCP is implemented; QUIC is a separate Dialer.
package transport

import (
	"context"
	"errors"
	"time"
)

// Route names the way a connection was made. Diagnostics and captures record
// it, because the frame layout depends on the transport (EVIDENCE.md E15).
type Route string

// Known routes.
const (
	RouteTCP   Route = "tcp"
	RouteQUIC  Route = "quic"
	RouteBLEEP Route = "bleep"
)

// Target is what to dial. Address is host:port; Host is kept separately because
// QUIC needs it for SNI and certificate checks.
type Target struct {
	Address string
	Host    string
	Route   Route
}

// Dialer opens a connection.
type Dialer interface {
	Dial(ctx context.Context, t Target) (Conn, error)
}

// Conn is one reliable byte stream.
//
// Implementations may fail with ErrClosed after Close; a second Close is a
// no-op. Deadlines are per call, as on net.Conn, and are cleared by a zero
// time.
type Conn interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	// Describe returns a short, log-safe description, e.g. "tcp 1.2.3.4:1119".
	Describe() string
	// SetDeadline bounds the whole connection (handshake and idle reads).
	SetDeadline(t time.Time) error
}

var (
	// ErrClosed is returned once the connection is closed.
	ErrClosed = errors.New("transport: connection closed")
	// ErrNoRoute means the dialer cannot serve the target's route.
	ErrNoRoute = errors.New("transport: unsupported route")
)
