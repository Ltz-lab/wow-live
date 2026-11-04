// Package pktlog reads TrinityCore's PKT 3.1 packet log (PacketLogFile), the
// record the local fork writes of a real session: every frame the server
// logged, both directions, with the opcode number the server's own table gave
// it.
//
// The numbers are the *server's* enumeration, not the client's: the fork's
// SMSG_UPDATE_OBJECT is 0x5c0000 where the client's is 0x5e0000. Bodies are
// the client's bytes unchanged, so a body is only meaningful next to the name
// it was logged under (tools/pktread.py in the server repo, or the table in
// protocol/opcodes for client numbers).
//
// See docs/EVIDENCE.md E38 (tools/worlddiff.py) for how the fork's log and the
// stock client's capture are compared.
package pktlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// PKT 3.1 layout, as the fork writes it and tools/pktread.py reads it:
//
//	+0   [3]byte  magic "PKT"
//	+3   uint16   version 0x0301
//	+5   uint8    sniffer id
//	+6   uint32   build
//	+10  [4]byte  locale
//	+14  ...      forty bytes written as a struct with its own padding, skipped
//	+54  uint32   start unix seconds
//	+58  uint32   start tick milliseconds
//	+62  uint32   header option length, then that many bytes
//	..  packets
//
// Each packet is five little-endian uint32s (direction, connection id, tick
// milliseconds, option length, length), the option bytes, then the frame the
// server logged: uint32 opcode followed by the payload. direction is the
// ASCII tag "GSMC" (client to server) or "GSMS" (server to client) read as a
// little-endian uint32.
const (
	pktMagic     = "PKT"
	pktVersion   = 0x0301
	pktFixedHead = 3 + 2 + 1 + 4 + 4 + 40

	dirC2S = 0x47534D43 // "GSMC" as written by the logger
	dirS2C = 0x47534D53 // "GSMS"
)

// Dir is a packet's direction.
type Dir int

const (
	// C2S is client to server.
	C2S Dir = iota
	// S2C is server to client.
	S2C
)

// String returns "C2S" or "S2C".
func (d Dir) String() string {
	if d == C2S {
		return "C2S"
	}
	return "S2C"
}

// Packet is one logged frame.
type Packet struct {
	// Index is the packet's position in the log, from 0.
	Index int
	Dir   Dir
	// Conn is the connection id the server logged it on: 0 is the first
	// (realm) connection, 1 the instance connection CONNECT_TO names.
	Conn    int
	TicksMs uint32 // milliseconds, already rebased on the log's start ticks
	Opcode  uint32 // the number the server logged (its own core enumeration)
	Body    []byte
}

// TimeMs is the packet's timestamp relative to the log start.
func (p Packet) TimeMs() float64 { return float64(p.TicksMs) }

// Log is a parsed packet log.
type Log struct {
	Build  uint32
	Locale string
	Start  uint32 // unix seconds
	// StartTicks is the tick the log rebases on; packet timestamps are already
	// relative to it.
	StartTicks uint32
	Packets    []Packet
}

// ReadPKT parses a PKT 3.1 log.
func ReadPKT(raw []byte) (*Log, error) {
	if len(raw) < pktFixedHead+12 {
		return nil, fmt.Errorf("pkt: %d bytes is shorter than the header", len(raw))
	}
	if string(raw[0:3]) != pktMagic {
		return nil, fmt.Errorf("pkt: magic is %q, want %q", raw[0:3], pktMagic)
	}
	if v := binary.LittleEndian.Uint16(raw[3:5]); v != pktVersion {
		return nil, fmt.Errorf("pkt: version %#x, want %#x", v, pktVersion)
	}
	l := &Log{
		Build:  binary.LittleEndian.Uint32(raw[6:10]),
		Locale: string(raw[10:14]),
	}
	off := pktFixedHead
	l.Start = binary.LittleEndian.Uint32(raw[off:])
	l.StartTicks = binary.LittleEndian.Uint32(raw[off+4:])
	opt := binary.LittleEndian.Uint32(raw[off+8:])
	off += 12 + int(opt)
	if off > len(raw) {
		return nil, errors.New("pkt: header option runs past the file")
	}
	frame := 0
	for off+20 <= len(raw) {
		index := frame
		frame++
		dir := binary.LittleEndian.Uint32(raw[off:])
		conn := binary.LittleEndian.Uint32(raw[off+4:])
		ticks := binary.LittleEndian.Uint32(raw[off+8:])
		optsize := binary.LittleEndian.Uint32(raw[off+12:])
		length := binary.LittleEndian.Uint32(raw[off+16:])
		off += 20
		off += int(optsize)
		if off+int(length) > len(raw) {
			break // a packet cut off by a live writer, as pktread.py says
		}
		if length < 4 {
			off += int(length)
			continue
		}
		op := binary.LittleEndian.Uint32(raw[off:])
		body := raw[off+4 : off+int(length)]
		off += int(length)
		var d Dir
		switch dir {
		case dirC2S:
			d = C2S
		case dirS2C:
			d = S2C
		default:
			continue
		}
		l.Packets = append(l.Packets, Packet{
			Index: index, Dir: d, Conn: int(conn), TicksMs: ticks - l.StartTicks, Opcode: op, Body: body,
		})
	}
	return l, nil
}

// ReadPKTFile parses a PKT file.
func ReadPKTFile(path string) (*Log, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ReadPKT(raw)
}

// LastLoginIndex is the index of the last CMSG_PLAYER_LOGIN in the log, given
// the name of an opcode number, or -1. A log usually holds more than one
// session (a character screen, a death, a logout and back in), and the live
// session is the last one.
func (l *Log) LastLoginIndex(name func(Dir, uint32) string) int {
	last := -1
	for i, p := range l.Packets {
		if p.Dir == C2S && name(p.Dir, p.Opcode) == "CMSG_PLAYER_LOGIN" {
			last = i
		}
	}
	return last
}

// Since returns the packets from index i on.
func (l *Log) Since(i int) []Packet {
	if i < 0 || i >= len(l.Packets) {
		return nil
	}
	return l.Packets[i:]
}
