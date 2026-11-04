package updateobject

import (
	"fmt"
	"strconv"
)

// Lists and the remaining nested parts of the UnitData, GameObjectData and
// ItemData delta readers (70755). See delta_nested.go for the mechanism.
//
// Two list kinds:
//
//   - A whole list ("static"): a 32-bit count in the bit stream it follows,
//     then count values with the byte getters; it replaces the list.
//     UnitData bit 2 StateWorldEffectIDs (0x1008e962c), GameObjectData
//     bit 1 StateWorldEffectIDs (0x1008b6bb4, only with bits 0 and 1 both).
//   - A dynamic list's change mask (readDynMask), then, after every mask of
//     that run, the changed elements in index order.

// readDynMask reads a dynamic list's update mask the way the client does
// inline (e.g. UnitData 0x1008e969c..0x1008e97ec): a 32-bit size in the
// current bit stream, then ceil(size/32) mask words. When size > 32 the
// words are read whole with the u32 getter (0x100037054) if the bit reader
// has no pending bits, else as 32 bits each (0x1008367bc); a word is taken
// while more than 32 bits remain before it; a last partial word is read as
// its remaining bits (0x1008346ac). Returns the size and the changed
// indices.
func (d *rd) readDynMask(name string, out *fieldSet) []int {
	n := uint32(d.bitsTraced(32))
	out.add(Field{Name: name + ".size", Width: WU32, Value: n})
	if d.err != nil {
		return nil
	}
	if n > 1<<16 {
		d.fail(fmt.Errorf("%w: %s.size %d", ErrMalformed, name, n))
		return nil
	}
	return d.readDynMaskWords(int(n))
}

func (d *rd) readDynMaskWords(n int) []int {
	words := (n + 31) / 32
	m := make([]uint32, words)
	rem, k := n, 0
	if n > 32 && words > 0 {
		aligned := d.r.Aligned()
		for k < words {
			prev := rem
			if aligned {
				d.traceRead("4", 0)
				m[k] = d.u32()
			} else {
				m[k] = uint32(d.bitsTraced(32))
			}
			k++
			rem = prev - 32
			if prev < 65 {
				break
			}
		}
	}
	if rem != 0 && k < words {
		m[k] = uint32(d.bitsTraced(uint(rem)))
	}
	var changed []int
	for i := 0; i < n; i++ {
		if m[i/32]&(1<<uint(i%32)) != 0 {
			changed = append(changed, i)
		}
	}
	return changed
}

// readWholeList is a static list: a 32-bit count in the current bit stream,
// then count u32s (getter 0x100037054).
func (d *rd) readWholeList(name string, out *fieldSet) {
	n := uint32(d.bitsTraced(32))
	out.add(Field{Name: name + ".size", Width: WU32, Value: n})
	if uint64(n)*4 > uint64(d.r.Remaining()) {
		d.fail(fmt.Errorf("%w: %s.size %d in %d bytes", ErrMalformed, name, n, d.r.Remaining()))
		return
	}
	for i := 0; i < int(n) && d.err == nil; i++ {
		d.val(out, idx(name, i), WU32)
	}
}

func init() {
	// UnitData, 0x1008e93e0.
	ud := &unitDataDelta
	ud.preludeFn = unitDataPrelude
	splice(ud, []deltaField{{bit: 143, read: mapDelta("Unk_0x368.", "A", "B")}})
	for i := 0; i < 3; i++ {
		// VirtualItems, group 193 (0x1008ee5a8): 0x1008e2974 three times.
		splice(ud, []deltaField{{bit: 194 + i, read: visibleItemDelta("VirtualItems.", i)}})
	}
	// The byte after group 128's values (0x1008ee0f0) is HasAssistActionData;
	// bit 151 is read right after it.
	replaceTrailer(ud, 128, unitAssistActionData)
	splice(ud, []deltaField{{bit: 151, read: readNothing}})
	for _, b := range []int{2, 3, 4, 5, 143, 151, 194, 195, 196} {
		delete(ud.unsupported, b)
	}

	// GameObjectData, 0x1008b6b60.
	gd := &gameObjectDataDelta
	gd.preludeFn = gameObjectDataPrelude
	replaceTrailer(gd, 0, gameObjectAssistActionDataDelta)
	splice(gd, []deltaField{{bit: 27, read: readNothing}})
	for _, b := range []int{1, 2, 3, 27} {
		delete(gd.unsupported, b)
	}

	// ItemData, 0x1008c9ec0.
	id := &itemDataDelta
	id.preludeFn = itemDataPrelude
	splice(id, []deltaField{
		{bit: 17, read: itemModifiersDelta},
		{bit: 19, read: itemBonusKeyDelta},
	})
	var ench []deltaField
	for i := 0; i < 14; i++ {
		ench = append(ench, deltaField{bit: 28 + i, read: enchantmentDelta(i)})
	}
	splice(id, ench)
	for _, b := range []int{1, 2} {
		delete(id.unsupported, b)
	}
}

// readNothing marks a bit another part reads (the bit is known, and its
// data comes with an earlier field).
func readNothing(*rd, mask, *fieldSet) {}

// replaceTrailer turns a struct's trailing bit-group byte into a reader.
func replaceTrailer(l *deltaLayout, group int, read func(d *rd, m mask, out *fieldSet)) {
	for i, f := range l.fields {
		if f.trailer && f.bit == group {
			l.fields[i] = deltaField{bit: group, read: read, trailer: true}
			return
		}
	}
	panic(fmt.Sprintf("updateobject: %s has no trailer for group %d", l.name, group))
}

// unitDataPrelude is UnitData's bit stream after the mask (0x1008e9470):
// the full-values flag (kept at [sp+0x18]; it selects the bit-143 map's
// reader, 0x1008edce8), bit 1 Field_314 (+0x388), bit 2 StateWorldEffectIDs
// as a whole list (+0x18). Then, with group 0, a new bit reader
// (0x1008e9684) takes the masks of bits 3 PassiveSpells (+0x2A0), 4
// WorldEffects (+0x2D8) and 5 ChannelObjects (+0x310), and the changed
// elements follow: PassiveSpells two i32s (0x1008e9c04, 0x1008e9c1c),
// WorldEffects an i32 (0x1008e9cfc), ChannelObjects a GUID (0x1008e9d78).
func unitDataPrelude(d *rd, m mask, out *fieldSet) {
	l := &unitDataDelta
	d.group()
	d.fullNested = d.bitsTraced(1) != 0
	if l.effective(m, 1) {
		out.add(Field{Name: "Field_314", Width: WBit, Value: d.bitsTraced(1) != 0})
	}
	if l.effective(m, 2) {
		d.readWholeList("StateWorldEffectIDs", out)
	}
	if !m.has(0) {
		return
	}
	d.group()
	var passive, effects, channel []int
	if l.effective(m, 3) {
		passive = d.readDynMask("PassiveSpells", out)
	}
	if l.effective(m, 4) {
		effects = d.readDynMask("WorldEffects", out)
	}
	if l.effective(m, 5) {
		channel = d.readDynMask("ChannelObjects", out)
	}
	for _, i := range passive {
		d.val(out, idx("PassiveSpells.SpellID", i), WI32)
		d.val(out, idx("PassiveSpells.AuraSpellID", i), WI32)
	}
	for _, i := range effects {
		d.val(out, idx("WorldEffects", i), WI32)
	}
	for _, i := range channel {
		d.val(out, idx("ChannelObjects", i), WGUID)
	}
}

// unitAssistActionData: the byte after group 128's values; its top bit is
// HasAssistActionData (allocated or freed, +0x3C0). If present and bit 151
// is set, 0x1008e356c: a 4-bit mask on a new byte; bit 0 groups 1 Type u8
// (+0), 3 VirtualRealmAddress u32 (+0x34), and with bits 0 and 2 a byte
// whose top 6 bits are the player name's length, then the name.
func unitAssistActionData(d *rd, m mask, out *fieldSet) {
	d.group()
	d.traceRead("1", 0)
	b := d.u8()
	has := b&0x80 != 0
	if b&0x7f != 0 {
		d.fail(fmt.Errorf("%w: UnitData bit group after group 128 is %#02x", ErrUnsupported, b))
		return
	}
	out.add(Field{Name: "HasAssistActionData", Width: WBit, Value: has})
	if !has || !unitDataDelta.effective(m, 151) {
		return
	}
	d.group()
	am := d.bitsTraced(4)
	if am&1 == 0 {
		return
	}
	if am&2 != 0 {
		d.val(out, "AssistActionData.Type", WU8)
	}
	if am&8 != 0 {
		d.val(out, "AssistActionData.VirtualRealmAddress", WU32)
	}
	if am&5 == 5 {
		d.traceRead("1", 0)
		n := int(d.u8() >> 2)
		out.add(Field{Name: "AssistActionData.PlayerName.len", Width: WBits, Value: uint64(n)})
		d.traceRead("D", n)
		s, err := d.r.Bytes(n)
		d.fail(err)
		out.add(Field{Name: "AssistActionData.PlayerName", Width: WString, Value: string(s)})
	}
}

// gameObjectDataPrelude: with bits 0 and 1, StateWorldEffectIDs (+0x18) as a
// whole list in the mask's bit stream (0x1008b6bb4). Then, with group 0, a
// new bit reader (0x1008b6c0c) takes the masks of bits 2 EnableDoodadSets
// (+0x80) and 3 WorldEffects (+0xD0), and the changed elements follow, an
// i32 each (0x1008b6ef0, 0x1008b6fc0).
func gameObjectDataPrelude(d *rd, m mask, out *fieldSet) {
	l := &gameObjectDataDelta
	if l.effective(m, 1) {
		d.readWholeList("StateWorldEffectIDs", out)
	}
	if !m.has(0) {
		return
	}
	d.group()
	var doodads, effects []int
	if l.effective(m, 2) {
		doodads = d.readDynMask("EnableDoodadSets", out)
	}
	if l.effective(m, 3) {
		effects = d.readDynMask("WorldEffects", out)
	}
	for _, i := range doodads {
		d.val(out, idx("EnableDoodadSets", i), WI32)
	}
	for _, i := range effects {
		d.val(out, idx("WorldEffects", i), WI32)
	}
}

// gameObjectAssistActionDataDelta: the byte after group 0's values
// (0x1008b755c); its top bit is HasAssistActionData (+0x180). If present
// and bit 27 is set, the struct is read whole by 0x100851f08, the create's
// reader (gameObjectAssistActionData).
func gameObjectAssistActionDataDelta(d *rd, m mask, out *fieldSet) {
	d.group()
	d.traceRead("1", 0)
	b := d.u8()
	has := b&0x80 != 0
	if b&0x7f != 0 {
		d.fail(fmt.Errorf("%w: GameObjectData bit group after group 0 is %#02x", ErrUnsupported, b))
		return
	}
	out.add(Field{Name: "HasAssistActionData", Width: WBit, Value: has})
	if has && gameObjectDataDelta.effective(m, 27) {
		d.inBits = false
		d.runOps(gameObjectAssistActionData, 0, out, "")
		d.inBits = false
	}
}

// itemDataPrelude: with group 0, the masks of bits 1 ArtifactPowers (+0xB8)
// and 2 Gems (+0xF0) continue the item's mask bit stream (0x1008c9f60,
// 0x1008ca140); then the changed elements: an ArtifactPower is i16, u8, u8
// (0x1008ca384..0x1008ca3b4), a Gem a nested delta (0x1008c88cc).
func itemDataPrelude(d *rd, m mask, out *fieldSet) {
	l := &itemDataDelta
	if !m.has(0) {
		return
	}
	var powers, gems []int
	if l.effective(m, 1) {
		powers = d.readDynMask("ArtifactPowers", out)
	}
	if l.effective(m, 2) {
		gems = d.readDynMask("Gems", out)
	}
	for _, i := range powers {
		d.val(out, idx("ArtifactPowers.ArtifactPowerID", i), WI16)
		d.val(out, idx("ArtifactPowers.PurchasedRank", i), WU8)
		d.val(out, idx("ArtifactPowers.CurrentRankWithBonus", i), WU8)
	}
	for _, i := range gems {
		gemDelta(d, i, out)
	}
}

// gemDelta is SocketedGem, 0x1008c88cc: a byte whose top bit says mask
// block 0 follows, then 32 bits. Bit 0 groups 1 ItemID i32 (+0), 2 Context
// u8 (+4); bit 3 groups 4+j, j < 16, BonusListIDs[j] u16 (+6 + 2j).
func gemDelta(d *rd, i int, out *fieldSet) {
	d.group()
	var gm uint32
	if d.bitsTraced(1) != 0 {
		gm = uint32(d.bitsTraced(32))
	}
	if gm&1 != 0 {
		if gm&2 != 0 {
			d.val(out, idx("Gems.ItemID", i), WI32)
		}
		if gm&4 != 0 {
			d.val(out, idx("Gems.Context", i), WU8)
		}
	}
	if gm&8 != 0 {
		for j := 0; j < 16; j++ {
			if gm&(1<<uint(4+j)) != 0 {
				d.val(out, "Gems.BonusListIDs["+strconv.Itoa(i)+"]["+strconv.Itoa(j)+"]", WU16)
			}
		}
	}
}

// itemModifiersDelta is bit 17, ItemModList (+0xB0), 0x100833fdc: a byte;
// without its top bit nothing more. Else its low 7 bits are the size, a
// new bit reader takes the size's mask words (readDynMaskWords), and each
// changed modifier is a u8 Type and an i32 Value.
func itemModifiersDelta(d *rd, _ mask, out *fieldSet) {
	d.traceRead("1", 0)
	b := d.u8()
	out.add(Field{Name: "Modifiers.Changed", Width: WBit, Value: b&0x80 != 0})
	if b&0x80 == 0 {
		return
	}
	n := int(b & 0x7f)
	out.add(Field{Name: "Modifiers.size", Width: WBits, Value: uint64(n)})
	d.group()
	for _, i := range d.readDynMaskWords(n) {
		d.val(out, idx("Modifiers.Type", i), WU8)
		d.val(out, idx("Modifiers.Value", i), WI32)
	}
}

// itemBonusKeyDelta is bit 19, ItemBonusKey (+0x130), read whole by
// 0x100834584, the create's reader.
func itemBonusKeyDelta(d *rd, _ mask, out *fieldSet) {
	d.inBits = false
	d.runOps(prefixed("ItemBonusKey.", itemBonusKey), 0, out, "")
	d.inBits = false
}

// enchantmentDelta is Enchantment[i] (+0x180 + 0xC i), group 27, bits 28+i
// (loop 0x1008cb24c), 0x1008c862c: a 5-bit mask on a new byte; bit 0
// groups 1 ID i32 (+0), 2 Duration u32 (+4), 3 Charges i16 (+8),
// 4 Inactive u16 (+0xA). Names follow the create.
func enchantmentDelta(i int) func(d *rd, _ mask, out *fieldSet) {
	return func(d *rd, _ mask, out *fieldSet) {
		d.group()
		em := d.bitsTraced(5)
		if em&1 == 0 {
			return
		}
		for b, f := range []struct {
			name string
			w    Width
		}{{"ID", WI32}, {"Duration", WU32}, {"Charges", WI16}, {"Inactive", WU16}} {
			if em&(1<<uint(b+1)) != 0 {
				d.val(out, idx("Enchantment."+f.name, i), f.w)
			}
		}
	}
}
