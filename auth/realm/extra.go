package realm

// The realm-flow calls the stock 70755 client makes that the join itself does
// not need, recorded from the fork's bnetserver with service.protobuf logging
// (EVIDENCE.md E37): after the list ticket it fetches the BLEEP proxy list,
// asks for the last character played (twice: ContentSetIDFilter -1 without
// locality filtering, then 137 with it and its ping data) and the super
// district list, and its realm join carries Param_BleepClientPingData.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"

	"github.com/Ltz-lab/wow-live/auth/bnet"
)

const (
	CmdFetchBleepProxies = "Command_FetchBleepProxiesRequest_v1"
	CmdLastCharPlayed    = "Command_LastCharPlayedRequest_v1"
	CmdSuperDistrictList = "Command_SuperDistrictListRequest_v1"

	ParamBleepClientPingData    = "Param_BleepClientPingData"
	ParamBleepProxyList         = "Param_BleepProxyList"
	ParamContentSetIDFilter     = "Param_ContentSetIDFilter"
	ParamFilterToPreferredLocal = "Param_FilterToPreferredLocality"
	ParamSuperDistrictList      = "Param_SuperDistrictList"
	ParamRealmEntry             = "Param_RealmEntry"

	TypeBleepPingDataList = "JSONBleepPingDataList"
	TypeBleepProxyList    = "JSONBleepProxyList"
	TypeSuperDistrictList = "JSONSuperDistrictList"

	// ContentSetForever is the ContentSetIDFilter of the stock client's
	// second LastCharPlayed request.
	ContentSetForever = 137
)

// EmptyPingData is the stock client's ping blob when it has no proxy to
// ping: JSONBleepPingDataList:{"pingData":[]}.
const EmptyPingData = `{"pingData":[]}`

// DeflateBlob frames json as u32le uncompressed length + zlib("Type:json\0"),
// at the best compression, as the client's 78 DA header shows.
func DeflateBlob(typeName, json string) []byte {
	plain := append([]byte(typeName+":"+json), 0)
	var zb bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&zb, zlib.BestCompression)
	zw.Write(plain)
	zw.Close()
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(plain)))
	return append(out, zb.Bytes()...)
}

// PingDataBlob is Param_BleepClientPingData for json.
func PingDataBlob(json string) []byte { return DeflateBlob(TypeBleepPingDataList, json) }

// FetchBleepProxies runs Command_FetchBleepProxiesRequest_v1 and returns the
// proxy list JSON.
func FetchBleepProxies(ctx context.Context, c Caller) ([]byte, error) {
	resp, err := c.ProcessTask(ctx, bnet.ProcessTaskRequest{Attributes: []bnet.Attribute{
		{Name: commandName(CmdFetchBleepProxies, ""), Value: bnet.Variant{Kind: bnet.VariantNone}},
	}})
	if err != nil {
		return nil, err
	}
	if a, ok := bnet.Lookup(resp.Results, ParamBleepProxyList); ok && a.Value.Kind == bnet.VariantBlob {
		return inflate(a.Value.Blob, TypeBleepProxyList, 1<<20)
	}
	return nil, nil
}

// LastCharPlayedRequest is one Command_LastCharPlayedRequest_v1.
type LastCharPlayedRequest struct {
	SubRegion  string
	Ticket     []byte
	ContentSet int64
	Preferred  bool
	// PingData, when set, is sent as Param_BleepClientPingData.
	PingData []byte
}

// LastCharPlayed runs Command_LastCharPlayedRequest_v1 and returns the raw
// Param_RealmEntry blob, if any.
func LastCharPlayed(ctx context.Context, c Caller, req LastCharPlayedRequest) ([]byte, error) {
	attrs := []bnet.Attribute{
		{Name: commandName(CmdLastCharPlayed, ""), Value: bnet.Variant{Kind: bnet.VariantString, String: req.SubRegion}},
		{Name: ParamRealmListTicket, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: req.Ticket}},
		{Name: ParamContentSetIDFilter, Value: bnet.Variant{Kind: bnet.VariantInt, Int: req.ContentSet}},
		{Name: ParamFilterToPreferredLocal, Value: bnet.Variant{Kind: bnet.VariantBool, Bool: req.Preferred}},
	}
	if req.PingData != nil {
		attrs = append(attrs, bnet.Attribute{Name: ParamBleepClientPingData, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: req.PingData}})
	}
	resp, err := c.ProcessTask(ctx, bnet.ProcessTaskRequest{Attributes: attrs})
	if err != nil {
		return nil, err
	}
	if a, ok := bnet.Lookup(resp.Results, ParamRealmEntry); ok && a.Value.Kind == bnet.VariantBlob {
		return a.Value.Blob, nil
	}
	return nil, nil
}

// SuperDistrictList runs Command_SuperDistrictListRequest_v1 and returns the
// list JSON.
func SuperDistrictList(ctx context.Context, c Caller, subRegion string, ticket []byte) ([]byte, error) {
	resp, err := c.ProcessTask(ctx, bnet.ProcessTaskRequest{Attributes: []bnet.Attribute{
		{Name: commandName(CmdSuperDistrictList, ""), Value: bnet.Variant{Kind: bnet.VariantString, String: subRegion}},
		{Name: ParamRealmListTicket, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: ticket}},
	}})
	if err != nil {
		return nil, err
	}
	if a, ok := bnet.Lookup(resp.Results, ParamSuperDistrictList); ok && a.Value.Kind == bnet.VariantBlob {
		return inflate(a.Value.Blob, TypeSuperDistrictList, 1<<20)
	}
	return nil, nil
}
