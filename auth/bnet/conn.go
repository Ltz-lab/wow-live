package bnet

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Conn reads and writes whole RPC frames over a byte stream. It mirrors the
// client's read state machine (0x1022e7118): u16 big-endian header length,
// header, then Header.size payload bytes.
//
// WriteFrame is safe for concurrent use; ReadFrame must be called from one
// goroutine.
type Conn struct {
	rw  io.ReadWriter
	wmu sync.Mutex
}

// NewConn wraps a stream (a TLS connection to the bgs endpoint in production,
// a pipe in tests).
func NewConn(rw io.ReadWriter) *Conn { return &Conn{rw: rw} }

// WriteFrame encodes f and writes it with a single Write.
func (c *Conn) WriteFrame(f Frame) error {
	raw, err := Encode(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.rw.Write(raw)
	return err
}

// ReadFrame reads one frame. A clean end of stream before the first byte is
// io.EOF; an end inside a frame is io.ErrUnexpectedEOF.
func (c *Conn) ReadFrame() (Frame, error) {
	var pre [2]byte
	if _, err := io.ReadFull(c.rw, pre[:]); err != nil {
		return Frame{}, err
	}
	hlen := int(binary.BigEndian.Uint16(pre[:]))
	hb := make([]byte, hlen)
	if _, err := io.ReadFull(c.rw, hb); err != nil {
		return Frame{}, unexpected(err)
	}
	h, err := decodeHeader(hb)
	if err != nil {
		return Frame{}, err
	}
	if h.Size >= MaxPayload {
		return Frame{}, fmt.Errorf("%w: header says %d", ErrPayloadTooLarge, h.Size)
	}
	body := make([]byte, h.Size)
	if _, err := io.ReadFull(c.rw, body); err != nil {
		return Frame{}, unexpected(err)
	}
	return Frame{Header: h, Body: body}, nil
}

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
