package updateobject

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Real bytes: every SMSG_UPDATE_OBJECT of the 70755 Goldshire capture (88
// packets), recorded on a private local TrinityCore server
// (testdata/real70755/README.md). File names keep the capture's packet
// sequence numbers.

type realPacket struct {
	name string
	b    []byte
}

func realPackets(t *testing.T) []realPacket {
	t.Helper()
	files, err := filepath.Glob("testdata/real70755/*_SMSG_UPDATE_OBJECT.bin")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) != 88 {
		t.Fatalf("%d packets in testdata/real70755, want 88", len(files))
	}
	out := make([]realPacket, len(files))
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = realPacket{strings.SplitN(filepath.Base(f), "_", 2)[0], b}
	}
	return out
}

func fieldMap(fs []Field) map[string]any {
	m := map[string]any{}
	for _, f := range fs {
		m[f.Name] = f.Value
	}
	return m
}

// TestReal70755Decode decodes every packet, every create's fields and every
// values block, and checks the totals and the known gaps.
func TestReal70755Decode(t *testing.T) {
	types := map[GUID]ObjectType{}
	var blocks, creates, values int
	createsByType := map[ObjectType]int{}
	vendors := 0
	type gap struct{ pkt, why string }
	var gaps []gap
	for _, p := range realPackets(t) {
		u, err := Decode(p.b)
		if err != nil {
			t.Fatalf("packet %s: %v", p.name, err)
		}
		if u.MapID != 0 {
			t.Errorf("packet %s: map %d, want 0 (Eastern Kingdoms)", p.name, u.MapID)
		}
		// The framing and movement encoders reproduce the server's bytes.
		if string(Encode(u)) != string(p.b) {
			t.Errorf("packet %s: re-encoded bytes differ", p.name)
		}
		for _, bl := range u.Blocks {
			blocks++
			switch {
			case bl.Create != nil:
				creates++
				c := bl.Create
				types[bl.GUID] = c.ObjectType
				createsByType[c.ObjectType]++
				of, err := c.DecodeFields()
				if err != nil {
					t.Errorf("packet %s: create %v (type %d): %v", p.name, bl.GUID, c.ObjectType, err)
					continue
				}
				if of.Vendor != nil {
					vendors++
				}
			case bl.Values != nil:
				values++
				_, err := DecodeValues(types[bl.GUID], bl.Values)
				if err != nil {
					gaps = append(gaps, gap{p.name, err.Error()})
				}
			}
		}
	}
	if blocks != 333 || creates != 251 || values != 82 {
		t.Errorf("%d blocks (%d creates, %d values), want 333 (251, 82)", blocks, creates, values)
	}
	want := map[ObjectType]int{TypeGameObject: 132, TypeUnit: 97, TypeItem: 21, TypeActivePlayer: 1}
	for k, n := range want {
		if createsByType[k] != n {
			t.Errorf("%d creates of type %d, want %d", createsByType[k], k, n)
		}
	}
	if vendors != 18 {
		t.Errorf("%d creates with FVendor_C, want 18", vendors)
	}
	// Every values block decodes, the quest log (packet 1350) and the item
	// equipped (1403) included.
	if len(gaps) != 0 {
		t.Fatalf("values gaps %+v", gaps)
	}
}

func realBlock(t *testing.T, pkt string, i int) Block {
	t.Helper()
	b, err := os.ReadFile("testdata/real70755/" + pkt + "_SMSG_UPDATE_OBJECT.bin")
	if err != nil {
		t.Fatal(err)
	}
	u, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return u.Blocks[i]
}

func TestReal70755ActivePlayerCreate(t *testing.T) {
	bl := realBlock(t, "0275", 19)
	c := bl.Create
	if c == nil || c.ObjectType != TypeActivePlayer || !c.Movement.ThisIsYou || c.FieldFlags != FlagOwner|FlagPartyMember {
		t.Fatalf("block: %+v", bl)
	}
	if bl.GUID != (GUID{Hi: 0x0801180000000000, Lo: 3}) {
		t.Errorf("guid %v", bl.GUID)
	}
	of, err := c.DecodeFields()
	if err != nil {
		t.Fatal(err)
	}
	u := of.Unit
	if u.Level != 61 || u.Health != 2260 || u.MaxHealth != 2260 || u.Race != 3 || u.ClassID != 7 {
		t.Errorf("unit: level %d health %d/%d race %d class %d", u.Level, u.Health, u.MaxHealth, u.Race, u.ClassID)
	}
	if of.Player.Name != "Qweqwee" || of.ActivePlayer == nil || len(of.ActivePlayer.Raw) == 0 {
		t.Errorf("player %q, active player %d raw bytes", of.Player.Name, len(of.ActivePlayer.Raw))
	}
	p := c.Movement.Movement.Info.Position
	if p.X < -9488.3 || p.X > -9488.2 || p.Y < 63.8 || p.Y > 63.9 {
		t.Errorf("position %+v", p)
	}
}

// TestReal70755NestedValues: packet 1403 equips an item (VisibleItems[14],
// a nested VisibleItem delta) and raises the maximum health 2260 -> 2430;
// packet 1350 accepts quest 47 into quest log slot 0 (a
// QuestLogQuestIdToIndex change and a QuestLog nested delta).
func TestReal70755NestedValues(t *testing.T) {
	d, err := DecodeValues(TypeActivePlayer, realBlock(t, "1403", 1).Values)
	if err != nil {
		t.Fatal(err)
	}
	if v := fieldMap(d.Unit)["MaxHealth"]; v != int64(2430) {
		t.Errorf("MaxHealth = %v, want 2430", v)
	}
	of, err := realBlock(t, "0275", 19).Create.DecodeFields()
	if err != nil {
		t.Fatal(err)
	}
	p := of.Player
	if p.VisibleItems[15].ItemID != 36 || p.VisibleItems[14].ItemID != 0 {
		t.Fatalf("create: visible items %+v", p.VisibleItems)
	}
	p.ApplyTo(d.Player)
	if v := p.VisibleItems[14]; v.ItemID != 21710 || v.ItemModifiedAppearanceID != 164745 {
		t.Errorf("VisibleItems[14] = %+v", v)
	}
	if p.VisibleItems[15].ItemID != 36 {
		t.Errorf("VisibleItems[15] = %+v", p.VisibleItems[15])
	}

	d, err = DecodeValues(TypeActivePlayer, realBlock(t, "1350", 0).Values)
	if err != nil {
		t.Fatal(err)
	}
	p.ApplyTo(d.Player)
	if p.QuestLogQuestIDToIndex[47] != 0 || len(p.QuestLogQuestIDToIndex) != 1 || p.QuestLog[0].QuestID != 47 {
		t.Errorf("quest log: map %v, slot 0 %+v", p.QuestLogQuestIDToIndex, p.QuestLog[0])
	}

	// A plain health tick of the active player, with the ActivePlayerData
	// delta kept raw after it.
	d, err = DecodeValues(TypeActivePlayer, realBlock(t, "1049", 0).Values)
	if err != nil {
		t.Fatal(err)
	}
	if v := fieldMap(d.Unit)["Health"]; v != int64(2244) || len(d.ActivePlayerRaw) != 23 {
		t.Errorf("Health = %v, %d raw bytes", v, len(d.ActivePlayerRaw))
	}
}

func TestReal70755GameObjectAndItem(t *testing.T) {
	gos := map[int32]*GameObjectData{}
	var item *ItemData
	for _, p := range realPackets(t) {
		u, _ := Decode(p.b)
		for _, bl := range u.Blocks {
			if bl.Create == nil {
				continue
			}
			of, err := bl.Create.DecodeFields()
			if err != nil {
				t.Fatal(err)
			}
			if of.GameObject != nil {
				gos[of.Object.EntryID] = of.GameObject
			}
			if of.Item != nil && of.Object.EntryID == 36 {
				item = of.Item
			}
		}
	}
	for _, w := range []struct {
		entry   int32
		display int32
		typ     int8
		faction int32
	}{
		{176495, 3031, 15, 0},  // a transport (MO_TRANSPORT)
		{142075, 1907, 19, 12}, // the Goldshire mailbox
		{22772, 199, 8, 0},     // a spell focus (brazier) at the inn
		{177494, 39, 7, 0},     // a chair (GAMEOBJECT_TYPE_CHAIR)
	} {
		g := gos[w.entry]
		if g == nil || g.DisplayID != w.display || g.TypeID != w.typ || g.FactionTemplate != w.faction || g.State != 1 {
			t.Errorf("game object %d: %+v", w.entry, g)
		}
	}
	// A worn item of the player's (owner flags: stack count and durability).
	self := GUID{Hi: 0x0801180000000000, Lo: 3}
	if item == nil || item.Owner != self || item.ContainedIn != self || item.StackCount != 1 || item.Durability != 32 || item.MaxDurability != 35 {
		t.Errorf("item 36: %+v", item)
	}
}

// TestReal70755Rotation checks UnpackRotation against the stationary facing
// the same create carries: every object here turns about the vertical, so
// the quaternion is (0, 0, sin(o/2), cos(o/2)) up to sign.
func TestReal70755Rotation(t *testing.T) {
	n := 0
	for _, p := range realPackets(t) {
		u, _ := Decode(p.b)
		for _, bl := range u.Blocks {
			c := bl.Create
			if c == nil || c.Movement.Rotation == nil || c.Movement.Stationary == nil {
				continue
			}
			q := UnpackRotation(*c.Movement.Rotation)
			o := float64(c.Movement.Stationary.O)
			// q and -q are the same rotation: compare |<q, (0,0,z,w)>| with 1.
			dot := float64(q.Z)*math.Sin(o/2) + float64(q.W)*math.Cos(o/2)
			if q.X != 0 || q.Y != 0 || math.Abs(math.Abs(dot)-1) > 1e-5 {
				t.Errorf("packet %s %v: rotation %+v, facing %.3f", p.name, bl.GUID, q, o)
			}
			n++
		}
	}
	if n != 132 {
		t.Errorf("%d rotations checked, want 132", n)
	}
}
