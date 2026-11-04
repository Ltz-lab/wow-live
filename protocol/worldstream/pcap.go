package worldstream

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// Tail reads TCP segments from a pcapng file, including one tcpdump is still
// writing (`tcpdump -U -w`): each Next returns the segments in the blocks
// completed since the last call and keeps a partial block for later.
type Tail struct {
	f     *os.File
	buf   []byte
	links []uint16
}

// OpenTail opens a pcapng capture for tailing.
func OpenTail(path string) (*Tail, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Tail{f: f}, nil
}

// Close closes the file.
func (t *Tail) Close() error { return t.f.Close() }

// Next returns the segments that became complete.
func (t *Tail) Next() ([]Segment, error) {
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
	var segs []Segment
	le := binary.LittleEndian
	p := 0
	for p+12 <= len(t.buf) {
		typ, blen := le.Uint32(t.buf[p:]), int(le.Uint32(t.buf[p+4:]))
		if blen < 12 {
			return segs, fmt.Errorf("pcapng: block of %d bytes at %d (not pcapng?)", blen, p)
		}
		if p+blen > len(t.buf) {
			break
		}
		b := t.buf[p+8 : p+blen-4]
		switch typ {
		case 1: // interface description
			t.links = append(t.links, le.Uint16(b))
		case 6: // enhanced packet
			iface := le.Uint32(b)
			ts := uint64(le.Uint32(b[4:]))<<32 | uint64(le.Uint32(b[8:]))
			capLen := int(le.Uint32(b[12:]))
			if 20+capLen <= len(b) {
				link := uint16(1)
				if int(iface) < len(t.links) {
					link = t.links[iface]
				}
				if s, ok := parsePacket(link, b[20:20+capLen]); ok {
					s.T = time.UnixMicro(int64(ts))
					segs = append(segs, s)
				}
			}
		}
		p += blen
	}
	t.buf = append(t.buf[:0], t.buf[p:]...)
	return segs, nil
}

func parsePacket(link uint16, d []byte) (Segment, bool) {
	var s Segment
	switch link {
	case 258: // PKTAP: its own header, then the inner link
		if len(d) < 8 {
			return s, false
		}
		hl := int(binary.LittleEndian.Uint32(d))
		inner := binary.LittleEndian.Uint32(d[4:])
		if hl > len(d) {
			return s, false
		}
		return parsePacket(uint16(inner), d[hl:])
	case 1: // Ethernet
		if len(d) < 14 {
			return s, false
		}
		d = d[14:]
	case 0: // BSD loopback
		if len(d) < 4 {
			return s, false
		}
		d = d[4:]
	case 12, 101: // raw IP
	default:
		return s, false
	}
	if len(d) < 20 {
		return s, false
	}
	var tcp []byte
	switch d[0] >> 4 {
	case 4:
		ihl := int(d[0]&0xf) * 4
		total := int(binary.BigEndian.Uint16(d[2:]))
		if d[9] != 6 || total > len(d) || ihl > total {
			return s, false
		}
		s.Src = fmt.Sprintf("%d.%d.%d.%d", d[12], d[13], d[14], d[15])
		s.Dst = fmt.Sprintf("%d.%d.%d.%d", d[16], d[17], d[18], d[19])
		tcp = d[ihl:total]
	case 6:
		if len(d) < 40 || d[6] != 6 {
			return s, false
		}
		plen := int(binary.BigEndian.Uint16(d[4:]))
		s.Src, s.Dst = hex.EncodeToString(d[8:24]), hex.EncodeToString(d[24:40])
		if 40+plen > len(d) {
			return s, false
		}
		tcp = d[40 : 40+plen]
	default:
		return s, false
	}
	if len(tcp) < 20 {
		return s, false
	}
	s.SPort, s.DPort = binary.BigEndian.Uint16(tcp), binary.BigEndian.Uint16(tcp[2:])
	s.Seq = binary.BigEndian.Uint32(tcp[4:])
	off := int(tcp[12]>>4) * 4
	flags := tcp[13]
	s.SYN, s.FIN = flags&0x02 != 0, flags&0x01 != 0
	if off > len(tcp) {
		return s, false
	}
	s.Payload = append([]byte(nil), tcp[off:]...)
	return s, true
}
