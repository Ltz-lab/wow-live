package updateobject

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

func stationaryMovement() *CreateMovement {
	return &CreateMovement{Stationary: &Pos4{X: 1, Y: 2, Z: 3, O: 0.5}}
}

// TestOuterHeaderBytes checks the outer header against bytes assembled by
// hand from the client reader 0x1007fb750.
func TestOuterHeaderBytes(t *testing.T) {
	b := raw{}.
		u8(0x2A).u8(0x00).          // u16 map 42
		u32(0).                     // no blocks
		u8(0b1100_0000).            // bit 7 (Flag7), bit 6 HasRemovedObjects
		u8(0x01).u8(0x00).          // u16 destroy count 1
		u32(2).                     // two GUIDs
		u8(0x01).u8(0x00).u8(0x07). // GUID mask 0x0001, Lo=7
		u8(0x00).u8(0x01).u8(0x09). // GUID mask 0x0100, Hi=9
		u32(0)                      // empty blob
	u, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if u.MapID != 42 || !u.Flag7 || u.Removed == nil {
		t.Fatalf("header %+v", u)
	}
	if len(u.Removed.Destroyed) != 1 || u.Removed.Destroyed[0] != (GUID{Lo: 7}) ||
		len(u.Removed.OutOfRange) != 1 || u.Removed.OutOfRange[0] != (GUID{Hi: 9}) {
		t.Fatalf("removed %+v", u.Removed)
	}
	if got := Encode(u); !bytes.Equal(got, b) {
		t.Fatalf("encode\n got %x\nwant %x", got, []byte(b))
	}
}

func framingVariants() map[string]*UpdateObject {
	create := func(typ BlockType, ot ObjectType, data []byte, frags ...uint8) Block {
		return Block{Type: typ, GUID: GUID{Lo: 0x1234, Hi: 0x0800000000000000}, Create: &CreateBlock{
			ObjectType: ot, Movement: stationaryMovement(), FieldFlags: FlagOwner, Fragments: frags, FragmentData: data,
		}}
	}
	return map[string]*UpdateObject{
		"empty":        {MapID: 0},
		"removed only": {MapID: 1, Flag7: true, Removed: &RemovedObjects{Destroyed: []GUID{{Lo: 1}}, OutOfRange: []GUID{{Lo: 2}, {Hi: 3}}}},
		"destroy only": {MapID: 1, Removed: &RemovedObjects{Destroyed: []GUID{{Lo: 1}}}},
		"values":       {MapID: 2, Blocks: []Block{{Type: BlockValues, GUID: GUID{Lo: 5}, Values: &ValuesBlock{Data: []byte{0, 0, 3, 0, 0, 0, 0}}}}},
		"create1":      {MapID: 3, Blocks: []Block{create(BlockCreateObject, TypeUnit, []byte{9, 9}, FragmentCGObject, FragmentTagUnit)}},
		"create2":      {MapID: 3, Blocks: []Block{create(BlockCreateObject2, TypeGameObject, nil)}},
		"mixed":        {MapID: 4, Flag7: true, Removed: &RemovedObjects{OutOfRange: []GUID{{Lo: 8}}}, Blocks: []Block{create(BlockCreateObject, TypeUnit, []byte{1}, FragmentCGObject), {Type: BlockValues, GUID: GUID{Lo: 6}, Values: &ValuesBlock{}}}},
		"no fragments": {MapID: 5, Blocks: []Block{create(BlockCreateObject2, TypeItem, nil)}},
	}
}

func TestFramingRoundTrip(t *testing.T) {
	for name, u := range framingVariants() {
		t.Run(name, func(t *testing.T) {
			b := Encode(u)
			got, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			if again := Encode(got); !bytes.Equal(again, b) {
				t.Fatalf("re-encode differs\n got %x\nwant %x", again, b)
			}
			if len(got.Blocks) != len(u.Blocks) || got.MapID != u.MapID || got.Flag7 != u.Flag7 || (got.Removed == nil) != (u.Removed == nil) {
				t.Fatalf("decoded %+v", got)
			}
		})
	}
}

// TestFramingMalformed truncates and corrupts every variant: decoding must
// fail cleanly, never panic or over-allocate.
func TestFramingMalformed(t *testing.T) {
	for name, u := range framingVariants() {
		b := Encode(u)
		for n := 0; n < len(b); n++ {
			if _, err := Decode(b[:n]); err == nil {
				t.Errorf("%s: truncated to %d of %d bytes decoded", name, n, len(b))
			}
		}
		if _, err := Decode(append(append([]byte(nil), b...), 0)); err == nil {
			t.Errorf("%s: trailing byte accepted", name)
		}
	}
	cases := map[string][]byte{
		"bad block type": raw{}.u8(0).u8(0).u32(1).u8(0).u32(3).u8(3).u8(0).u8(0),
		"huge count":     raw{}.u8(0).u8(0).u32(0xFFFFFFFF).u8(0).u32(4).u8(0).u8(0).u8(0).u8(0),
		"destroy > all":  raw{}.u8(0).u8(0).u32(0).u8(0x40).u8(2).u8(0).u32(1).u8(0).u8(0).u32(0),
		"huge guid list": raw{}.u8(0).u8(0).u32(0).u8(0x40).u8(0).u8(0).u32(0x7FFFFFFF),
		"huge blob":      raw{}.u8(0).u8(0).u32(0).u8(0).u32(0xFFFFFFF0),
		"short create":   raw{}.u8(0).u8(0).u32(1).u8(0).u32(4).u8(1).u8(0).u8(0).u8(5),
		"no fragment end": func() []byte {
			blk := codec.NewWriter()
			blk.Uint8(1)
			blk.GUID(GUID{Lo: 1})
			blk.Uint8(uint8(TypeUnit))
			EncodeCreateMovement(blk, &CreateMovement{})
			blk.Uint32(2)
			blk.Uint8(0)
			blk.Uint8(FragmentCGObject) // no 255
			w := codec.NewWriter()
			w.Uint16(0)
			w.Uint32(1)
			w.Uint8(0)
			w.Uint32(uint32(blk.Len()))
			w.RawBytes(blk.Bytes())
			return w.Bytes()
		}(),
	}
	for name, b := range cases {
		_, err := Decode(b)
		if err == nil {
			t.Errorf("%s: decoded", name)
		}
		if errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: malformed input reported as unsupported: %v", name, err)
		}
	}
	if _, err := Decode(cases["bad block type"]); !errors.Is(err, ErrMalformed) {
		t.Errorf("bad block type: %v, want ErrMalformed", err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, u := range framingVariants() {
		f.Add(Encode(u))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		u, err := Decode(b)
		if err != nil {
			return
		}
		// Whatever decodes must re-encode to something that decodes (the
		// bytes may differ: padding bits and zero GUID bytes are not kept).
		if _, err := Decode(Encode(u)); err != nil {
			t.Fatalf("re-encoded message does not decode: %v", err)
		}
		for _, blk := range u.Blocks {
			if blk.Create != nil {
				_, _ = blk.Create.DecodeFields()
			}
			if blk.Values != nil {
				_, _ = DecodeValues(TypeActivePlayer, blk.Values)
			}
		}
	})
}
