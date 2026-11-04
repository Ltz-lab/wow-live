package world

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// ConnectTo serials (the fork's ConnectToSerial). A failed dial is reported
// with CMSG_CONNECT_TO_FAILED carrying the serial, and the server retries with
// the next one.
const (
	ConnectToSerialRealm  = 14
	ConnectToSerialWorld1 = 17
	ConnectToSerialWorld2 = 35
	ConnectToSerialWorld3 = 53
	ConnectToSerialWorld4 = 71
	ConnectToSerialWorld5 = 89
)

// ConnectTo address types.
const (
	ConnectAddrNone  = 0
	ConnectAddrIPv4  = 1
	ConnectAddrIPv6  = 2
	ConnectAddrNamed = 3
)

// ConnectTo is SMSG_CONNECT_TO (0x4E0008): the server asking for a second
// connection, the one world entry continues on.
//
// Layout (the fork's ConnectTo::Write, and the real capture's 45-byte #190):
// u32 payload count, u32 serial, u8 con, u64 key, u32 native realm address,
// u32 key3, then per payload u8 address type, the address (4 or 16 bytes, or
// a C string for a named socket), u16 port and a BLEEP token.
type ConnectTo struct {
	Serial             uint32
	Con                uint8
	Key                uint64
	NativeRealmAddress uint32
	Key3               uint32
	Payloads           []ConnectPayload
}

// ConnectPayload is one address the client may dial.
type ConnectPayload struct {
	Type uint8
	IP   net.IP
	Name string
	Port uint16
	// Token is the BLEEP token; empty on the fork.
	Token BleepToken
}

// BleepToken is the per-address proxy token: 5-, 24- and 6-bit lengths, an
// i64 lifespan in nanoseconds, then the token, the proxy id (a sized C string
// whose length counts its NUL) and the address.
type BleepToken struct {
	Token, ProxyID, Address string
	LifespanNanos           int64
}

// AccountID is the low 32 bits of Key (the fork's ConnectToKey.AccountId).
func (c ConnectTo) AccountID() uint32 { return uint32(c.Key) }

// Instance reports the ConnectionType bit of Key: set for the instance
// (world) connection, the only kind the fork accepts a continued session for.
func (c ConnectTo) Instance() bool { return c.Key>>32&1 == 1 }

// Target returns the first dialable address as host:port, IPv4 first unless
// ipv6 is set.
func (c ConnectTo) Target(ipv6 bool) (string, error) {
	want := uint8(ConnectAddrIPv4)
	if ipv6 {
		want = ConnectAddrIPv6
	}
	for _, pass := range []bool{true, false} {
		for _, p := range c.Payloads {
			if p.Type != ConnectAddrIPv4 && p.Type != ConnectAddrIPv6 {
				continue
			}
			if pass && p.Type != want {
				continue
			}
			return net.JoinHostPort(p.IP.String(), strconv.Itoa(int(p.Port))), nil
		}
	}
	return "", errors.New("world: CONNECT_TO lists no IP address")
}

// DecodeConnectTo parses the SMSG_CONNECT_TO body (no opcode).
func DecodeConnectTo(body []byte) (ConnectTo, error) {
	var c ConnectTo
	r := codec.NewReader(body)
	n, err := r.Uint32()
	if err != nil {
		return c, err
	}
	if n > 16 {
		return c, fmt.Errorf("world: CONNECT_TO claims %d payloads", n)
	}
	if c.Serial, err = r.Uint32(); err != nil {
		return c, err
	}
	if c.Con, err = r.Uint8(); err != nil {
		return c, err
	}
	if c.Key, err = r.Uint64(); err != nil {
		return c, err
	}
	if c.NativeRealmAddress, err = r.Uint32(); err != nil {
		return c, err
	}
	if c.Key3, err = r.Uint32(); err != nil {
		return c, err
	}
	for i := uint32(0); i < n; i++ {
		p, err := decodeConnectPayload(r)
		if err != nil {
			return c, fmt.Errorf("world: CONNECT_TO payload %d: %w", i, err)
		}
		c.Payloads = append(c.Payloads, p)
	}
	return c, nil
}

func decodeConnectPayload(r *codec.Reader) (ConnectPayload, error) {
	var p ConnectPayload
	var err error
	if p.Type, err = r.Uint8(); err != nil {
		return p, err
	}
	switch p.Type {
	case ConnectAddrIPv4:
		b, err := r.Bytes(4)
		if err != nil {
			return p, err
		}
		p.IP = net.IPv4(b[0], b[1], b[2], b[3])
	case ConnectAddrIPv6:
		b, err := r.Bytes(16)
		if err != nil {
			return p, err
		}
		p.IP = append(net.IP(nil), b...)
	case ConnectAddrNamed:
		if p.Name, err = r.CString(); err != nil {
			return p, err
		}
	case ConnectAddrNone:
	default:
		return p, fmt.Errorf("address type %d", p.Type)
	}
	if p.Port, err = r.Uint16(); err != nil {
		return p, err
	}
	p.Token, err = decodeBleepToken(r)
	return p, err
}

func decodeBleepToken(r *codec.Reader) (BleepToken, error) {
	var t BleepToken
	tokLen, err := r.Bits(5)
	if err != nil {
		return t, err
	}
	proxyLen, err := r.Bits(24)
	if err != nil {
		return t, err
	}
	addrLen, err := r.Bits(6)
	if err != nil {
		return t, err
	}
	r.ResetBits()
	life, err := r.Uint64()
	if err != nil {
		return t, err
	}
	t.LifespanNanos = int64(life)
	tok, err := r.Bytes(int(tokLen))
	if err != nil {
		return t, err
	}
	t.Token = string(tok)
	// The proxy id's length counts its NUL, which is not on the wire: the
	// fork's empty id has length 1 and no bytes (capture #190).
	if proxyLen > 0 {
		px, err := r.Bytes(int(proxyLen - 1))
		if err != nil {
			return t, err
		}
		t.ProxyID = string(px)
	}
	ad, err := r.Bytes(int(addrLen))
	if err != nil {
		return t, err
	}
	t.Address = string(ad)
	return t, nil
}

// ConnectToFailedBody is the CMSG_CONNECT_TO_FAILED body: u8 con, u32 serial
// (the fork's ConnectToFailed::Read).
func ConnectToFailedBody(con uint8, serial uint32) []byte {
	w := codec.NewWriterCap(5)
	w.Uint8(con)
	w.Uint32(serial)
	return w.Bytes()
}
