package bnet

import "context"

import (
	"fmt"
	"math"
)

// Messages. Field numbers are the embedded descriptors' (tools/pbdesc.py);
// presence is explicit where proto2 distinguishes it and a zero value is a
// legal, meaningful value.

// SemanticVersion is bgs.protocol.SemanticVersion (semantic_version.proto).
type SemanticVersion struct {
	Major, Minor, Patch uint32 // fields 1-3
	VersionString       string // field 4
}

func (v SemanticVersion) encode() []byte {
	var w pbWriter
	w.varint(1, uint64(v.Major))
	w.varint(2, uint64(v.Minor))
	w.varint(3, uint64(v.Patch))
	if v.VersionString != "" {
		w.str(4, v.VersionString)
	}
	return w.b
}

// ClientSdkVersionInfo is bgs.protocol.connection.v1.ClientSdkVersionInfo.
type ClientSdkVersionInfo struct {
	SDKName         string          // field 1
	SDKVersion      SemanticVersion // field 2
	ProtocolVersion SemanticVersion // field 3
}

func (c ClientSdkVersionInfo) encode() []byte {
	var w pbWriter
	w.str(1, c.SDKName)
	w.bytes(2, c.SDKVersion.encode())
	w.bytes(3, c.ProtocolVersion.encode())
	return w.b
}

// BoundService is bgs.protocol.connection.v1.BoundService (both required).
type BoundService struct {
	Hash uint32 // field 1, fixed32
	ID   uint32 // field 2
}

func (s BoundService) encode() []byte {
	var w pbWriter
	w.fixed32(1, s.Hash)
	w.varint(2, uint64(s.ID))
	return w.b
}

// BindRequest is bgs.protocol.connection.v1.BindRequest. ConnectionService.Bind
// is marked obsolete in its method options (BGSMethodOptions field 10), and
// the shipped client connects bindless, so nothing here is exercised by it;
// the message is modelled from the descriptor only.
type BindRequest struct {
	DeprecatedImportedHashes []uint32       // field 1, fixed32
	DeprecatedExported       []BoundService // field 2
	Exported                 []BoundService // field 3
	Imported                 []BoundService // field 4
}

func (b BindRequest) encode() []byte {
	var w pbWriter
	for _, h := range b.DeprecatedImportedHashes {
		w.fixed32(1, h)
	}
	for _, s := range b.DeprecatedExported {
		w.bytes(2, s.encode())
	}
	for _, s := range b.Exported {
		w.bytes(3, s.encode())
	}
	for _, s := range b.Imported {
		w.bytes(4, s.encode())
	}
	return w.b
}

// EncodeBindRequest serializes a BindRequest.
func EncodeBindRequest(b BindRequest) []byte { return b.encode() }

// BindResponse is bgs.protocol.connection.v1.BindResponse.
type BindResponse struct {
	ImportedServiceIDs []uint32 // field 1
}

// DecodeBindResponse parses a BindResponse.
func DecodeBindResponse(b []byte) (BindResponse, error) {
	fs, err := pbParse(b)
	if err != nil {
		return BindResponse{}, err
	}
	var r BindResponse
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "BindResponse.imported_service_id"); err != nil {
				return r, err
			}
			r.ImportedServiceIDs = append(r.ImportedServiceIDs, uint32(f.V))
		}
	}
	return r, nil
}

// METERING_LEVEL_CATEGORIZED, connection_service.proto MeteringLevel.
const MeteringCategorized = 1

// ConnectRequest is bgs.protocol.connection.v1.ConnectRequest.
type ConnectRequest struct {
	ClientID       *ProcessID            // field 1
	BindRequest    *BindRequest          // field 2
	UseBindlessRPC bool                  // field 3
	MeteringLevel  uint32                // field 4 (0 is omitted)
	ClientSDK      *ClientSdkVersionInfo // field 5
}

// DefaultConnectRequest is the request the client builds in OnConnect
// (0x1022c0ae4, bgs_low.cpp): client_sdk_info with sdk_name "BGS C++ SDK"
// (0x1022b4c54), sdk_version 11.5.2 / "11.5.2" (0x1022b4c64; major, minor from
// the pair at 0x103138970 = (11, 5), patch 2) and protocol_version 3.33.19 /
// "3.33.19-2ddb4a936f" (0x1022b4bc8; (3, 33) at 0x1031d3820, patch 0x13), and
// two further single-field flags both stored as 1: use_bindless_rpc and
// metering_level = CATEGORIZED. client_id and bind_request are not set.
func DefaultConnectRequest() ConnectRequest {
	return ConnectRequest{
		UseBindlessRPC: true,
		MeteringLevel:  MeteringCategorized,
		ClientSDK: &ClientSdkVersionInfo{
			SDKName:         "BGS C++ SDK",
			SDKVersion:      SemanticVersion{Major: 11, Minor: 5, Patch: 2, VersionString: "11.5.2"},
			ProtocolVersion: SemanticVersion{Major: 3, Minor: 33, Patch: 19, VersionString: "3.33.19-2ddb4a936f"},
		},
	}
}

// Encode serializes the request.
func (r ConnectRequest) Encode() []byte {
	var w pbWriter
	if r.ClientID != nil {
		w.bytes(1, r.ClientID.encode())
	}
	if r.BindRequest != nil {
		w.bytes(2, r.BindRequest.encode())
	}
	if r.UseBindlessRPC {
		w.boolean(3, true)
	}
	if r.MeteringLevel != 0 {
		w.varint(4, uint64(r.MeteringLevel))
	}
	if r.ClientSDK != nil {
		w.bytes(5, r.ClientSDK.encode())
	}
	return w.b
}

// ConnectResponse is bgs.protocol.connection.v1.ConnectResponse. The content
// handle arrays (fields 5 and 8) are skipped.
type ConnectResponse struct {
	ServerID        ProcessID     // field 1, required
	ClientID        *ProcessID    // field 2
	BindResult      uint32        // field 3
	BindResponse    *BindResponse // field 4
	ServerTime      uint64        // field 6
	UseBindlessRPC  bool          // field 7
	CIID            string        // field 9, the client instance id
	ConnectedRegion uint32        // field 10
}

// DecodeConnectResponse parses a ConnectResponse.
func DecodeConnectResponse(b []byte) (ConnectResponse, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ConnectResponse{}, err
	}
	var r ConnectResponse
	var hasServer bool
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "ConnectResponse.server_id"); err != nil {
				return r, err
			}
			if r.ServerID, err = decodeProcessID(f.B); err != nil {
				return r, err
			}
			hasServer = true
		case 2:
			if err := f.want(wireBytes, "ConnectResponse.client_id"); err != nil {
				return r, err
			}
			p, err := decodeProcessID(f.B)
			if err != nil {
				return r, err
			}
			r.ClientID = &p
		case 3:
			if err := f.want(wireVarint, "ConnectResponse.bind_result"); err != nil {
				return r, err
			}
			r.BindResult = uint32(f.V)
		case 4:
			if err := f.want(wireBytes, "ConnectResponse.bind_response"); err != nil {
				return r, err
			}
			br, err := DecodeBindResponse(f.B)
			if err != nil {
				return r, err
			}
			r.BindResponse = &br
		case 6:
			if err := f.want(wireVarint, "ConnectResponse.server_time"); err != nil {
				return r, err
			}
			r.ServerTime = f.V
		case 7:
			if err := f.want(wireVarint, "ConnectResponse.use_bindless_rpc"); err != nil {
				return r, err
			}
			r.UseBindlessRPC = f.V != 0
		case 9:
			if err := f.want(wireBytes, "ConnectResponse.ciid"); err != nil {
				return r, err
			}
			r.CIID = string(f.B)
		case 10:
			if err := f.want(wireVarint, "ConnectResponse.connected_region"); err != nil {
				return r, err
			}
			r.ConnectedRegion = uint32(f.V)
		}
	}
	if !hasServer {
		return r, fmt.Errorf("%w: ConnectResponse.server_id", ErrMissingRequired)
	}
	return r, nil
}

// EncodeConnectResponse is the server half, used by tests and replay peers.
func EncodeConnectResponse(r ConnectResponse) []byte {
	var w pbWriter
	w.bytes(1, r.ServerID.encode())
	if r.ClientID != nil {
		w.bytes(2, r.ClientID.encode())
	}
	if r.BindResult != 0 {
		w.varint(3, uint64(r.BindResult))
	}
	if r.BindResponse != nil {
		var b pbWriter
		for _, id := range r.BindResponse.ImportedServiceIDs {
			b.varint(1, uint64(id))
		}
		w.bytes(4, b.b)
	}
	if r.ServerTime != 0 {
		w.varint(6, r.ServerTime)
	}
	if r.UseBindlessRPC {
		w.boolean(7, true)
	}
	if r.CIID != "" {
		w.str(9, r.CIID)
	}
	if r.ConnectedRegion != 0 {
		w.varint(10, uint64(r.ConnectedRegion))
	}
	return w.b
}

// DecodeConnectRequest parses a ConnectRequest; the server half for tests.
func DecodeConnectRequest(b []byte) (ConnectRequest, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ConnectRequest{}, err
	}
	var r ConnectRequest
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "ConnectRequest.client_id"); err != nil {
				return r, err
			}
			p, err := decodeProcessID(f.B)
			if err != nil {
				return r, err
			}
			r.ClientID = &p
		case 3:
			if err := f.want(wireVarint, "ConnectRequest.use_bindless_rpc"); err != nil {
				return r, err
			}
			r.UseBindlessRPC = f.V != 0
		case 4:
			if err := f.want(wireVarint, "ConnectRequest.metering_level"); err != nil {
				return r, err
			}
			r.MeteringLevel = uint32(f.V)
		case 5:
			if err := f.want(wireBytes, "ConnectRequest.client_sdk_info"); err != nil {
				return r, err
			}
			sdk, err := decodeSdkInfo(f.B)
			if err != nil {
				return r, err
			}
			r.ClientSDK = &sdk
		}
	}
	return r, nil
}

func decodeSemver(b []byte) (SemanticVersion, error) {
	fs, err := pbParse(b)
	if err != nil {
		return SemanticVersion{}, err
	}
	var v SemanticVersion
	for _, f := range fs {
		switch f.Num {
		case 1, 2, 3:
			if err := f.want(wireVarint, "SemanticVersion"); err != nil {
				return v, err
			}
			switch f.Num {
			case 1:
				v.Major = uint32(f.V)
			case 2:
				v.Minor = uint32(f.V)
			default:
				v.Patch = uint32(f.V)
			}
		case 4:
			if err := f.want(wireBytes, "SemanticVersion.version_string"); err != nil {
				return v, err
			}
			v.VersionString = string(f.B)
		}
	}
	return v, nil
}

func decodeSdkInfo(b []byte) (ClientSdkVersionInfo, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ClientSdkVersionInfo{}, err
	}
	var c ClientSdkVersionInfo
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "ClientSdkVersionInfo.sdk_name"); err != nil {
				return c, err
			}
			c.SDKName = string(f.B)
		case 2, 3:
			if err := f.want(wireBytes, "ClientSdkVersionInfo.version"); err != nil {
				return c, err
			}
			v, err := decodeSemver(f.B)
			if err != nil {
				return c, err
			}
			if f.Num == 2 {
				c.SDKVersion = v
			} else {
				c.ProtocolVersion = v
			}
		}
	}
	return c, nil
}

// ---- GameUtilities / bgs.protocol.v2 attributes --------------------------

// VariantKind selects the member of the bgs.protocol.v2.Variant oneof
// (attribute_types.proto, common/v2).
type VariantKind int

const (
	VariantNone   VariantKind = iota // an empty Variant message
	VariantBool                      // bool_value = 1
	VariantInt                       // int_value = 2
	VariantFloat                     // float_value = 3
	VariantString                    // string_value = 4
	VariantBlob                      // blob_value = 5
	VariantUint                      // uint_value = 6
)

// Variant is bgs.protocol.v2.Variant. The client's own parameter bag uses the
// same numbering: its type codes 4 (string), 5 (blob) and 6 (uint) are what
// 0x10139f648, 0x10139f734 and 0x10139f594 set.
type Variant struct {
	Kind   VariantKind
	Bool   bool
	Int    int64
	Float  float64
	String string
	Blob   []byte
	Uint   uint64
}

func (v Variant) encode() []byte {
	var w pbWriter
	switch v.Kind {
	case VariantBool:
		w.boolean(1, v.Bool)
	case VariantInt:
		w.varint(2, uint64(v.Int))
	case VariantFloat:
		w.double(3, v.Float)
	case VariantString:
		w.str(4, v.String)
	case VariantBlob:
		w.bytes(5, v.Blob)
	case VariantUint:
		w.varint(6, v.Uint)
	}
	return w.b
}

func decodeVariant(b []byte) (Variant, error) {
	fs, err := pbParse(b)
	if err != nil {
		return Variant{}, err
	}
	var v Variant
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireVarint, "Variant.bool_value"); err != nil {
				return v, err
			}
			v = Variant{Kind: VariantBool, Bool: f.V != 0}
		case 2:
			if err := f.want(wireVarint, "Variant.int_value"); err != nil {
				return v, err
			}
			v = Variant{Kind: VariantInt, Int: int64(f.V)}
		case 3:
			if err := f.want(wireFixed64, "Variant.float_value"); err != nil {
				return v, err
			}
			v = Variant{Kind: VariantFloat, Float: math.Float64frombits(f.V)}
		case 4:
			if err := f.want(wireBytes, "Variant.string_value"); err != nil {
				return v, err
			}
			v = Variant{Kind: VariantString, String: string(f.B)}
		case 5:
			if err := f.want(wireBytes, "Variant.blob_value"); err != nil {
				return v, err
			}
			v = Variant{Kind: VariantBlob, Blob: append([]byte(nil), f.B...)}
		case 6:
			if err := f.want(wireVarint, "Variant.uint_value"); err != nil {
				return v, err
			}
			v = Variant{Kind: VariantUint, Uint: f.V}
		}
	}
	return v, nil
}

// Attribute is bgs.protocol.v2.Attribute. A name-only attribute still carries
// an (empty) Variant, as the client's bag does (0x10139efb0 allocates the
// value on creation), so Value is always encoded.
type Attribute struct {
	Name  string  // field 1
	Value Variant // field 2
}

func (a Attribute) encode() []byte {
	var w pbWriter
	w.str(1, a.Name)
	w.bytes(2, a.Value.encode())
	return w.b
}

func decodeAttribute(b []byte) (Attribute, error) {
	fs, err := pbParse(b)
	if err != nil {
		return Attribute{}, err
	}
	var a Attribute
	for _, f := range fs {
		switch f.Num {
		case 1:
			if err := f.want(wireBytes, "Attribute.name"); err != nil {
				return a, err
			}
			a.Name = string(f.B)
		case 2:
			if err := f.want(wireBytes, "Attribute.value"); err != nil {
				return a, err
			}
			if a.Value, err = decodeVariant(f.B); err != nil {
				return a, err
			}
		}
	}
	return a, nil
}

// ProcessTaskRequest is GameUtilitiesService.ProcessTask's request
// (game_utilities_service.proto): repeated attribute = 1, repeated payload = 2.
type ProcessTaskRequest struct {
	Attributes []Attribute
	Payload    []Attribute
}

// Encode serializes the request.
func (r ProcessTaskRequest) Encode() []byte {
	var w pbWriter
	for _, a := range r.Attributes {
		w.bytes(1, a.encode())
	}
	for _, a := range r.Payload {
		w.bytes(2, a.encode())
	}
	return w.b
}

// DecodeProcessTaskRequest parses a request; the server half for tests.
func DecodeProcessTaskRequest(b []byte) (ProcessTaskRequest, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ProcessTaskRequest{}, err
	}
	var r ProcessTaskRequest
	for _, f := range fs {
		if f.Num != 1 && f.Num != 2 {
			continue
		}
		if err := f.want(wireBytes, "ProcessTaskRequest"); err != nil {
			return r, err
		}
		a, err := decodeAttribute(f.B)
		if err != nil {
			return r, err
		}
		if f.Num == 1 {
			r.Attributes = append(r.Attributes, a)
		} else {
			r.Payload = append(r.Payload, a)
		}
	}
	return r, nil
}

// ProcessTaskResponse is the response: repeated result = 1.
type ProcessTaskResponse struct {
	Results []Attribute
}

// Encode serializes the response; the server half for tests.
func (r ProcessTaskResponse) Encode() []byte {
	var w pbWriter
	for _, a := range r.Results {
		w.bytes(1, a.encode())
	}
	return w.b
}

// DecodeProcessTaskResponse parses a response.
func DecodeProcessTaskResponse(b []byte) (ProcessTaskResponse, error) {
	fs, err := pbParse(b)
	if err != nil {
		return ProcessTaskResponse{}, err
	}
	var r ProcessTaskResponse
	for _, f := range fs {
		if f.Num != 1 {
			continue
		}
		if err := f.want(wireBytes, "ProcessTaskResponse.result"); err != nil {
			return r, err
		}
		a, err := decodeAttribute(f.B)
		if err != nil {
			return r, err
		}
		r.Results = append(r.Results, a)
	}
	return r, nil
}

// Lookup returns the attribute with the given name. The client's own lookup
// (0x10139ee2c) returns nothing when the name occurs more than once, so a
// duplicate is treated as absent here too.
func Lookup(attrs []Attribute, name string) (Attribute, bool) {
	var found Attribute
	n := 0
	for _, a := range attrs {
		if a.Name == name {
			found = a
			n++
		}
	}
	return found, n == 1
}

// GetAllValuesForAttributeRequest is GameUtilities.GetAllValuesForAttribute:
// the attribute key (field 1, a string).
type GetAllValuesForAttributeRequest struct {
	AttributeKey string // 1
}

// Encode serializes the request.
func (r GetAllValuesForAttributeRequest) Encode() []byte {
	var w pbWriter
	if r.AttributeKey != "" {
		w.str(1, r.AttributeKey)
	}
	return w.b
}

// GetAllValuesForAttributeResponse is repeated attribute_value = 1.
type GetAllValuesForAttributeResponse struct {
	Values []Variant
}

// Encode serializes the response; the server half for tests.
func (r GetAllValuesForAttributeResponse) Encode() []byte {
	var w pbWriter
	for _, v := range r.Values {
		w.bytes(1, v.encode())
	}
	return w.b
}

// DecodeGetAllValuesForAttributeResponse parses a response.
func DecodeGetAllValuesForAttributeResponse(b []byte) (GetAllValuesForAttributeResponse, error) {
	fs, err := pbParse(b)
	if err != nil {
		return GetAllValuesForAttributeResponse{}, err
	}
	var r GetAllValuesForAttributeResponse
	for _, f := range fs {
		if f.Num != 1 {
			continue
		}
		if err := f.want(wireBytes, "GetAllValuesForAttributeResponse.attribute_value"); err != nil {
			return r, err
		}
		v, err := decodeVariant(f.B)
		if err != nil {
			return r, err
		}
		r.Values = append(r.Values, v)
	}
	return r, nil
}

// The v1 GameUtilities service is what the TrinityCore fork (and the bnet
// server side generally) registers: its hash is FNV-1a of
// "bgs.protocol.game_utilities.v1.GameUtilitiesService", and its Variant oneof
// numbers every kind two higher than v2 (string 5, blob 6, uint 9).
// Attribute names/values keep fields 1 and 2.
const (
	GameUtilitiesV1Hash = 0x51923A28
	// GameUtilitiesV2Hash is the same service's OriginalHash; its handlers read
	// ProcessTask and keep attribute names untruncated.
	GameUtilitiesV2Hash = 0x3FC1274D
	MethodProcessTaskV1 = 1
	MethodAllValuesV1   = 2
)

// EncodeV1 is Variant.encode with the v1 field numbers.
func (v Variant) EncodeV1() []byte {
	var w pbWriter
	switch v.Kind {
	case VariantBool:
		w.boolean(2, v.Bool)
	case VariantInt:
		w.varint(3, uint64(v.Int))
	case VariantString:
		w.str(5, v.String)
	case VariantBlob:
		w.bytes(6, v.Blob)
	case VariantUint:
		w.varint(9, v.Uint)
	}
	return w.b
}

func (a Attribute) encodeV1() []byte {
	var w pbWriter
	if a.Name != "" {
		w.str(1, a.Name)
	}
	w.bytes(2, a.Value.EncodeV1())
	return w.b
}

// EncodeV1 is ProcessTaskRequest.Encode with v1 variants.
func (r ProcessTaskRequest) EncodeV1() []byte {
	var w pbWriter
	for _, a := range r.Attributes {
		w.bytes(1, a.encodeV1())
	}
	for _, a := range r.Payload {
		w.bytes(2, a.encodeV1())
	}
	return w.b
}

// EncodeV1 is GetAllValuesForAttributeRequest.Encode; the key is field 1 in
// both versions and only the variants differ.
func (r GetAllValuesForAttributeRequest) EncodeV1() []byte { return r.Encode() }

// ServiceV1 returns a Service addressed by the v1 server-side hash.
func ServiceV1(s Service) Service { s.Hash = GameUtilitiesV1Hash; return s }

// ServiceV2 returns a Service addressed by the v2 (OriginalHash) server-side
// hash.
func ServiceV2(s Service) Service { s.Hash = GameUtilitiesV2Hash; return s }

// ProcessTaskV2 sends GameUtilities.ProcessTask with the v2 Variant numbering,
// which is the shape the client's own builders emit.
func (c *Client) ProcessTaskV2(ctx context.Context, req ProcessTaskRequest) (ProcessTaskResponse, error) {
	body, err := c.Call(ctx, ServiceV2(GameUtilities), MethodProcessTask, req.Encode())
	if err != nil {
		return ProcessTaskResponse{}, err
	}
	return DecodeProcessTaskResponse(body)
}
