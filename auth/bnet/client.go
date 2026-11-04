package bnet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

var (
	// ErrClosed means the client's connection ended.
	ErrClosed = errors.New("bnet: connection closed")
	// ErrBindlessRequired means the server did not accept bindless RPC. The
	// only alternative is ConnectionService.Bind, which the client's own
	// descriptors mark obsolete and the shipped client never calls.
	ErrBindlessRequired = errors.New("bnet: server did not accept use_bindless_rpc; Bind is obsolete and not modelled")
)

// StatusError is a response whose Header.status is non-zero.
type StatusError struct {
	Status      uint32
	ServiceHash uint32
	Method      uint32
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("bnet: service %#08x method %d returned status %#x", e.ServiceHash, e.Method, e.Status)
}

// LogonError is a LogonComplete notification with a non-zero error_code.
type LogonError struct{ Code uint32 }

func (e *LogonError) Error() string { return fmt.Sprintf("bnet: logon failed with error %d", e.Code) }

// Handler receives the payload of a server-initiated request.
type Handler func(body []byte)

// ResponseServiceID is the header service id of an RPC response.
const ResponseServiceID = 254

type handlerKey struct{ hash, method uint32 }

type response struct {
	hdr  Header
	body []byte
}

// Client issues bgs RPC requests over a Conn and matches responses to them by
// token. Server-initiated requests (the AuthenticationListener callbacks) go
// to registered handlers, routed by service hash.
type Client struct {
	conn   *Conn
	router *Router
	closer io.Closer

	mu       sync.Mutex
	next     uint32
	pending  map[uint32]chan response
	handlers map[handlerKey]Handler
	ciid     string
	done     chan struct{}
	err      error
	// unhandled, when set, sees every server-initiated message no handler
	// took (the loop drops them, unanswered).
	unhandled func(h Header, body []byte)
}

// OnUnhandled sets the observer for server requests no handler takes.
func (c *Client) OnUnhandled(f func(h Header, body []byte)) {
	c.mu.Lock()
	c.unhandled = f
	c.mu.Unlock()
}

// NewClient starts the read loop over rw. If rw is an io.Closer, Close closes it.
func NewClient(rw io.ReadWriter) *Client {
	c := &Client{
		conn:     NewConn(rw),
		router:   NewRouter(),
		pending:  map[uint32]chan response{},
		handlers: map[handlerKey]Handler{},
		done:     make(chan struct{}),
	}
	if cl, ok := rw.(io.Closer); ok {
		c.closer = cl
	}
	go c.readLoop()
	return c
}

// Router exposes the service routing table.
func (c *Client) Router() *Router { return c.router }

// SetCIID sets the client instance id attached to requests for services whose
// options say use_client_id. Connect fills it from ConnectResponse.ciid when
// the server supplies one.
func (c *Client) SetCIID(id string) {
	c.mu.Lock()
	c.ciid = id
	c.mu.Unlock()
}

// Handle registers the handler for a server-initiated method.
func (c *Client) Handle(svc Service, method uint32, h Handler) {
	c.mu.Lock()
	c.handlers[handlerKey{svc.Hash, method}] = h
	c.mu.Unlock()
}

// Err reports why the client stopped, or nil while it runs.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the client and closes the stream if it can be closed.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	if err == io.EOF {
		err = ErrClosed
	}
	c.err = err
	close(c.done)
}

func (c *Client) readLoop() {
	for {
		f, err := c.conn.ReadFrame()
		if err != nil {
			c.fail(err)
			return
		}
		// Responses are matched by token. The flag is checked too, but a server
		// that never sets it (the TrinityCore fork does not) still answers with
		// the request's token, and tokens are unique per connection.
		c.mu.Lock()
		ch, pending := c.pending[f.Header.Token]
		c.mu.Unlock()
		if f.Header.IsResponse || pending {
			c.mu.Lock()
			delete(c.pending, f.Header.Token)
			c.mu.Unlock()
			if ch != nil {
				ch <- response{f.Header, f.Body}
			}
			continue
		}
		hash, ok := c.router.Hash(f.Header)
		c.mu.Lock()
		var h Handler
		if ok {
			h = c.handlers[handlerKey{hash, f.Header.MethodID}]
		}
		un := c.unhandled
		c.mu.Unlock()
		if h != nil {
			h(f.Body)
			continue
		}
		// ConnectionService.Echo is the server's keepalive (live sends one
		// ~30 s after the realm join, E37); the client answers with an empty
		// EchoResponse under service id 254, the response id.
		if ok && hash == ConnectionService.Hash && f.Header.MethodID == MethodEcho {
			_ = c.conn.WriteFrame(Frame{Header: Header{ServiceID: ResponseServiceID, Token: f.Header.Token}})
			continue
		}
		if un != nil {
			un(f.Header, f.Body)
		}
	}
}

// request builds the header the client's request builder writes
// (0x1022dce60): service_id (0 when bindless), method_id, token, the service
// hash and, for services that use the client id, the ciid.
func (c *Client) request(svc Service, method uint32) (Header, chan response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok := c.next
	c.next++
	h := Header{
		ServiceID:   c.router.ServiceID(svc.Hash),
		MethodID:    method,
		Token:       tok,
		ServiceHash: svc.Hash,
	}
	if svc.UseClientID && c.ciid != "" {
		h.CIID = c.ciid
	}
	ch := make(chan response, 1)
	c.pending[tok] = ch
	return h, ch
}

// Call sends a request and waits for the response with the same token. A
// non-zero Header.status becomes a *StatusError.
func (c *Client) Call(ctx context.Context, svc Service, method uint32, req []byte) ([]byte, error) {
	h, ch := c.request(svc, method)
	if err := c.conn.WriteFrame(Frame{Header: h, Body: req}); err != nil {
		c.mu.Lock()
		delete(c.pending, h.Token)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		if r.hdr.Status != 0 {
			return r.body, &StatusError{Status: r.hdr.Status, ServiceHash: svc.Hash, Method: method}
		}
		return r.body, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, h.Token)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.Err()
	}
}

// Connect performs the ConnectionService.Connect bootstrap with the request
// the client builds (DefaultConnectRequest) and records the ciid the server
// hands out. It fails with ErrBindlessRequired if the server does not accept
// bindless RPC.
func (c *Client) Connect(ctx context.Context) (ConnectResponse, error) {
	return c.ConnectWith(ctx, DefaultConnectRequest())
}

// ConnectWith is Connect with an explicit request.
func (c *Client) ConnectWith(ctx context.Context, req ConnectRequest) (ConnectResponse, error) {
	body, err := c.Call(ctx, ConnectionService, MethodConnect, req.Encode())
	if err != nil {
		return ConnectResponse{}, err
	}
	resp, err := DecodeConnectResponse(body)
	if err != nil {
		return ConnectResponse{}, err
	}
	// Live Battle.net (us.actual.battle.net, 2026-11-04) omits
	// use_bindless_rpc from ConnectResponse and still serves bindless
	// requests, so an absent flag is not a refusal. Only the fork sets it.
	_ = ErrBindlessRequired
	if resp.CIID != "" {
		c.SetCIID(resp.CIID)
	}
	return resp, nil
}

// Bind issues the obsolete ConnectionService.Bind for the given services and
// records the returned ids in the router, assuming (as the descriptor's
// repeated imported_service_id suggests but the binary does not confirm) that
// ids come back in request order. Nothing in the shipped client calls it.
func (c *Client) Bind(ctx context.Context, imported ...Service) error {
	var req BindRequest
	for _, s := range imported {
		req.DeprecatedImportedHashes = append(req.DeprecatedImportedHashes, s.Hash)
	}
	body, err := c.Call(ctx, ConnectionService, MethodBind, EncodeBindRequest(req))
	if err != nil {
		return err
	}
	resp, err := DecodeBindResponse(body)
	if err != nil {
		return err
	}
	if len(resp.ImportedServiceIDs) != len(imported) {
		return fmt.Errorf("bnet: Bind returned %d ids for %d services", len(resp.ImportedServiceIDs), len(imported))
	}
	for i, s := range imported {
		c.router.Set(s.Hash, resp.ImportedServiceIDs[i])
	}
	return nil
}

// Logon sends AuthenticationService.Logon and waits for the server's
// OnLogonComplete callback, whose LogonRecord carries the session key and the
// login ticket. The callback handler is installed before the request goes
// out, because the server may deliver it before the Logon response.
func (c *Client) Logon(ctx context.Context, req LogonRequest) (LogonRecord, error) {
	done := make(chan LogonComplete, 1)
	bad := make(chan error, 1)
	c.Handle(AuthenticationListener, MethodOnLogonComplete, func(body []byte) {
		if os.Getenv("WOW_LOGIN_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "logoncomplete %x\n", body)
		}
		n, err := DecodeLogonComplete(body)
		if err != nil {
			bad <- err
			return
		}
		done <- n
	})
	if _, err := c.Call(ctx, AuthenticationService, MethodLogon, req.Encode()); err != nil {
		return LogonRecord{}, err
	}
	select {
	case n := <-done:
		if n.ErrorCode != 0 {
			return n.Record, &LogonError{Code: n.ErrorCode}
		}
		return n.Record, nil
	case err := <-bad:
		return LogonRecord{}, err
	case <-ctx.Done():
		return LogonRecord{}, ctx.Err()
	case <-c.done:
		return LogonRecord{}, c.Err()
	}
}

// ProcessTask sends GameUtilitiesService.ProcessTask, the carrier of every
// realm command, and decodes the attribute results.
func (c *Client) ProcessTask(ctx context.Context, req ProcessTaskRequest) (ProcessTaskResponse, error) {
	body, err := c.Call(ctx, GameUtilities, MethodProcessTask, req.Encode())
	if err != nil {
		return ProcessTaskResponse{}, err
	}
	return DecodeProcessTaskResponse(body)
}

// ProcessTaskV1 sends GameUtilities.ProcessTask against the v1 service hash and
// with v1 Variant numbering.
func (c *Client) ProcessTaskV1(ctx context.Context, req ProcessTaskRequest) (ProcessTaskResponse, error) {
	body, err := c.Call(ctx, ServiceV1(GameUtilities), MethodProcessTaskV1, req.EncodeV1())
	if err != nil {
		return ProcessTaskResponse{}, err
	}
	return DecodeProcessTaskResponse(body)
}
