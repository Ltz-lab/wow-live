package realm

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/Ltz-lab/wow-live/protocol/version"
	"strings"
	"testing"

	"github.com/Ltz-lab/wow-live/auth/bnet"
)

type fakeUtil struct {
	values   []string
	list     []byte
	lastKey  string
	lastAttr []bnet.Attribute
}

func (f *fakeUtil) Call(_ context.Context, svc bnet.Service, method uint32, req []byte) ([]byte, error) {
	if svc != bnet.GameUtilities || method != bnet.MethodGetAllValuesForAttributes {
		return nil, errors.New("unexpected call")
	}
	var vals []bnet.Variant
	for _, v := range f.values {
		vals = append(vals, bnet.Variant{Kind: bnet.VariantString, String: v})
	}
	return bnet.GetAllValuesForAttributeResponse{Values: vals}.Encode(), nil
}

func (f *fakeUtil) ProcessTaskV2(ctx context.Context, req bnet.ProcessTaskRequest) (bnet.ProcessTaskResponse, error) {
	return f.ProcessTask(ctx, req)
}

func (f *fakeUtil) ProcessTask(_ context.Context, req bnet.ProcessTaskRequest) (bnet.ProcessTaskResponse, error) {
	f.lastAttr = req.Attributes
	if f.list == nil {
		return bnet.ProcessTaskResponse{}, nil
	}
	return bnet.ProcessTaskResponse{Results: []bnet.Attribute{
		{Name: ParamRealmList, Value: bnet.Variant{Kind: bnet.VariantBlob, Blob: f.list}},
	}}, nil
}

func TestSubRegions(t *testing.T) {
	f := &fakeUtil{values: []string{"70-1-70", "70-1-71"}}
	got, err := SubRegions(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "70-1-70" {
		t.Fatalf("got %v", got)
	}
}

func TestRealmListRoundTrip(t *testing.T) {
	blob, err := EncodeRealmList([]Realm{
		{Address: 0x46010046, Name: "Trinity"},
		{Address: 0x46010047, Name: "Second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeUtil{list: blob}
	got, err := RealmList(context.Background(), f, ListRequest{SubRegion: "70-1-70"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != 0x46010046 || got[0].Name != "Trinity" {
		t.Fatalf("got %+v", got)
	}
	if len(f.lastAttr) != 1 || f.lastAttr[0].Name != CmdRealmList+nameSuffix || f.lastAttr[0].Value.String != "70-1-70" {
		t.Fatalf("request %+v", f.lastAttr)
	}
}

func TestDecodeRealmListDropsDeleting(t *testing.T) {
	raw := `{"updates":[{"wowRealmAddress":7,"update":{"name":"A"},"deleting":false},` +
		`{"wowRealmAddress":8,"deleting":true}]}`
	blob := compressForTest(t, raw)
	got, err := DecodeRealmList(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Address != 7 {
		t.Fatalf("got %+v", got)
	}
}

func TestDecodeRealmListRejectsBadPrefix(t *testing.T) {
	blob := compressRaw(t, `{"updates":[]}`) // no type prefix
	if _, err := DecodeRealmList(blob); !errors.Is(err, ErrNoRealmList) {
		t.Fatalf("err %v", err)
	}
}

func TestRealmListWithoutAttribute(t *testing.T) {
	if _, err := RealmList(context.Background(), &fakeUtil{}, ListRequest{}); !errors.Is(err, ErrNoRealmList) {
		t.Fatalf("err %v", err)
	}
}

func TestClientInfoJSON(t *testing.T) {
	raw, err := ClientInfo{}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var obj struct {
		Info struct {
			Platform     uint32 `json:"platform"`
			Type         uint32 `json:"type"`
			PlatformType uint32 `json:"platformType"`
			ClientArch   uint32 `json:"clientArch"`
			SystemArch   uint32 `json:"systemArch"`
			Secret       []int  `json:"secret"`
			Version      struct {
				Major    uint32 `json:"versionMajor"`
				Minor    uint32 `json:"versionMinor"`
				Revision uint32 `json:"versionRevision"`
				Build    uint32 `json:"versionBuild"`
			} `json:"version"`
			TimeZone    string `json:"timeZone"`
			CurrentTime uint32 `json:"currentTime"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	i := obj.Info
	if i.Platform != PlatformMacARM || i.Type != TypeWoW || i.PlatformType != PlatformTypeMac ||
		i.ClientArch != ArchARM64 || i.SystemArch != ArchARM64 {
		t.Fatalf("identities %+v", i)
	}
	// the default is the installed build (version.Forever(), 70755)
	if i.Version.Build != 70755 || i.Version.Major != 1 || i.Version.Minor != 60 || i.Version.Revision != 1 {
		t.Fatalf("version %+v", i.Version)
	}
	old := version.Build70707
	raw707, err := ClientInfo{Version: &old}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw707), `"versionBuild":70707`) {
		t.Fatalf("explicit 70707 not honoured: %s", raw707)
	}
	if len(i.Secret) != SecretLen {
		t.Fatalf("secret %v: want %d byte values", i.Secret, SecretLen)
	}
	secret := i.Secret
	for _, v := range secret {
		if v < 0 || v > 255 {
			t.Fatalf("secret %v: byte values out of range", i.Secret)
		}
	}
	if i.TimeZone == "" || i.CurrentTime == 0 {
		t.Fatalf("time fields %+v", i)
	}
}

func TestClientInfoSecretLength(t *testing.T) {
	if _, err := (ClientInfo{Secret: make([]byte, 31)}).JSON(); !errors.Is(err, ErrSecret) {
		t.Fatalf("err %v", err)
	}
}

func compressForTest(t *testing.T, payload string) []byte {
	t.Helper()
	blob, err := EncodeRealmList(nil) // reuse the framing code through one empty encode
	if err != nil {
		t.Fatal(err)
	}
	_ = blob
	return compressRaw(t, TypeRealmList+":"+payload)
}

// compressRaw wraps plain bytes in the u32le length + zlib framing.
func compressRaw(t *testing.T, plain string) []byte {
	t.Helper()
	b := append([]byte(plain), 0)
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(b)))
	return append(out, zb.Bytes()...)
}
