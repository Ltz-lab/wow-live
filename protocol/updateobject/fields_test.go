package updateobject

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

func sampleFields() Fields {
	return Fields{
		Object: map[string]any{"EntryID": int32(0), "Scale": float32(1)},
		Unit: map[string]any{
			"DisplayID": int32(49), "Race": uint8(1), "ClassId": uint8(1), "Sex": uint8(0), "DisplayPower": uint8(1),
			"Health": int64(250), "MaxHealth": int64(300), "Level": int32(12), "FactionTemplate": int32(1),
			"Power[0]": int32(40), "MaxPower[0]": int32(100), "Target": GUID{Lo: 0x77, Hi: 0x2000000000000000},
			"PassiveSpells.size": uint32(1), "PassiveSpells.SpellID[0]": int32(668),
		},
		Player: map[string]any{"Name.len": uint64(4), "Name": "Jack", "PlayerFlags": uint32(8)},
	}
}

func TestDecodeFieldsChains(t *testing.T) {
	tests := []struct {
		name  string
		typ   ObjectType
		flags uint8
	}{
		{"unit, no flags", TypeUnit, 0},
		{"unit, empath", TypeUnit, FlagEmpath},
		{"player, other", TypePlayer, 0},
		{"player, party", TypePlayer, FlagPartyMember},
		{"active player, self", TypeActivePlayer, FlagOwner | FlagPartyMember},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sampleFields()
			f.ActivePlayerRaw = []byte{1, 2, 3, 4}
			data, err := BuildFragmentData(tc.typ, tc.flags, f)
			if err != nil {
				t.Fatal(err)
			}
			c := &CreateBlock{ObjectType: tc.typ, FieldFlags: tc.flags, Fragments: []uint8{FragmentCGObject, FragmentTagUnit, FragmentTagPlayer}, FragmentData: data}
			of, err := c.DecodeFields()
			if err != nil {
				t.Fatal(err)
			}
			u := of.Unit
			if u.Health != 250 || u.MaxHealth != 300 || u.Level != 12 || u.ClassID != 1 || u.Race != 1 ||
				u.Power[0] != 40 || u.MaxPower[0] != 100 || u.Target != (GUID{Lo: 0x77, Hi: 0x2000000000000000}) {
				t.Fatalf("unit %+v", u)
			}
			if of.Object.Scale != 1 {
				t.Fatalf("object %+v", of.Object)
			}
			if v, _ := (&fieldSet{list: u.Fields, index: indexOf(u.Fields)}).get("PassiveSpells.SpellID[0]"); v != int32(668) {
				t.Fatalf("unmodelled field lost: %v", v)
			}
			switch tc.typ {
			case TypeUnit:
				if of.Player != nil || of.ActivePlayer != nil {
					t.Fatal("unit decoded player parts")
				}
			case TypePlayer:
				if of.Player.Name != "Jack" || of.ActivePlayer != nil {
					t.Fatalf("player %+v", of.Player)
				}
			case TypeActivePlayer:
				if of.Player.Name != "Jack" || !bytes.Equal(of.ActivePlayer.Raw, []byte{1, 2, 3, 4}) {
					t.Fatalf("active player %+v %+v", of.Player, of.ActivePlayer)
				}
			}
		})
	}
}

func indexOf(fs []Field) map[string]int {
	m := map[string]int{}
	for i, f := range fs {
		m[f.Name] = i
	}
	return m
}

func TestDecodeFieldsRefuses(t *testing.T) {
	data, err := BuildFragmentData(TypeUnit, 0, sampleFields())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		c    CreateBlock
		want error
	}{
		{"unknown fragment", CreateBlock{ObjectType: TypeUnit, Fragments: []uint8{FragmentCGObject, 20}, FragmentData: data}, ErrUnsupported},
		{"corpse fields", CreateBlock{ObjectType: TypeCorpse, Fragments: []uint8{FragmentCGObject}, FragmentData: []byte{1, 0}}, ErrUnsupported},
		{"trailing bytes", CreateBlock{ObjectType: TypeUnit, Fragments: []uint8{FragmentCGObject}, FragmentData: append(append([]byte(nil), data...), 0)}, ErrMalformed},
		{"truncated", CreateBlock{ObjectType: TypeUnit, Fragments: []uint8{FragmentCGObject}, FragmentData: data[:len(data)-1]}, codec.ErrUnderflow},
		{"data without CGObject", CreateBlock{ObjectType: TypeUnit, Fragments: []uint8{FragmentTagUnit}, FragmentData: []byte{1}}, ErrMalformed},
	}
	for _, tc := range tests {
		if _, err := tc.c.DecodeFields(); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	inactive := CreateBlock{ObjectType: TypeUnit, Fragments: []uint8{FragmentCGObject}, FragmentData: []byte{0}}
	if of, err := inactive.DecodeFields(); err != nil || of.Unit != nil {
		t.Errorf("inactive CGObject: %+v, %v", of, err)
	}
}

func TestDecodeValues(t *testing.T) {
	data, err := BuildValues(nil, map[string]any{"Health": int64(100), "MaxPower[0]": int32(120), "Target": GUID{Lo: 5}}, map[string]any{"PlayerFlags": uint32(2)})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []ObjectType{TypeActivePlayer, TypePlayer} {
		d, err := DecodeValues(typ, &ValuesBlock{Data: data})
		if err != nil {
			t.Fatal(err)
		}
		u := &UnitData{}
		u.ApplyTo(d.Unit)
		if u.Health != 100 || u.MaxPower[0] != 120 || u.Target != (GUID{Lo: 5}) || d.Object != nil {
			t.Fatalf("%d: unit %+v", typ, u)
		}
		if len(d.Player) != 1 || d.Player[0].Value != uint32(2) {
			t.Fatalf("%d: player %+v", typ, d.Player)
		}
	}
	// A unit has no PlayerData: the client ignores bit 6, so the player
	// delta's bytes are left over.
	if _, err := DecodeValues(TypeUnit, &ValuesBlock{Data: data}); !errors.Is(err, ErrMalformed) {
		t.Errorf("unit with player delta: %v", err)
	}
}

func TestDecodeValuesRefuses(t *testing.T) {
	w := codec.NewWriter()
	w.Uint8(0)
	w.Uint8(0)
	w.Uint8(3)
	w.Uint32(1 << 7) // ActivePlayerData
	ap := w.Bytes()

	w = codec.NewWriter()
	w.Uint8(0)
	w.Uint8(0)
	w.Uint8(3)
	w.Uint32(1 << 6)
	_ = w.Bits(1, 11)      // PlayerData: block 0 present
	_ = w.Bits(1|1<<3, 32) // group 0, Customizations (a list, not decoded)
	list := w.Bytes()

	tests := []struct {
		name string
		typ  ObjectType
		data []byte
		want error
	}{
		{"list bit", TypePlayer, list, ErrUnsupported},
		{"partial fragment ids", TypeUnit, []byte{0, 1, 1}, ErrUnsupported},
		{"deactivate", TypeUnit, []byte{0, 0, 0}, ErrUnsupported},
		{"corpse", TypeCorpse, []byte{0, 0, 3, 1, 0, 0, 0}, ErrUnsupported},
		{"short", TypeUnit, []byte{0, 0}, ErrMalformed},
	}
	for _, tc := range tests {
		if _, err := DecodeValues(tc.typ, &ValuesBlock{Data: tc.data}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// The active player's ActivePlayerData delta ends the block and is kept
	// raw.
	apRaw := append(append([]byte{}, ap...), 0xAA, 0xBB)
	if d, err := DecodeValues(TypeActivePlayer, &ValuesBlock{Data: apRaw}); err != nil || string(d.ActivePlayerRaw) != "\xaa\xbb" {
		t.Errorf("active player delta: %+v, %v", d, err)
	}
	// For a non-active player bit 7 carries no data: the client ignores it.
	if _, err := DecodeValues(TypePlayer, &ValuesBlock{Data: ap}); err != nil {
		t.Errorf("player with bit 7: %v", err)
	}
}
