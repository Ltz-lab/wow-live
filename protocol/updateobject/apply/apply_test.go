package apply

import (
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/updateobject"
	"github.com/Ltz-lab/wow-live/state"
)

var update = flag.Bool("update", false, "rewrite testdata/*.hex")

// Wire GUIDs: a player (high type 0x08) and a creature.
var (
	selfGUID     = updateobject.GUID{Lo: 0x2A, Hi: 0x0800000000000000}
	creatureGUID = updateobject.GUID{Lo: 0x00000000000F4A11, Hi: 0x2040000000000000}
)

// selfCreate is a synthetic create of the player's own character with the
// client's real layouts: the movement section of TestActivePlayerGolden,
// field flags Owner|PartyMember (what the fork sends a player about
// itself), fragments CGObject + Tag_Unit + Tag_Player, and the
// Object/Unit/Player chain. ActivePlayerData, which is not decoded, is an
// opaque tail.
func selfCreate(t *testing.T) *updateobject.UpdateObject {
	speeds := [9]float32{2.5, 7, 4.5, 4.722222, 2.5, 7, 4.5, 3.141594, 3.141594}
	mv := &updateobject.CreateMovement{
		ThisIsYou: true,
		Movement: &updateobject.MovementUpdate{
			Info: updateobject.MovementInfo{
				Mover:           selfGUID,
				MoveTime:        1000,
				Position:        updateobject.Pos4{X: -8949.95, Y: -132.493, Z: 83.5312, O: 0.5},
				MoveIndex:       7,
				GravityModifier: 1,
			},
			Speeds:       speeds,
			ForcesModMag: 1,
		},
		ActivePlayer: &updateobject.ActivePlayerCreate{},
	}
	data, err := updateobject.BuildFragmentData(updateobject.TypeActivePlayer, updateobject.FlagOwner|updateobject.FlagPartyMember, updateobject.Fields{
		Object: map[string]any{"EntryID": int32(0), "DynamicFlags": uint32(0), "Scale": float32(1)},
		Unit: map[string]any{
			"DisplayID": int32(49), "NativeDisplayID": int32(49),
			"Race": uint8(1), "ClassId": uint8(1), "PlayerClassId": uint8(1), "Sex": uint8(0), "DisplayPower": uint8(1),
			"Health": int64(250), "MaxHealth": int64(300), "Level": int32(12), "EffectiveLevel": int32(12),
			"FactionTemplate": int32(1), "MaxPower[1]": int32(1000),
			"BoundingRadius": float32(0.389), "CombatReach": float32(1.5), "DisplayScale": float32(1),
			"NativeXDisplayScale": float32(1), "ModCastingSpeed": float32(1), "ModCastingSpeedNeg": float32(1),
			"ModHaste": float32(1), "ModSpellHaste": float32(1), "ModRangedHaste": float32(1), "ModHasteRegen": float32(1),
			"ModTimeRate": float32(1), "MaxHealthModifier": float32(1), "HoverHeight": float32(1),
			"AttackRoundBaseTime[0]": uint32(2000), "AttackRoundBaseTime[1]": uint32(2000),
			"Target": creatureGUID,
		},
		Player: map[string]any{
			"WowAccount":                     updateobject.GUID{Lo: 0x1001, Hi: 0x0C00000000000000},
			"PlayerFlags":                    uint32(0x8),
			"Name.len":                       uint64(4),
			"Name":                           "Jack",
			"SuperDistrict.SuperDistrictID":  uint32(2),
			"SuperDistrict2.SuperDistrictID": uint32(2),
		},
		ActivePlayerRaw: make([]byte, 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &updateobject.UpdateObject{MapID: 0, Flag7: true, Blocks: []updateobject.Block{{
		Type: updateobject.BlockCreateObject2,
		GUID: selfGUID,
		Create: &updateobject.CreateBlock{
			ObjectType:   updateobject.TypeActivePlayer,
			Movement:     mv,
			FieldFlags:   updateobject.FlagOwner | updateobject.FlagPartyMember,
			Fragments:    []uint8{updateobject.FragmentCGObject, updateobject.FragmentTagUnit, updateobject.FragmentTagPlayer},
			FragmentData: data,
		},
	}}}
}

// golden checks b against testdata/name (or rewrites it with -update) and
// returns the stored bytes, so the regression runs on the recorded message.
func golden(t *testing.T, name string, b []byte) []byte {
	path := "testdata/" + name
	if *update {
		if err := os.WriteFile(path, []byte(hex.EncodeToString(b)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString(strings.TrimSpace(string(h)))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(b) != hex.EncodeToString(want) {
		t.Fatalf("%s: encoder output changed; rerun with -update if intended", name)
	}
	return want
}

func mustApply(t *testing.T, s *state.State, msg []byte) Result {
	t.Helper()
	u, err := updateobject.Decode(msg)
	if err != nil {
		t.Fatal(err)
	}
	return Apply(s, u)
}

// TestActivePlayerPipeline is the pipeline regression test: bytes ->
// updateobject.Decode -> Apply -> state snapshot.
func TestActivePlayerPipeline(t *testing.T) {
	s := state.New()
	msg := golden(t, "active_player_create.hex", updateobject.Encode(selfCreate(t)))
	res := mustApply(t, s, msg)
	if len(res.Skipped) != 0 || len(res.Created) != 1 {
		t.Fatalf("result %+v", res)
	}
	snap := s.Snapshot()
	if snap.Self != GUIDOf(selfGUID) {
		t.Fatalf("self %v", snap.Self)
	}
	if len(snap.Objects) != 1 {
		t.Fatalf("%d objects", len(snap.Objects))
	}
	o := snap.Objects[0]
	want := state.Object{
		GUID: GUIDOf(selfGUID), Kind: state.KindPlayer, Name: "Jack", Level: 12, Class: 1, Race: 1,
		Position: state.Vec3{X: -8949.95, Y: -132.493, Z: 83.5312}, Facing: 0.5,
		Health: 250, MaxHealth: 300, Target: GUIDOf(creatureGUID),
	}
	o.Updated = want.Updated
	if o.Auras != nil || o.Power != nil {
		t.Fatalf("auras/power invented: %+v", o)
	}
	o.Auras, o.Power = nil, nil
	if !equalObject(o, want) {
		t.Fatalf("snapshot object\n got %+v\nwant %+v", o, want)
	}

	// Auras come from elsewhere (SMSG_AURA_UPDATE); an update must keep them.
	if err := s.Mutate(GUIDOf(selfGUID), func(o *state.Object) { o.Auras = []string{"Battle Shout"} }); err != nil {
		t.Fatal(err)
	}

	// A values update: health and level.
	vals, err := updateobject.BuildValues(nil, map[string]any{"Health": int64(180), "Level": int32(13)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	upd := golden(t, "active_player_values.hex", updateobject.Encode(&updateobject.UpdateObject{Flag7: true, Blocks: []updateobject.Block{
		{Type: updateobject.BlockValues, GUID: selfGUID, Values: &updateobject.ValuesBlock{Data: vals}},
	}}))
	res = mustApply(t, s, upd)
	if len(res.Skipped) != 0 || len(res.Updated) != 1 {
		t.Fatalf("update result %+v", res)
	}
	o, _ = s.Object(GUIDOf(selfGUID))
	if o.Health != 180 || o.Level != 13 || o.MaxHealth != 300 || len(o.Auras) != 1 || o.Auras[0] != "Battle Shout" {
		t.Fatalf("after update %+v", o)
	}
}

func equalObject(a, b state.Object) bool {
	return a.GUID == b.GUID && a.Kind == b.Kind && a.Name == b.Name && a.Level == b.Level && a.Class == b.Class &&
		a.Race == b.Race && a.Position == b.Position && a.Facing == b.Facing && a.Health == b.Health &&
		a.MaxHealth == b.MaxHealth && a.Target == b.Target && a.Entry == b.Entry
}

// TestCreateUpdateRemove runs a creature through create -> update -> remove.
func TestCreateUpdateRemove(t *testing.T) {
	s := state.New()
	data, err := updateobject.BuildFragmentData(updateobject.TypeUnit, 0, updateobject.Fields{
		Object: map[string]any{"EntryID": int32(299), "Scale": float32(1)},
		Unit:   map[string]any{"Health": int64(42), "MaxHealth": int64(42), "Level": int32(2), "ClassId": uint8(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	create := &updateobject.UpdateObject{Blocks: []updateobject.Block{{
		Type: updateobject.BlockCreateObject2, GUID: creatureGUID,
		Create: &updateobject.CreateBlock{
			ObjectType:   updateobject.TypeUnit,
			Movement:     &updateobject.CreateMovement{Stationary: &updateobject.Pos4{X: 10, Y: 20, Z: 30, O: 1}},
			Fragments:    []uint8{updateobject.FragmentCGObject, updateobject.FragmentTagUnit},
			FragmentData: data,
		},
	}}}
	res := mustApply(t, s, updateobject.Encode(create))
	key := GUIDOf(creatureGUID)
	if len(res.Created) != 1 || res.Created[0] != key || len(res.Skipped) != 0 {
		t.Fatalf("create %+v", res)
	}
	o, _ := s.Object(key)
	if o.Kind != state.KindUnit || o.Entry != 299 || o.Health != 42 || o.Position != (state.Vec3{X: 10, Y: 20, Z: 30}) {
		t.Fatalf("created %+v", o)
	}

	vals, err := updateobject.BuildValues(nil, map[string]any{"Health": int64(0), "Target": selfGUID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res = mustApply(t, s, updateobject.Encode(&updateobject.UpdateObject{Blocks: []updateobject.Block{
		{Type: updateobject.BlockValues, GUID: creatureGUID, Values: &updateobject.ValuesBlock{Data: vals}},
		{Type: updateobject.BlockValues, GUID: updateobject.GUID{Lo: 999}, Values: &updateobject.ValuesBlock{Data: vals}},
	}}))
	if len(res.Updated) != 1 || len(res.Skipped) != 1 || !errors.Is(res.Skipped[0].Err, ErrUnknownObject) {
		t.Fatalf("update %+v", res)
	}
	o, _ = s.Object(key)
	if o.Health != 0 || o.MaxHealth != 42 || o.Target != GUIDOf(selfGUID) {
		t.Fatalf("updated %+v", o)
	}

	res = mustApply(t, s, updateobject.Encode(&updateobject.UpdateObject{Removed: &updateobject.RemovedObjects{Destroyed: []updateobject.GUID{creatureGUID}}}))
	if len(res.Removed) != 1 || s.Len() != 0 {
		t.Fatalf("remove %+v, %d objects left", res, s.Len())
	}
}

// TestUnsupportedBlocksAreReported checks that a block we cannot decode is
// skipped with ErrUnsupported while the rest of the message applies.
func TestUnsupportedBlocksAreReported(t *testing.T) {
	s := state.New()
	u := &updateobject.UpdateObject{Blocks: []updateobject.Block{
		{Type: updateobject.BlockCreateObject, GUID: updateobject.GUID{Lo: 7}, Create: &updateobject.CreateBlock{
			ObjectType:   updateobject.TypeCorpse,
			Movement:     &updateobject.CreateMovement{Stationary: &updateobject.Pos4{X: 1}},
			Fragments:    []uint8{updateobject.FragmentCGObject, 208},
			FragmentData: []byte{1, 0, 0, 0, 0},
		}},
	}}
	res := mustApply(t, s, updateobject.Encode(u))
	if len(res.Skipped) != 1 || !errors.Is(res.Skipped[0].Err, updateobject.ErrUnsupported) {
		t.Fatalf("result %+v", res)
	}
	if o, ok := s.Object(state.LowGUID(7)); !ok || o.Kind != state.KindCorpse || o.Position.X != 1 {
		t.Fatalf("object %+v %v", o, ok)
	}
}
