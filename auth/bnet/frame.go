// Package bnet is the Battle.net (bgs) RPC layer that runs before the world
// socket exists: the frame envelope, the Header message, the bootstrap
// (ConnectionService.Connect), the authentication Logon, and the generic
// attribute-bag GameUtilities.ProcessTask that carries the realm commands.
//
// Everything here is read off the client's own code and the protobuf
// descriptors it embeds (docs/EVIDENCE.md E26, tools/pbdesc.py).
//
// # Frame
//
// On the wire a bgs RPC frame is
//
//	u16 big-endian  header length
//	Header          protobuf, that many bytes
//	payload         Header.size bytes
//
// The client's read state machine (rpc_frame_transport_default.cpp,
// OnReadComplete at 0x1022e7118) has three states: 0 reads the u16 and
// byte-swaps it (FUN_1022dd1fc: *p>>8 | *p<<8), 1 reads and parses that many
// header bytes and takes Header.size (rejecting size >= 1<<20, the
// `>>0x14 == 0` test), 2 reads size payload bytes. The write side
// (SendMessage 0x1022e77c8, FUN_1022dd174) is the mirror: it writes the swapped
// ByteSize of the header, the header, then the message body.
package bnet

import (
	"encoding/binary"
	"fmt"
)

// MaxPayload is the exclusive upper bound on Header.size. The client rejects
// a header whose size has any bit at or above 1<<20 (0x1022e7118,
// `*(uint *)(hdr + 0xb4) >> 0x14 == 0`).
const MaxPayload = 1 << 20

// maxHeader is the largest header the u16 prefix can describe.
const maxHeader = 0xffff

// ProcessID is bgs.protocol.ProcessId (rpc_types.proto).
type ProcessID struct {
	Label uint32 // field 1, required
	Epoch uint32 // field 2, required
}

func (p ProcessID) encode() []byte {
	var w pbWriter
	w.varint(1, uint64(p.Label))
	w.varint(2, uint64(p.Epoch))
	return w.b
}

func decodeProcessID(b []byte) (ProcessID, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ProcessID{}, err
	}
	var p ProcessID
	var got [2]bool
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "ProcessId.label"); err != nil {
				return p, err
			}
			p.Label, got[0] = uint32(f.V), true
		case 2:
			if err := f.want(wireVarint, "ProcessId.epoch"); err != nil {
				return p, err
			}
			p.Epoch, got[1] = uint32(f.V), true
		}
	}
	if !got[0] || !got[1] {
		return p, fmt.Errorf("%w: ProcessId", ErrMissingRequired)
	}
	return p, nil
}

// ObjectAddress is bgs.protocol.ObjectAddress.
type ObjectAddress struct {
	Host     ProcessID // field 1, required
	ObjectID uint64    // field 2
}

func (a ObjectAddress) encode() []byte {
	var w pbWriter
	w.bytes(1, a.Host.encode())
	if a.ObjectID != 0 {
		w.varint(2, a.ObjectID)
	}
	return w.b
}

func decodeObjectAddress(b []byte) (ObjectAddress, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ObjectAddress{}, err
	}
	var a ObjectAddress
	var host bool
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "ObjectAddress.host"); err != nil {
				return a, err
			}
			if a.Host, err = decodeProcessID(f.B); err != nil {
				return a, err
			}
			host = true
		case 2:
			if err := f.want(wireVarint, "ObjectAddress.object_id"); err != nil {
				return a, err
			}
			a.ObjectID = f.V
		}
	}
	if !host {
		return a, fmt.Errorf("%w: ObjectAddress.host", ErrMissingRequired)
	}
	return a, nil
}

// ErrorInfo is bgs.protocol.ErrorInfo; all four fields are required.
type ErrorInfo struct {
	ObjectAddress ObjectAddress // field 1
	Status        uint32        // field 2
	ServiceHash   uint32        // field 3
	MethodID      uint32        // field 4
}

func (e ErrorInfo) encode() []byte {
	var w pbWriter
	w.bytes(1, e.ObjectAddress.encode())
	w.varint(2, uint64(e.Status))
	w.varint(3, uint64(e.ServiceHash))
	w.varint(4, uint64(e.MethodID))
	return w.b
}

func decodeErrorInfo(b []byte) (ErrorInfo, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ErrorInfo{}, err
	}
	var e ErrorInfo
	var got [4]bool
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "ErrorInfo.object_address"); err != nil {
				return e, err
			}
			if e.ObjectAddress, err = decodeObjectAddress(f.B); err != nil {
				return e, err
			}
			got[0] = true
		case 2:
			if err := f.want(wireVarint, "ErrorInfo.status"); err != nil {
				return e, err
			}
			e.Status, got[1] = uint32(f.V), true
		case 3:
			if err := f.want(wireVarint, "ErrorInfo.service_hash"); err != nil {
				return e, err
			}
			e.ServiceHash, got[2] = uint32(f.V), true
		case 4:
			if err := f.want(wireVarint, "ErrorInfo.method_id"); err != nil {
				return e, err
			}
			e.MethodID, got[3] = uint32(f.V), true
		}
	}
	for _, g := range got {
		if !g {
			return e, fmt.Errorf("%w: ErrorInfo", ErrMissingRequired)
		}
	}
	return e, nil
}

// FanoutTarget is bgs.protocol.FanoutTarget.
type FanoutTarget struct {
	ClientID string // field 1
	Key      []byte // field 2
	ObjectID uint64 // field 3
}

func (t FanoutTarget) encode() []byte {
	var w pbWriter
	if t.ClientID != "" {
		w.str(1, t.ClientID)
	}
	if len(t.Key) != 0 {
		w.bytes(2, t.Key)
	}
	if t.ObjectID != 0 {
		w.varint(3, t.ObjectID)
	}
	return w.b
}

func decodeFanoutTarget(b []byte) (FanoutTarget, error) {
	fs, err := pbParse(b)
	if err != nil {
		return FanoutTarget{}, err
	}
	var t FanoutTarget
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "FanoutTarget.client_id"); err != nil {
				return t, err
			}
			t.ClientID = string(f.B)
		case 2:
			if err := f.want(wireBytes, "FanoutTarget.key"); err != nil {
				return t, err
			}
			t.Key = append([]byte(nil), f.B...)
		case 3:
			if err := f.want(wireVarint, "FanoutTarget.object_id"); err != nil {
				return t, err
			}
			t.ObjectID = f.V
		}
	}
	return t, nil
}

// Header is bgs.protocol.Header (rpc_types.proto; descriptor at image offset
// 0x31f72dc, dumped by tools/pbdesc.py). Field numbers are the descriptor's;
// the serializer at 0x10238c750 confirms the order and the key bytes for the
// fields the client writes (0x08 service_id, 0x10 method_id, 0x18 token,
// 0x20 object_id, 0x28 size, 0x5d service_hash, ...).
//
// ServiceID and Token are `required` in the descriptor and are always
// emitted, including a zero ServiceID: the client's request builder
// (0x1022dce60) sets service_id to an explicit 0, together with method_id,
// token and service_hash (has-bits 0x2160), and Size via 0x1022dce28. Every
// other field is omitted at its zero value. Size is not caller-set: Encode
// fills it from the payload length, as the client does.
type Header struct {
	ServiceID      uint32         // 1, required
	MethodID       uint32         // 2
	Token          uint32         // 3, required
	ObjectID       uint64         // 4
	Size           uint32         // 5, payload length; Encode sets it
	Status         uint32         // 6
	Errors         []ErrorInfo    // 7
	Timeout        uint64         // 8
	IsResponse     bool           // 9
	ForwardTargets []ProcessID    // 10
	ServiceHash    uint32         // 11, fixed32
	CIID           string         // 13, client instance id
	FanoutTargets  []FanoutTarget // 14
	CIIDFanout     []string       // 15
	ClientRecord   []byte         // 16
	OriginalSender []byte         // 17
	SenderToken    uint32         // 18
	RouterLabel    uint32         // 19
	ErrorReason    string         // 20
	SpanTraceID    string         // 21 (Span.trace_id)
}

func encodeHeader(h Header) []byte {
	var w pbWriter
	w.varint(1, uint64(h.ServiceID))
	if h.MethodID != 0 {
		w.varint(2, uint64(h.MethodID))
	}
	w.varint(3, uint64(h.Token))
	if h.ObjectID != 0 {
		w.varint(4, h.ObjectID)
	}
	w.varint(5, uint64(h.Size))
	if h.Status != 0 {
		w.varint(6, uint64(h.Status))
	}
	for _, e := range h.Errors {
		w.bytes(7, e.encode())
	}
	if h.Timeout != 0 {
		w.varint(8, h.Timeout)
	}
	if h.IsResponse {
		w.boolean(9, true)
	}
	for _, p := range h.ForwardTargets {
		w.bytes(10, p.encode())
	}
	if h.ServiceHash != 0 {
		w.fixed32(11, h.ServiceHash)
	}
	if h.CIID != "" {
		w.str(13, h.CIID)
	}
	for _, t := range h.FanoutTargets {
		w.bytes(14, t.encode())
	}
	for _, s := range h.CIIDFanout {
		w.str(15, s)
	}
	if len(h.ClientRecord) != 0 {
		w.bytes(16, h.ClientRecord)
	}
	if len(h.OriginalSender) != 0 {
		w.bytes(17, h.OriginalSender)
	}
	if h.SenderToken != 0 {
		w.varint(18, uint64(h.SenderToken))
	}
	if h.RouterLabel != 0 {
		w.varint(19, uint64(h.RouterLabel))
	}
	if h.ErrorReason != "" {
		w.str(20, h.ErrorReason)
	}
	if h.SpanTraceID != "" {
		var sp pbWriter
		sp.str(1, h.SpanTraceID)
		w.bytes(21, sp.b)
	}
	return w.b
}

// decodeHeader is strict: known fields must carry the wire type the
// descriptor gives them, the two required fields must be present, and
// structurally malformed input is an error. Field numbers the descriptor does
// not define are skipped, which is what a proto2 parser does with unknown
// fields.
func decodeHeader(b []byte) (Header, error) {
	fs, err := pbParse(b)
	if err != nil {
		return Header{}, err
	}
	var h Header
	var hasService, hasToken bool
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "Header.service_id"); err != nil {
				return h, err
			}
			h.ServiceID, hasService = uint32(f.V), true
		case 2:
			if err := f.want(wireVarint, "Header.method_id"); err != nil {
				return h, err
			}
			h.MethodID = uint32(f.V)
		case 3:
			if err := f.want(wireVarint, "Header.token"); err != nil {
				return h, err
			}
			h.Token, hasToken = uint32(f.V), true
		case 4:
			if err := f.want(wireVarint, "Header.object_id"); err != nil {
				return h, err
			}
			h.ObjectID = f.V
		case 5:
			if err := f.want(wireVarint, "Header.size"); err != nil {
				return h, err
			}
			h.Size = uint32(f.V)
		case 6:
			if err := f.want(wireVarint, "Header.status"); err != nil {
				return h, err
			}
			h.Status = uint32(f.V)
		case 7:
			if err := f.want(wireBytes, "Header.error"); err != nil {
				return h, err
			}
			e, err := decodeErrorInfo(f.B)
			if err != nil {
				return h, err
			}
			h.Errors = append(h.Errors, e)
		case 8:
			if err := f.want(wireVarint, "Header.timeout"); err != nil {
				return h, err
			}
			h.Timeout = f.V
		case 9:
			if err := f.want(wireVarint, "Header.is_response"); err != nil {
				return h, err
			}
			h.IsResponse = f.V != 0
		case 10:
			if err := f.want(wireBytes, "Header.forward_targets"); err != nil {
				return h, err
			}
			p, err := decodeProcessID(f.B)
			if err != nil {
				return h, err
			}
			h.ForwardTargets = append(h.ForwardTargets, p)
		case 11:
			if err := f.want(wireFixed32, "Header.service_hash"); err != nil {
				return h, err
			}
			h.ServiceHash = uint32(f.V)
		case 13:
			if err := f.want(wireBytes, "Header.ciid"); err != nil {
				return h, err
			}
			h.CIID = string(f.B)
		case 14:
			if err := f.want(wireBytes, "Header.fanout_target"); err != nil {
				return h, err
			}
			t, err := decodeFanoutTarget(f.B)
			if err != nil {
				return h, err
			}
			h.FanoutTargets = append(h.FanoutTargets, t)
		case 15:
			if err := f.want(wireBytes, "Header.ciid_fanout_target"); err != nil {
				return h, err
			}
			h.CIIDFanout = append(h.CIIDFanout, string(f.B))
		case 16:
			if err := f.want(wireBytes, "Header.client_record"); err != nil {
				return h, err
			}
			h.ClientRecord = append([]byte(nil), f.B...)
		case 17:
			if err := f.want(wireBytes, "Header.original_sender"); err != nil {
				return h, err
			}
			h.OriginalSender = append([]byte(nil), f.B...)
		case 18:
			if err := f.want(wireVarint, "Header.sender_token"); err != nil {
				return h, err
			}
			h.SenderToken = uint32(f.V)
		case 19:
			if err := f.want(wireVarint, "Header.router_label"); err != nil {
				return h, err
			}
			h.RouterLabel = uint32(f.V)
		case 20:
			if err := f.want(wireBytes, "Header.error_reason"); err != nil {
				return h, err
			}
			h.ErrorReason = string(f.B)
		case 21:
			if err := f.want(wireBytes, "Header.span"); err != nil {
				return h, err
			}
			sp, err := pbParse(f.B)
			if err != nil {
				return h, err
			}
			for _, sf := range sp {
				if sf.Num == 1 {
					if err := sf.want(wireBytes, "Span.trace_id"); err != nil {
						return h, err
					}
					h.SpanTraceID = string(sf.B)
				}
			}
		}
	}
	if !hasService || !hasToken {
		return h, fmt.Errorf("%w: Header.service_id/token", ErrMissingRequired)
	}
	return h, nil
}

// Frame is one complete bgs RPC message.
type Frame struct {
	Header Header
	Body   []byte
}

// ErrPayloadTooLarge means a payload does not fit under MaxPayload.
var ErrPayloadTooLarge = fmt.Errorf("bnet: payload exceeds %d bytes", MaxPayload-1)

// Encode renders a complete frame. Header.Size is set from len(f.Body).
func Encode(f Frame) ([]byte, error) {
	if len(f.Body) >= MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	f.Header.Size = uint32(len(f.Body))
	hdr := encodeHeader(f.Header)
	if len(hdr) > maxHeader {
		return nil, fmt.Errorf("bnet: header of %d bytes does not fit the u16 length prefix", len(hdr))
	}
	out := make([]byte, 0, 2+len(hdr)+len(f.Body))
	out = binary.BigEndian.AppendUint16(out, uint16(len(hdr)))
	out = append(out, hdr...)
	return append(out, f.Body...), nil
}

// Decode parses one frame from the front of b and reports its total encoded
// length. A buffer that ends early yields ErrShortFrame.
func Decode(b []byte) (Frame, int, error) {
	if len(b) < 2 {
		return Frame{}, 0, ErrShortFrame
	}
	hlen := int(binary.BigEndian.Uint16(b[0:2]))
	if len(b) < 2+hlen {
		return Frame{}, 0, ErrShortFrame
	}
	h, err := decodeHeader(b[2 : 2+hlen])
	if err != nil {
		return Frame{}, 0, err
	}
	if h.Size >= MaxPayload {
		return Frame{}, 0, ErrPayloadTooLarge
	}
	total := 2 + hlen + int(h.Size)
	if len(b) < total {
		return Frame{}, 0, ErrShortFrame
	}
	body := make([]byte, h.Size)
	copy(body, b[2+hlen:total])
	return Frame{Header: h, Body: body}, total, nil
}
