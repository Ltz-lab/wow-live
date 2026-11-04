package updateobject

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// The client's update-field readers, run in an emulator by tools/ufemu.py
// over bytes this package encodes. The recorded runs live in
// testdata/ufemu/results.json. Regenerate with:
//
//	UFEMU_CASES=/tmp/cases.json go test ./protocol/updateobject -run TestWriteUfemuCases
//	python3 tools/ufemu.py artifacts/WoWForever-arm64.decrypted \
//	    --cases /tmp/cases.json --out protocol/updateobject/testdata/ufemu/results.json

type ufemuCase struct {
	Name   string            `json:"name"`
	Reader string            `json:"reader"`
	Flags  string            `json:"flags"`
	Hex    string            `json:"hex"`
	Delta  bool              `json:"delta,omitempty"`
	Init   map[string]uint64 `json:"init,omitempty"`
}

type ufemuRead struct {
	Pos int  `json:"pos"`
	W   any  `json:"w"`
	N   *int `json:"n,omitempty"`
}

type ufemuResult struct {
	ufemuCase
	Consumed int         `json:"consumed"`
	Total    int         `json:"total"`
	Error    *string     `json:"error"`
	Reads    []ufemuRead `json:"reads"`
}

// layoutCase is one create layout under one flag set and one value filler.
type layoutCase struct {
	name   string
	reader string
	prog   []op
	flags  uint8
	vals   valueSource
}

// filler gives every field a distinct non-zero value, and counts and
// presence bits from over.
func filler(over map[string]any) valueSource {
	n := 0
	return func(name string, w Width) any {
		if v, ok := over[name]; ok {
			return v
		}
		base := name
		if i := strings.IndexByte(base, '['); i >= 0 {
			base = base[:i]
		}
		if v, ok := over[base]; ok {
			return v
		}
		if strings.HasSuffix(base, ".size") || strings.HasSuffix(base, ".len") || strings.HasPrefix(base, "Has") ||
			strings.Contains(base, ".Has") {
			return nil
		}
		n++
		switch w {
		case WGUID:
			return GUID{Lo: uint64(n)<<8 | 0x11, Hi: uint64(n) << 48}
		case WF32:
			return float32(n) + 0.5
		case WBit:
			return n%2 == 0
		}
		return uint64(n)
	}
}

// readerInit sets members of the (zeroed) destination struct that a reader
// dereferences: optional sub-structs are tagged pointers, 1 = absent.
var readerInit = map[string]map[string]uint64{
	"0x1008e3fa0": {"0x3c0": 1, "0x380": 0x3f800000},             // AssistActionData; the +0x368 map's max load factor (1.0f)
	"0x1008d2130": {"0x108": 0x3f800000, "0x400": 1, "0x408": 1}, // the +0xF0 map's max load factor; the two name sets
}

var visFlagSets = []uint8{0, FlagOwner, FlagPartyMember, FlagUnitAll, FlagEmpath, FlagOwner | FlagPartyMember | FlagUnitAll | FlagEmpath}

func layoutCases() []layoutCase {
	var cs []layoutCase
	add := func(name, reader string, prog []op, flags uint8, over map[string]any) {
		cs = append(cs, layoutCase{name: fmt.Sprintf("%s/%#x", name, flags), reader: reader, prog: prog, flags: flags, vals: filler(over)})
	}
	for _, f := range visFlagSets {
		add("ObjectData", "0x1008ce2b8", objectDataCreate, f, nil)
		add("UnitData/empty", "0x1008e3fa0", unitDataCreate, f, nil)
		add("UnitData/full", "0x1008e3fa0", unitDataCreate, f, map[string]any{
			"StateWorldEffectIDs.size":        uint32(2),
			"Unk_0x368.size":                  uint32(2),
			"PassiveSpells.size":              uint32(2),
			"WorldEffects.size":               uint32(3),
			"ChannelObjects.size":             uint32(2),
			"VirtualItems.HasTransmog":        true,
			"VirtualItems.HasIllusion":        true,
			"HasAssistActionData":             true,
			"AssistActionData.PlayerName.len": uint64(5),
			"AssistActionData.PlayerName":     "Jacks",
		})
		add("PlayerData/empty", "0x1008d2130", playerDataCreate, f, nil)
		add("PlayerData/full", "0x1008d2130", playerDataCreate, f, map[string]any{
			"Customizations.size":                                uint32(2),
			"RandomCustomizations.size":                          uint32(1),
			"QuestLogQuestIdToIndex.size":                        uint32(2),
			"QuestSessionQuestLog.size":                          uint32(1),
			"ArenaCooldowns.size":                                uint32(2),
			"PetNames.size":                                      uint32(2),
			"PetNames.Name.len":                                  uint64(3),
			"PetNames.Name":                                      "Rex",
			"CtrOptions.ConditionalFlags.size":                   uint32(2),
			"DungeonScore.Runs.size":                             uint32(2),
			"DungeonScore.Runs.FinishedSuccess":                  true,
			"LeaverInfo.IsLeaver":                                true,
			"VisibleEquipableSpells.Modifications.size":          uint64(2),
			"VisibleEquipableSpells.HasItemBonus":                true,
			"VisibleEquipableSpells.ItemBonus.BonusListIDs.size": uint32(2),
			"VisualItemReplacements.size":                        uint32(2),
			"Name.len":                                           uint64(4),
			"Name":                                               "Jack",
			"Surname.len":                                        uint64(5),
			"Surname":                                            "Stone",
			"HasQuestSession":                                    true,
			"HasLevelLink":                                       true,
			"HasDeclinedNames":                                   true,
			"DeclinedNames.Name.len":                             uint64(2),
			"DeclinedNames.Name":                                 "ab",
		})
		add("GameObjectData/empty", "0x1008b5fe8", gameObjectDataCreate, f, nil)
		add("GameObjectData/full", "0x1008b5fe8", gameObjectDataCreate, f, map[string]any{
			"StateWorldEffectIDs.size":         uint32(2),
			"EnableDoodadSets.size":            uint32(2),
			"WorldEffects.size":                uint32(3),
			"HasAssistActionData":              true,
			"AssistActionData.PlayerName.len":  uint64(5),
			"AssistActionData.PlayerName":      "Jacks",
			"AssistActionData.MonsterName.len": uint64(4),
			"AssistActionData.MonsterName":     "Bob",
		})
		add("ItemData/empty", "0x1008c8c98", itemDataCreate, f, nil)
		add("ItemData/full", "0x1008c8c98", itemDataCreate, f, map[string]any{
			"Modifiers.size":                  uint64(2),
			"ArtifactPowers.size":             uint32(2),
			"Gems.size":                       uint32(2),
			"ItemBonusKey.BonusListIDs.size":  uint32(3),
			"ItemBonusKey.Modifications.size": uint32(2),
		})
	}
	return cs
}

// deltaCase is one delta layout with a set of changed fields.
type deltaCase struct {
	name   string
	reader string
	layout *deltaLayout
	vals   deltaValues
}

func deltaPrelude(l *deltaLayout) func(*rd, mask, *fieldSet) {
	if l == &unitDataDelta {
		return unitDataPrelude
	}
	return nil
}

// allDeltaValues sets every supported field of l to a distinct value.
func allDeltaValues(l *deltaLayout) deltaValues {
	v := deltaValues{}
	n := 0
	for _, f := range l.fields {
		if f.trailer || f.read != nil {
			continue
		}
		n++
		switch f.width {
		case WGUID:
			v[f.name] = GUID{Lo: uint64(n)<<8 | 0x22, Hi: uint64(n) << 40}
		case WF32:
			v[f.name] = float32(n) + 0.25
		default:
			v[f.name] = uint64(n)
		}
	}
	return v
}

func deltaCases() []deltaCase {
	return []deltaCase{
		{"ObjectData/delta/all", "0x1008ce4bc", &objectDataDelta, allDeltaValues(&objectDataDelta)},
		{"UnitData/delta/all", "0x1008e93e0", &unitDataDelta, func() deltaValues {
			v := allDeltaValues(&unitDataDelta)
			v["Field_314"] = true
			return v
		}()},
		{"UnitData/delta/health", "0x1008e93e0", &unitDataDelta, deltaValues{
			"Health": uint64(77), "Power[0]": uint64(5), "MaxPower[0]": uint64(100), "Target": GUID{Lo: 0x99},
		}},
		{"PlayerData/delta/all", "0x1008d4e40", &playerDataDelta, allDeltaValues(&playerDataDelta)},
		{"GameObjectData/delta/all", "0x1008b6b60", &gameObjectDataDelta, allDeltaValues(&gameObjectDataDelta)},
		{"GameObjectData/delta/state", "0x1008b6b60", &gameObjectDataDelta, deltaValues{"State": uint64(0), "Flags": uint64(32)}},
		{"ItemData/delta/all", "0x1008c9ec0", &itemDataDelta, allDeltaValues(&itemDataDelta)},
		{"ItemData/delta/stack", "0x1008c9ec0", &itemDataDelta, deltaValues{"StackCount": uint64(5), "Durability": uint64(30)}},
	}
}

func (c deltaCase) bytes(t *testing.T) []byte {
	w := codec.NewWriter()
	if err := writeDelta(w, c.layout, c.vals); err != nil {
		t.Fatalf("%s: %v", c.name, err)
	}
	return w.Bytes()
}

func (c layoutCase) bytes(t *testing.T) []byte {
	w := codec.NewWriter()
	if err := writeLayout(w, c.prog, c.flags, c.vals); err != nil {
		t.Fatalf("%s: %v", c.name, err)
	}
	return w.Bytes()
}

func TestWriteUfemuCases(t *testing.T) {
	path := os.Getenv("UFEMU_CASES")
	if path == "" {
		t.Skip("set UFEMU_CASES to write the emulator cases")
	}
	var out []ufemuCase
	for _, c := range layoutCases() {
		out = append(out, ufemuCase{Name: c.name, Reader: c.reader, Flags: fmt.Sprintf("%#x", c.flags|flagCreate), Hex: hex.EncodeToString(c.bytes(t)), Init: readerInit[c.reader]})
	}
	for _, c := range deltaCases() {
		out = append(out, ufemuCase{Name: c.name, Reader: c.reader, Flags: "0", Hex: hex.EncodeToString(c.bytes(t)), Delta: true, Init: readerInit[createReaderOf[c.reader]]})
	}
	b, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// createReaderOf maps a delta reader to its struct's create reader (whose
// destination init it shares).
var createReaderOf = map[string]string{"0x1008ce4bc": "0x1008ce2b8", "0x1008e93e0": "0x1008e3fa0", "0x1008d4e40": "0x1008d2130",
	"0x1008b6b60": "0x1008b5fe8", "0x1008c9ec0": "0x1008c8c98"}

// TestDeltasAgainstClient is TestLayoutsAgainstClient for the delta readers.
func TestDeltasAgainstClient(t *testing.T) {
	res := loadUfemu(t)
	for _, c := range deltaCases() {
		t.Run(c.name, func(t *testing.T) {
			r, ok := res[c.name]
			if !ok {
				t.Fatalf("no recorded client run; regenerate testdata/ufemu")
			}
			b := c.bytes(t)
			if hex.EncodeToString(b) != r.Hex {
				t.Fatalf("encoded bytes differ from the recorded run; regenerate testdata/ufemu")
			}
			if r.Error != nil {
				t.Fatalf("client run failed: %s", *r.Error)
			}
			var tr []traceEnt
			d := &rd{r: codec.NewReader(b), trace: &tr}
			fs := d.readDelta(c.layout, deltaPrelude(c.layout))
			if d.err != nil {
				t.Fatal(d.err)
			}
			if len(fs) == 0 {
				t.Fatal("no fields decoded")
			}
			got := map[string]any{}
			for _, f := range fs {
				got[f.Name] = f.Value
			}
			for name, v := range c.vals {
				if fmt.Sprint(got[name]) != fmt.Sprint(v) {
					t.Errorf("%s = %v, want %v", name, got[name], v)
				}
			}
			want := normClient(r.Reads)
			if !reflect.DeepEqual(tr, want) {
				for i := range want {
					if i >= len(tr) || tr[i] != want[i] {
						lo := max(0, i-3)
						t.Fatalf("read %d differs: client %v, ours %v (from %d: client %v ours %v)", i, want[i], at(tr, i), lo, want[lo:min(len(want), i+3)], tr[lo:min(len(tr), i+3)])
					}
				}
				t.Fatalf("we made %d reads, client %d", len(tr), len(want))
			}
			if r.Consumed != len(b) || d.r.Remaining() != 0 {
				t.Fatalf("client consumed %d, we left %d, of %d bytes", r.Consumed, d.r.Remaining(), len(b))
			}
		})
	}
}

func loadUfemu(t *testing.T) map[string]ufemuResult {
	b, err := os.ReadFile("testdata/ufemu/results.json")
	if err != nil {
		t.Fatal(err)
	}
	var rs []ufemuResult
	if err := json.Unmarshal(b, &rs); err != nil {
		t.Fatal(err)
	}
	m := map[string]ufemuResult{}
	for _, r := range rs {
		m[r.Name] = r
	}
	return m
}

func normClient(rs []ufemuRead) []traceEnt {
	out := make([]traceEnt, 0, len(rs))
	for _, r := range rs {
		e := traceEnt{Pos: r.Pos, W: fmt.Sprint(r.W)}
		if e.W == "D" && r.N != nil {
			e.N = *r.N
		}
		out = append(out, e)
	}
	return out
}

// TestLayoutsAgainstClient checks that, for every case, the client's reader
// consumed exactly the bytes we encoded and made the same reads, in the same
// order and at the same offsets, as our decoder.
func TestLayoutsAgainstClient(t *testing.T) {
	res := loadUfemu(t)
	for _, c := range layoutCases() {
		t.Run(c.name, func(t *testing.T) {
			r, ok := res[c.name]
			if !ok {
				t.Fatalf("no recorded client run; regenerate testdata/ufemu (see ufemu_test.go)")
			}
			b := c.bytes(t)
			if hex.EncodeToString(b) != r.Hex {
				t.Fatalf("encoded bytes differ from the recorded run; regenerate testdata/ufemu")
			}
			if r.Error != nil {
				t.Fatalf("client run failed: %s", *r.Error)
			}
			var tr []traceEnt
			d := &rd{r: codec.NewReader(b), trace: &tr}
			var fs fieldSet
			d.readLayout(c.prog, c.flags, &fs)
			if d.err != nil {
				t.Fatal(d.err)
			}
			left := d.r.Remaining()
			defer func() {
				if left != 0 && !t.Failed() {
					t.Fatalf("decoder left %d bytes", left)
				}
			}()
			want := normClient(r.Reads)
			if !reflect.DeepEqual(tr, want) {
				for i := range want {
					if i >= len(tr) || tr[i] != want[i] {
						lo := max(0, i-3)
						t.Fatalf("read %d differs: client %v, ours %v (from %d: client %v ours %v)", i, want[i], at(tr, i), lo, want[lo:min(len(want), i+3)], tr[lo:min(len(tr), i+3)])
					}
				}
				t.Fatalf("we made %d reads, client %d", len(tr), len(want))
			}
			if r.Consumed != len(b) {
				t.Fatalf("client consumed %d of %d bytes", r.Consumed, len(b))
			}
		})
	}
}

func at(tr []traceEnt, i int) any {
	if i < len(tr) {
		return tr[i]
	}
	return "nothing"
}

// TestDumpCreateNames writes each create layout's field names in read order
// (owner and party-member visibility, no counts), for tools/ufdelta.py.
func TestDumpCreateNames(t *testing.T) {
	path := os.Getenv("UFNAMES")
	if path == "" {
		t.Skip("set UFNAMES to write the create field names")
	}
	out := map[string][][2]string{}
	for name, prog := range map[string][]op{"ObjectData": objectDataCreate, "UnitData": unitDataCreate, "PlayerData": playerDataCreate,
		"GameObjectData": gameObjectDataCreate, "ItemData": itemDataCreate} {
		flags := FlagOwner | FlagPartyMember
		w := codec.NewWriter()
		if err := writeLayout(w, prog, flags, mapValues(nil)); err != nil {
			t.Fatal(err)
		}
		var fs fieldSet
		d := &rd{r: codec.NewReader(w.Bytes())}
		d.readLayout(prog, flags, &fs)
		for _, f := range fs.list {
			out[name] = append(out[name], [2]string{f.Name, f.Width.String()})
		}
	}
	b, _ := json.Marshal(out)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
