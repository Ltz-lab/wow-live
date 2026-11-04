package pktlog

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Tail reads a PKT log the server is still writing: each Next returns the
// packets completed since the last call (the header is read on the first).
type Tail struct {
	f          *os.File
	buf        []byte
	header     bool
	startTicks uint32
	frame      int
}

// OpenTail opens a PKT log for tailing from its start.
func OpenTail(path string) (*Tail, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Tail{f: f}, nil
}

// Close closes the file.
func (t *Tail) Close() error { return t.f.Close() }

// Next returns the packets that became complete.
func (t *Tail) Next() ([]Packet, error) {
	chunk := make([]byte, 1<<20)
	for {
		n, err := t.f.Read(chunk)
		t.buf = append(t.buf, chunk[:n]...)
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	off := 0
	if !t.header {
		if len(t.buf) < pktFixedHead+12 {
			return nil, nil
		}
		if string(t.buf[0:3]) != pktMagic {
			return nil, fmt.Errorf("pkt: magic is %q, want %q", t.buf[0:3], pktMagic)
		}
		opt := binary.LittleEndian.Uint32(t.buf[pktFixedHead+8:])
		if len(t.buf) < pktFixedHead+12+int(opt) {
			return nil, nil
		}
		t.startTicks = binary.LittleEndian.Uint32(t.buf[pktFixedHead+4:])
		off, t.header = pktFixedHead+12+int(opt), true
	}
	var out []Packet
	for off+20 <= len(t.buf) {
		le := binary.LittleEndian
		dir, conn, ticks := le.Uint32(t.buf[off:]), le.Uint32(t.buf[off+4:]), le.Uint32(t.buf[off+8:])
		optsize, length := le.Uint32(t.buf[off+12:]), le.Uint32(t.buf[off+16:])
		end := off + 20 + int(optsize) + int(length)
		if end > len(t.buf) {
			break
		}
		body := t.buf[off+20+int(optsize) : end]
		off = end
		index := t.frame
		t.frame++
		if length < 4 {
			continue
		}
		var d Dir
		switch dir {
		case dirC2S:
			d = C2S
		case dirS2C:
			d = S2C
		default:
			continue
		}
		out = append(out, Packet{Index: index, Dir: d, Conn: int(conn), TicksMs: ticks - t.startTicks,
			Opcode: le.Uint32(body), Body: append([]byte(nil), body[4:]...)})
	}
	t.buf = append(t.buf[:0], t.buf[off:]...)
	return out, nil
}
