// Package realm runs the two GameUtilities commands that turn a Battle.net
// logon into a world-server join: Command_RealmListTicketRequest_v1, which
// yields Param_RealmListTicket, and Command_RealmJoinRequest_v1, which yields
// the join ticket, the server addresses and the join secret.
//
// Both ride GameUtilitiesService.ProcessTask as a bag of named v2 attributes.
// What is decoded from the client (docs/EVIDENCE.md E26) and what is not:
//
// Decoded:
//   - the command and parameter names, and the Variant kind of each: the
//     client's bag setters 0x10139f648 / 0x10139f594 / 0x10139f734 write kinds
//     4 (string), 6 (uint) and 5 (blob), the numbering of bgs.protocol.v2.Variant;
//   - the request builders 0x1014333f4 (ticket) and 0x101434b44 (join) and the
//     response readers 0x1014339d8 (ticket) and 0x101434e10 (join), including
//     their error codes;
//   - the blob framings: "<TypeName>:<json>\0" for Param_Identity and
//     Param_ClientInfo (0x10139f824), and u32le length + zlib of
//     "<TypeName>:<json>\0" for Param_ServerAddresses (0x1014af4ac, limit
//     0x40000), with Param_JoinSecret exactly 32 bytes (0x101434f28).
//
// Modelled but not settled by the binary (each is a typed value or error, never
// a silent guess):
//   - the JSON member names and nesting inside the blobs beyond the names in
//     the image's string inventory (Identity, ClientInfo, ServerAddresses);
//   - where the 64-byte world KeyData comes from. The join-info struct built at
//     0x101434e10 is JoinSecret (32 bytes) followed by 32 bytes read from the
//     Aurora context at +0x48, whose origin is not resolved; see JoinResult.KeyData;
//   - that the world AUTH_SESSION ticket string is the RealmJoinTicket.
package realm

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Ltz-lab/wow-live/auth/bnet"
	"github.com/Ltz-lab/wow-live/world"
)

// Command and parameter names, as the client registers them
// (strings at 0x1030128a8..0x103012ac7; referenced from the builders named in
// the package comment).
const (
	CmdRealmListTicket = "Command_RealmListTicketRequest_v1"
	CmdRealmJoin       = "Command_RealmJoinRequest_v1"

	ParamIdentity        = "Param_Identity"
	ParamClientInfo      = "Param_ClientInfo"
	ParamRealmListTicket = "Param_RealmListTicket"
	ParamRealmAddress    = "Param_RealmAddress"
	ParamBnetSessionKey  = "Param_BnetSessionKey"
	ParamRealmJoinTicket = "Param_RealmJoinTicket"
	ParamServerAddresses = "Param_ServerAddresses"
	ParamJoinSecret      = "Param_JoinSecret"
)

// JSON blob type names, from the image's string inventory
// (JSONRealmListTicketIdentity, JSONRealmListTicketClientInformation,
// JSONRealmListServerIPAddresses). The blob codecs compare the text before the
// first ':' with the type's own name (0x1014af4ac, 0x10139f824).
const (
	TypeIdentity        = "JSONRealmListTicketIdentity"
	TypeClientInfo      = "JSONRealmListTicketClientInformation"
	TypeServerAddresses = "JSONRealmListServerIPAddresses"
)

// JoinSecretLen is the exact length of Param_JoinSecret (0x101434f28 rejects
// anything else with 0x80000130).
const JoinSecretLen = 32

// MaxServerAddresses is the cap on the decompressed address JSON (the
// `0x40000` argument at 0x10139ef28).
const MaxServerAddresses = 0x40000

// Errors. The numeric codes are the ones the client's readers report.
var (
	ErrNotConnected    = errors.New("realm: no connection (client 0x80000149)")
	ErrNoTicket        = errors.New("realm: response has no Param_RealmListTicket blob (client 0x8000012d)")
	ErrNoJoinTicket    = errors.New("realm: response has no Param_RealmJoinTicket blob (client 0x8000012e)")
	ErrNoAddresses     = errors.New("realm: response has no valid Param_ServerAddresses (client 0x8000012f)")
	ErrBadJoinSecret   = errors.New("realm: Param_JoinSecret is missing or not 32 bytes (client 0x80000130)")
	ErrBadBlob         = errors.New("realm: malformed JSON blob")
	ErrKeyHalfRequired = errors.New("realm: the second 32 bytes of the world KeyData come from client state the binary does not resolve (Aurora context +0x48 at 0x101434e10); supply them explicitly")
)

// Caller is what the realm commands need from the bgs client;
// *bnet.Client satisfies it.
type Caller interface {
	ProcessTask(ctx context.Context, req bnet.ProcessTaskRequest) (bnet.ProcessTaskResponse, error)
	// ProcessTaskV2 is ProcessTask addressed by the v2 (OriginalHash) service
	// hash, whose handlers keep attribute names untruncated.
	ProcessTaskV2(ctx context.Context, req bnet.ProcessTaskRequest) (bnet.ProcessTaskResponse, error)
}

// JSONBlob renders a Param_Identity / Param_ClientInfo style blob:
// TypeName ':' json NUL (0x10139f824 writes the type name, a colon, the JSON
// and a terminating NUL byte, and stores all of it as the blob).
func JSONBlob(typeName string, v any) ([]byte, error) {
	js, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonBlobRaw(typeName, js), nil
}

func jsonBlobRaw(typeName string, js []byte) []byte {
	out := make([]byte, 0, len(typeName)+1+len(js)+1)
	out = append(out, typeName...)
	out = append(out, ':')
	out = append(out, js...)
	return append(out, 0)
}

// Identity is the Param_Identity object: the game account the ticket is for.
// The member names gameAccountID and gameAccountRegion are in the image's
// string inventory next to the realm JSON types; the object's two members at
// 0x1014333f4 are a u64 and a byte, matching a 64-bit account id and a region.
type Identity struct {
	GameAccountID     uint64 `json:"gameAccountID"`
	GameAccountRegion uint32 `json:"gameAccountRegion"`
}

// TicketRequest is everything Command_RealmListTicketRequest_v1 carries.
type TicketRequest struct {
	Identity Identity
	// ClientInfoJSON is the JSON object of JSONRealmListTicketClientInformation.
	// The client fills it from a dozen runtime values (version, platform,
	// currentTime, clientArch, systemVersion, buildVariant, timeZone,
	// versionDataBuild, audioLocale, textLocale, platformType, systemArch;
	// 0x1014333f4); how each is typed and nested is not settled, so the caller
	// supplies the object.
	ClientInfoJSON json.RawMessage
	// CommandSuffix is appended as Command_X_<suffix> when non-empty
	// (0x10139efb0 does that with the string from 0x1014e01e0, which is empty
	// in the paths decoded here).
	CommandSuffix string
	// NoNUL drops the NUL that terminates the identity blob (debug knob).
	NoNUL bool
}

// ClientRequestSuffix is the default of the client's auroraClientRequestSuffix
// CVar, which it appends to every Command_* attribute name as "_<suffix>".
// The 70755 default is built by 0x10139e844 from two build flags
// (0x1033ee1cd = 0, 0x1033ee1ce = 1): "camelot1" (retail builds use "b9").
// Live Battle.net matches the full name; the fork truncates a command at its
// last underscore (ParseParamName), so it accepts the same names (EVIDENCE E32).
const ClientRequestSuffix = "camelot1"

// nameSuffix is "_" + ClientRequestSuffix.
const nameSuffix = "_" + ClientRequestSuffix

func commandName(base, suffix string) string {
	if suffix == "" {
		return base + nameSuffix
	}
	return base + "_" + suffix
}

// RealmListTicket runs Command_RealmListTicketRequest_v1 and returns the
// Param_RealmListTicket blob.
//
// Request: the command attribute (value an empty Variant), then
// Param_Identity and Param_ClientInfo as JSON blobs, in ProcessTaskRequest.attribute
// (the client's bag is the request's first repeated field).
func RealmListTicket(ctx context.Context, c Caller, req TicketRequest) ([]byte, error) {
	if len(req.ClientInfoJSON) == 0 {
		return nil, errors.New("realm: ClientInfoJSON is required")
	}
	ident, err := JSONBlob(TypeIdentity, req.Identity)
	if err != nil {
		return nil, err
	}
	if req.NoNUL {
		ident = bytes.TrimSuffix(ident, []byte{0})
	}
	pt := bnet.ProcessTaskRequest{Attributes: []bnet.Attribute{
		{Name: commandName(CmdRealmListTicket, req.CommandSuffix)},
		{Name: ParamIdentity, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: ident}},
		{Name: ParamClientInfo, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: jsonBlobRaw(TypeClientInfo, req.ClientInfoJSON)}},
	}}
	resp, err := c.ProcessTask(ctx, pt)
	if err != nil {
		return nil, err
	}
	a, ok := bnet.Lookup(resp.Results, ParamRealmListTicket)
	if !ok || a.Value.Kind != bnet.VariantBlob {
		return nil, ErrNoTicket
	}
	return a.Value.Blob, nil
}

// JoinRequest is everything Command_RealmJoinRequest_v1 carries.
type JoinRequest struct {
	// SubRegion is the command attribute's string value; the client logs it as
	// "subRegion" when it starts the join (0x1013ad9a8).
	SubRegion string
	// RealmAddress is the realm's virtual address (a uint attribute).
	RealmAddress uint32
	// Ticket is the Param_RealmListTicket blob from RealmListTicket.
	Ticket []byte
	// SessionKey is the LogonRecord.session_key the server sent in
	// LogonCompleteNotification (field 5, bytes; EVIDENCE.md E27); the client
	// forwards it unchanged as Param_BnetSessionKey (0x101434b44).
	SessionKey    []byte
	CommandSuffix string
	// PingData, when set, is sent last as Param_BleepClientPingData, as the
	// stock client does (E37).
	PingData []byte
}

// Address is one server endpoint from Param_ServerAddresses.
type Address struct {
	Family int    `json:"family"`
	IP     string `json:"ip"`
	Port   uint16 `json:"port"`
}

// JoinResult is the decoded Command_RealmJoinRequest_v1 response.
type JoinResult struct {
	// JoinTicket is Param_RealmJoinTicket, the first member of the client's
	// join-info struct (0x101434e10).
	JoinTicket []byte
	// Addresses are the endpoints in Param_ServerAddresses, in server order.
	Addresses []Address
	// JoinSecret is Param_JoinSecret, exactly 32 bytes.
	JoinSecret [JoinSecretLen]byte
}

// RealmJoin runs Command_RealmJoinRequest_v1.
//
// Request attribute order is the builder's (0x101434b44): the command (string
// value), Param_RealmAddress (uint), Param_RealmListTicket (blob),
// Param_BnetSessionKey (blob). Response members are those 0x101434e10 reads,
// with the same failure order and the same limits.
func RealmJoin(ctx context.Context, c Caller, req JoinRequest) (JoinResult, error) {
	pt := bnet.ProcessTaskRequest{Attributes: []bnet.Attribute{
		{Name: commandName(CmdRealmJoin, req.CommandSuffix), Value: bnet.Variant{Kind: bnet.VariantString, String: req.SubRegion}},
		{Name: ParamRealmAddress, Value: bnet.Variant{Kind: bnet.VariantUint, Uint: uint64(req.RealmAddress)}},
		{Name: ParamRealmListTicket, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: req.Ticket}},
		{Name: ParamBnetSessionKey, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: req.SessionKey}},
	}}
	if req.PingData != nil {
		pt.Attributes = append(pt.Attributes, bnet.Attribute{Name: ParamBleepClientPingData, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: req.PingData}})
	}
	resp, err := c.ProcessTask(ctx, pt)
	if err != nil {
		return JoinResult{}, err
	}
	return decodeJoin(resp.Results)
}

func decodeJoin(results []bnet.Attribute) (JoinResult, error) {
	var out JoinResult
	jt, ok := bnet.Lookup(results, ParamRealmJoinTicket)
	if !ok || jt.Value.Kind != bnet.VariantBlob {
		return out, ErrNoJoinTicket
	}
	out.JoinTicket = jt.Value.Blob
	sa, ok := bnet.Lookup(results, ParamServerAddresses)
	if !ok || sa.Value.Kind != bnet.VariantBlob {
		return out, ErrNoAddresses
	}
	addrs, err := DecodeServerAddresses(sa.Value.Blob)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrNoAddresses, err)
	}
	out.Addresses = addrs
	js, ok := bnet.Lookup(results, ParamJoinSecret)
	if !ok || js.Value.Kind != bnet.VariantBlob || len(js.Value.Blob) != JoinSecretLen {
		return out, ErrBadJoinSecret
	}
	copy(out.JoinSecret[:], js.Value.Blob)
	return out, nil
}

// serverAddresses is the JSONRealmListServerIPAddresses object. The member
// names families, family and addresses are in the image's string inventory;
// ip and port are the only strings of that shape in it. Their nesting here is
// the conventional one and is not separately confirmed.
type serverAddresses struct {
	Families []struct {
		Family    int `json:"family"`
		Addresses []struct {
			IP   string `json:"ip"`
			Port uint16 `json:"port"`
		} `json:"addresses"`
	} `json:"families"`
}

// DecodeServerAddresses decodes a Param_ServerAddresses blob: a little-endian
// u32 uncompressed length (at least 3, at most MaxServerAddresses), then a
// zlib stream that inflates to exactly that many bytes ending in NUL, holding
// "JSONRealmListServerIPAddresses:" and the JSON object (0x1014af4ac).
func DecodeServerAddresses(blob []byte) ([]Address, error) {
	if len(blob) < 4 {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadBlob, len(blob))
	}
	n := binary.LittleEndian.Uint32(blob)
	if n > MaxServerAddresses {
		return nil, fmt.Errorf("%w: uncompressed length %d above %d", ErrBadBlob, n, MaxServerAddresses)
	}
	if n < 3 {
		return nil, fmt.Errorf("%w: uncompressed length %d", ErrBadBlob, n)
	}
	zr, err := zlib.NewReader(bytes.NewReader(blob[4:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, int64(n)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	if uint32(len(raw)) != n {
		return nil, fmt.Errorf("%w: inflated %d bytes, header says %d", ErrBadBlob, len(raw), n)
	}
	if raw[n-1] != 0 {
		return nil, fmt.Errorf("%w: not NUL-terminated", ErrBadBlob)
	}
	raw = raw[:n-1]
	i := bytes.IndexByte(raw, ':')
	if i < 0 || string(raw[:i]) != TypeServerAddresses {
		return nil, fmt.Errorf("%w: type prefix is not %q", ErrBadBlob, TypeServerAddresses)
	}
	var sa serverAddresses
	if err := json.Unmarshal(raw[i+1:], &sa); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	var out []Address
	for _, f := range sa.Families {
		for _, a := range f.Addresses {
			out = append(out, Address{Family: f.Family, IP: a.IP, Port: a.Port})
		}
	}
	return out, nil
}

// EncodeServerAddresses is the server half of DecodeServerAddresses, for tests
// and replay peers.
func EncodeServerAddresses(addrs []Address) ([]byte, error) {
	var sa serverAddresses
	byFamily := map[int]int{}
	for _, a := range addrs {
		idx, ok := byFamily[a.Family]
		if !ok {
			sa.Families = append(sa.Families, struct {
				Family    int `json:"family"`
				Addresses []struct {
					IP   string `json:"ip"`
					Port uint16 `json:"port"`
				} `json:"addresses"`
			}{Family: a.Family})
			idx = len(sa.Families) - 1
			byFamily[a.Family] = idx
		}
		sa.Families[idx].Addresses = append(sa.Families[idx].Addresses, struct {
			IP   string `json:"ip"`
			Port uint16 `json:"port"`
		}{a.IP, a.Port})
	}
	js, err := json.Marshal(sa)
	if err != nil {
		return nil, err
	}
	plain := jsonBlobRaw(TypeServerAddresses, js)
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write(plain)
	zw.Close()
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(plain)))
	return append(out, zb.Bytes()...), nil
}

// KeyData assembles the 64-byte world KeyData as the client's join-info struct
// is laid out at 0x101434e10: JoinSecret first, then 32 bytes read from the
// Aurora context at +0x48. The binary does not resolve what those 32 bytes
// are (it does not generate them in the join builder), so the caller supplies
// them; with no second half the call fails rather than invent one.
func (r JoinResult) KeyData(secondHalf []byte) ([]byte, error) {
	if len(secondHalf) == 0 {
		return nil, ErrKeyHalfRequired
	}
	if len(secondHalf) != world.KeyDataLen-JoinSecretLen {
		return nil, fmt.Errorf("realm: second half of KeyData must be %d bytes, got %d", world.KeyDataLen-JoinSecretLen, len(secondHalf))
	}
	out := make([]byte, 0, world.KeyDataLen)
	out = append(out, r.JoinSecret[:]...)
	return append(out, secondHalf...), nil
}

// WorldAuth is what the world connection takes from the Battle.net side.
type WorldAuth struct {
	KeyData []byte // 64 bytes
	Ticket  []byte
}

// WorldAuth returns the auth material for world.Config. Ticket is the
// RealmJoinTicket; that the world AUTH_SESSION ticket string is this value is
// inferred (it is the join-info struct's first member and AUTH_SESSION's only
// string field), not read off a packet.
func (r JoinResult) WorldAuth(secondHalf []byte) (WorldAuth, error) {
	kd, err := r.KeyData(secondHalf)
	if err != nil {
		return WorldAuth{}, err
	}
	return WorldAuth{KeyData: kd, Ticket: append([]byte(nil), r.JoinTicket...)}, nil
}

// Apply fills the auth fields of a world.Config.
func (w WorldAuth) Apply(cfg *world.Config) {
	cfg.KeyData = w.KeyData
	cfg.Ticket = w.Ticket
}
