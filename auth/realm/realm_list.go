package realm

// The realm list: Command_RealmListRequest_v1 and the sub-region list.
//
// E26 covers the ticket and join commands; the list command sits before them
// and is what supplies the realm address the join needs. Its shape is public
// (fork bnetserver GameUtilitiesService.cpp GetRealmList / Shared::
// HandleGetAllValuesForAttribute):
//
//   - GameUtilities.GetAllValuesForAttribute("Command_RealmListRequest_v1")
//     answers the sub-regions, so the client knows what to ask for;
//   - Command_RealmListRequest_v1 takes the sub-region as its string value and
//     answers Param_RealmList, a u32le length + zlib of
//     "JSONRealmListUpdates:" followed by JSON.RealmList.RealmListUpdates.
//
// The client's own request/response builders for this command are not decoded,
// so this file follows the public side. The JSON is parsed tolerantly (the
// member names are JSON.RealmList's, but only the address and the name are
// used), because a mis-guessed member should not lose the whole list.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Ltz-lab/wow-live/auth/bnet"
)

const (
	// CmdRealmList is the client's list command (fork GetRealmList).
	CmdRealmList = "Command_RealmListRequest_v1"
	// ParamRealmList is its response attribute.
	ParamRealmList = "Param_RealmList"
	// ParamCharacterCountList is the second response attribute; not decoded.
	ParamCharacterCountList = "Param_CharacterCountList"
	// TypeRealmList is the blob type name in front of the JSON.
	TypeRealmList = "JSONRealmListUpdates"

	// MaxRealmList is the cap on the decompressed JSON, the same 0x40000 the
	// client uses for the address blob.
	MaxRealmList = MaxServerAddresses
)

// ErrNoRealmList means the response carried no usable Param_RealmList.
var ErrNoRealmList = errors.New("realm: response has no valid Param_RealmList")

// GameUtilitiesCaller is the generic GameUtilities call, for the commands that
// are not ProcessTask. *bnet.Client satisfies it.
type GameUtilitiesCaller interface {
	Call(ctx context.Context, svc bnet.Service, method uint32, req []byte) ([]byte, error)
}

// SubRegions asks for the sub-region values of the realm list command.
func SubRegions(ctx context.Context, c GameUtilitiesCaller) ([]string, error) {
	req := bnet.GetAllValuesForAttributeRequest{AttributeKey: CmdRealmList + nameSuffix}
	body, err := c.Call(ctx, bnet.GameUtilities, bnet.MethodGetAllValuesForAttributes, req.Encode())
	if err != nil {
		return nil, err
	}
	resp, err := bnet.DecodeGetAllValuesForAttributeResponse(body)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range resp.Values {
		if v.Kind == bnet.VariantString && v.String != "" {
			out = append(out, v.String)
		}
	}
	return out, nil
}

// Realm is one entry of the list.
type Realm struct {
	Address uint32
	Name    string
	// Characters is the game account's character count there
	// (Param_CharacterCountList); 0 when none or not sent.
	Characters int
	// Rest keeps the raw object fields for anything a later step wants.
	Rest map[string]any
}

// ListRequest is one Command_RealmListRequest_v1.
type ListRequest struct {
	SubRegion     string
	CommandSuffix string
	// Ticket is the Param_RealmListTicket from RealmListTicket, and
	// BuildVariant the Param_BuildVariant string: the client's list builder
	// (0x1014346cc) always sends both; live Battle.net refuses the list
	// without them (0x8000012d, EVIDENCE E32). The fork ignores both.
	Ticket       []byte
	BuildVariant string
	// Timeout bounds the call; zero means 20 s.
	Timeout time.Duration
}

// RealmList runs Command_RealmListRequest_v1 and returns the live realms in
// server order.
func RealmList(ctx context.Context, c Caller, req ListRequest) ([]Realm, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	pt := bnet.ProcessTaskRequest{Attributes: []bnet.Attribute{
		{Name: commandName(CmdRealmList, req.CommandSuffix), Value: bnet.Variant{Kind: bnet.VariantString, String: req.SubRegion}},
	}}
	if req.Ticket != nil {
		pt.Attributes = append(pt.Attributes, bnet.Attribute{Name: "Param_RealmListTicket", Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: req.Ticket}})
	}
	if req.BuildVariant != "" {
		pt.Attributes = append(pt.Attributes, bnet.Attribute{Name: "Param_BuildVariant", Value: bnet.Variant{Kind: bnet.VariantString, String: req.BuildVariant}})
	}
	resp, err := c.ProcessTask(ctx, pt)
	if err != nil {
		return nil, err
	}
	a, ok := bnet.Lookup(resp.Results, ParamRealmList)
	if !ok || a.Value.Kind != bnet.VariantBlob {
		return nil, ErrNoRealmList
	}
	realms, err := DecodeRealmList(a.Value.Blob)
	if err != nil {
		return nil, err
	}
	if cc, ok := bnet.Lookup(resp.Results, ParamCharacterCountList); ok && cc.Value.Kind == bnet.VariantBlob {
		if counts, err := DecodeCharacterCounts(cc.Value.Blob); err == nil {
			for i := range realms {
				realms[i].Characters = counts[realms[i].Address]
			}
		}
	}
	return realms, nil
}

// DecodeCharacterCounts decodes Param_CharacterCountList: the number of the
// game account's characters on each realm, keyed by realm address. The client
// uses it to put a player back on the realm their characters live on.
func DecodeCharacterCounts(blob []byte) (map[uint32]int, error) {
	raw, err := inflateAny(blob, MaxRealmList)
	if err != nil {
		return nil, err
	}
	if os.Getenv("WOW_DBG_COUNTS") != "" {
		fmt.Fprintf(os.Stderr, "DBG character counts: %s\n", raw)
	}
	var list struct {
		Counts []struct {
			Address json.RawMessage `json:"wowRealmAddress"`
			Count   int             `json:"count"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	out := map[uint32]int{}
	for _, c := range list.Counts {
		if a, ok := number(c.Address); ok {
			out[a] = c.Count
		}
	}
	return out, nil
}

// inflateAny is inflate without a fixed type name: it strips whatever
// "Type:" prefix the blob carries.
func inflateAny(blob []byte, max int) ([]byte, error) {
	if len(blob) < 4 {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadBlob, len(blob))
	}
	zr, err := zlib.NewReader(bytes.NewReader(blob[4:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	defer zr.Close()
	plain, err := io.ReadAll(io.LimitReader(zr, int64(max)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if i := bytes.IndexByte(plain, ':'); i > 0 && i < 64 && bytes.IndexByte(plain[:i], '{') < 0 {
		plain = plain[i+1:]
	}
	return plain, nil
}

// DecodeRealmList decodes a Param_RealmList blob and drops deleting entries.
func DecodeRealmList(blob []byte) ([]Realm, error) {
	raw, err := inflate(blob, TypeRealmList, MaxRealmList)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRealmList, err)
	}
	// updates: [ { wowRealmAddress, update: {...}, deleting: bool } ]
	var list struct {
		Updates []struct {
			Address  json.RawMessage `json:"wowRealmAddress"`
			Deleting bool            `json:"deleting"`
			Update   map[string]any  `json:"update"`
		} `json:"updates"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRealmList, err)
	}
	var out []Realm
	for _, u := range list.Updates {
		if u.Deleting || u.Update == nil {
			continue
		}
		addr, ok := number(u.Address)
		if !ok || addr == 0 {
			// The address may sit inside update instead, depending on nesting.
			if v, ok := number(rawField(u.Update, "wowRealmAddress")); ok && v != 0 {
				addr = v
			} else {
				continue
			}
		}
		name, _ := u.Update["name"].(string)
		out = append(out, Realm{Address: addr, Name: name, Rest: u.Update})
	}
	return out, nil
}

func rawField(m map[string]any, key string) json.RawMessage {
	if v, ok := m[key]; ok {
		b, _ := json.Marshal(v)
		return b
	}
	return nil
}

// number reads a JSON number, tolerating both a quoted and an unquoted form.
func number(raw json.RawMessage) (uint32, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return uint32(n), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		var v uint32
		if _, err := fmt.Sscanf(s, "%d", &v); err == nil {
			return v, true
		}
	}
	return 0, false
}

// inflate decodes the u32le length + zlib + "Type:" + JSON + NUL blob framing
// (E26). It returns the JSON after the type prefix.
func inflate(blob []byte, typeName string, max int) ([]byte, error) {
	if len(blob) < 4 {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadBlob, len(blob))
	}
	n := binary.LittleEndian.Uint32(blob)
	if n > uint32(max) || n < 3 {
		return nil, fmt.Errorf("%w: uncompressed length %d", ErrBadBlob, n)
	}
	zr, err := zlib.NewReader(bytes.NewReader(blob[4:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	defer zr.Close()
	plain, err := io.ReadAll(io.LimitReader(zr, int64(n)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBlob, err)
	}
	if uint32(len(plain)) != n {
		return nil, fmt.Errorf("%w: inflated %d bytes, header says %d", ErrBadBlob, len(plain), n)
	}
	if plain[n-1] != 0 {
		return nil, fmt.Errorf("%w: not NUL-terminated", ErrBadBlob)
	}
	plain = plain[:n-1]
	prefix := []byte(typeName + ":")
	if !bytes.HasPrefix(plain, prefix) {
		return nil, fmt.Errorf("%w: type prefix is not %q", ErrBadBlob, typeName)
	}
	return plain[len(prefix):], nil
}

// EncodeRealmList is the server half of DecodeRealmList, for tests and replay
// peers.
func EncodeRealmList(realms []Realm) ([]byte, error) {
	type part struct {
		Address  uint32         `json:"wowRealmAddress"`
		Update   map[string]any `json:"update"`
		Deleting bool           `json:"deleting"`
	}
	list := struct {
		Updates []part `json:"updates"`
	}{}
	for _, r := range realms {
		upd := r.Rest
		if upd == nil {
			upd = map[string]any{}
		}
		upd["wowRealmAddress"] = r.Address
		upd["name"] = r.Name
		list.Updates = append(list.Updates, part{Address: r.Address, Update: upd})
	}
	js, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	plain := append([]byte(TypeRealmList+":"), js...)
	plain = append(plain, 0)
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write(plain)
	zw.Close()
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(plain)))
	return append(out, zb.Bytes()...), nil
}
