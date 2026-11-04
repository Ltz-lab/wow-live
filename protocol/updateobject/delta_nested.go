package updateobject

import (
	"fmt"
	"strconv"
)

// Nested parts of the delta readers: sub-structs with their own change
// masks, maps and the flag that switches them to full values. Each is read
// at the point of the client's read order where the reader calls it, so it
// is spliced into the generated table (delta_tables.go) by bit.
//
// PlayerData, reader 0x1008d4e40 (70755):
//
//   - After the last mask block, in the same bit stream, one bit is kept at
//     [sp+0x38] (str at 0x1008d4fe0). Set, the quest log entries
//     (0x1008d78d4) and the QuestLogQuestIdToIndex map (0x1008d6994) are
//     read as full values instead of nested deltas. Then, inside group 0,
//     bit 1 → one bit at +0x340 (HasQuestSession), bit 2 → one bit at +0x341
//     (HasLevelLink). Bits 3..8 (dynamic lists) follow in the same stream
//     and stay unsupported.
//   - Bit 26, QuestLogQuestIdToIndex (+0xF0), between GuildTimeStamp (25)
//     and PlayerTitle (27): full flag → 0x1008d4d24, else 0x1008d7fcc.
//   - Bits 58+i, i < 175, QuestLog[i] (+0x470 + 0x48 i), after PartyType,
//     group 57 (loop 0x1008d77d0): full flag → 0x1008e2dac, else 0x1008e3080.
//   - Bits 234+i, i < 19, VisibleItems[i] (+0x35A8 + 0x20 i), group 233
//     (loop 0x1008d798c): 0x1008e2974 whatever the flag.
//   - Bits 261+i, i < 32, ForcedReactions[i] (+0x3820 + 8 i), group 260
//     (loop 0x1008d7be4), after AvgItemLevel: 0x1008e33a0.

func init() {
	pd := &playerDataDelta
	pd.preludeFn = playerDataPrelude
	nested := []deltaField{{bit: 26, read: mapDelta("QuestLogQuestIdToIndex.", "Key", "Value")}}
	for i := 0; i < 175; i++ {
		nested = append(nested, deltaField{bit: 58 + i, read: questLogDelta(i)})
	}
	for i := 0; i < 19; i++ {
		nested = append(nested, deltaField{bit: 234 + i, read: visibleItemDelta("VisibleItems.", i)})
	}
	for i := 0; i < 32; i++ {
		nested = append(nested, deltaField{bit: 261 + i, read: forcedReactionDelta(i)})
	}
	splice(pd, nested)
	for _, b := range []int{1, 2} {
		delete(pd.unsupported, b)
	}
}

// splice inserts nested readers into l's read order: each before the first
// field (not a trailer) with a higher bit, and marks their bits supported.
func splice(l *deltaLayout, nested []deltaField) {
	for _, n := range nested {
		at := len(l.fields)
		for i, f := range l.fields {
			if !f.trailer && f.bit > n.bit {
				at = i
				break
			}
		}
		l.fields = append(l.fields[:at], append([]deltaField{n}, l.fields[at:]...)...)
		delete(l.unsupported, n.bit)
	}
}

// playerDataPrelude: the full-values flag, then group 0's bits 1 and 2, in
// the mask's bit stream (0x1008d4fb0..0x1008d5198).
func playerDataPrelude(d *rd, m mask, out *fieldSet) {
	d.fullNested = d.bitsTraced(1) != 0
	if playerDataDelta.effective(m, 1) {
		out.add(Field{Name: "HasQuestSession", Width: WBit, Value: d.bitsTraced(1) != 0})
	}
	if playerDataDelta.effective(m, 2) {
		out.add(Field{Name: "HasLevelLink", Width: WBit, Value: d.bitsTraced(1) != 0})
	}
}

// val reads one byte-aligned value and records it.
func (d *rd) val(out *fieldSet, name string, w Width) {
	d.traceRead(getterOf(w), 0)
	out.add(Field{Name: name, Width: w, Value: d.value(w)})
}

// mapDelta reads an int32 → int32 map's delta: PlayerData bit 26
// QuestLogQuestIdToIndex (+0xF0) and UnitData bit 143 (+0x368, keys and
// values named A and B as in the create). With the struct's full-values
// flag set the reader calls 0x1008d4d24 directly; otherwise 0x1008d7fcc: a
// u8, non-zero → 0x1008d4d24; else a u16 change count and per change an
// i32 key, a u8 state and, unless the state is 2 (deleted, 0x1008d80c4), an
// i32 value. 0x1008d4d24 clears the map, then reads a u32 count and count ×
// (i32 key, i32 value), the create's reader.
func mapDelta(prefix, key, value string) func(d *rd, _ mask, out *fieldSet) {
	full := func(d *rd, out *fieldSet) {
		d.traceRead("4", 0)
		n := d.u32()
		out.add(Field{Name: prefix + "size", Width: WU32, Value: n})
		if uint64(n)*8 > uint64(d.r.Remaining()) {
			d.fail(fmt.Errorf("%w: %s%d entries in %d bytes", ErrMalformed, prefix, n, d.r.Remaining()))
			return
		}
		for i := 0; i < int(n) && d.err == nil; i++ {
			d.val(out, prefix+key, WI32)
			d.val(out, prefix+value, WI32)
		}
	}
	return func(d *rd, _ mask, out *fieldSet) {
		if d.fullNested {
			full(d, out)
			return
		}
		d.val(out, prefix+"Full", WU8)
		if v, _ := out.get(prefix + "Full"); v != uint8(0) {
			full(d, out)
			return
		}
		d.traceRead("2", 0)
		n := d.u16()
		out.add(Field{Name: prefix + "changes", Width: WU16, Value: n})
		if int(n)*5 > d.r.Remaining() {
			d.fail(fmt.Errorf("%w: %s%d changes in %d bytes", ErrMalformed, prefix, n, d.r.Remaining()))
			return
		}
		for i := 0; i < int(n) && d.err == nil; i++ {
			d.val(out, prefix+key, WI32)
			d.val(out, prefix+"State", WU8)
			if v, _ := out.get(prefix + "State"); v != uint8(MapDeleted) {
				d.val(out, prefix+value, WI32)
			}
		}
	}
}

// MapDeleted is the state byte of a map change that removes its key.
const MapDeleted = 2

// questLogDelta is QuestLog[i].
//
// Delta, 0x1008e3080: a byte; its top bit says whether mask block 0
// follows, then 32 bits (0x1008367bc). Bit 0 groups 1 QuestID i32 (+0),
// 2 StateFlags u16 (+4), 3 EndTime i64 (+8), 4 ObjectiveFlags u32 (+0x10),
// 5 EnabledObjectivesMask u32 (+0x14); bit 6 (tested on its own) groups
// 7+j, j < 24, ObjectiveProgress[j] i16 (+0x18 + 2j).
//
// Full, 0x1008e2dac: QuestID, StateFlags, 24 × ObjectiveProgress, EndTime,
// ObjectiveFlags, EnabledObjectivesMask (the create's order).
func questLogDelta(i int) func(d *rd, _ mask, out *fieldSet) {
	n := func(f string) string { return idx("QuestLog."+f, i) }
	prog := func(j int) string {
		return "QuestLog.ObjectiveProgress[" + strconv.Itoa(i) + "][" + strconv.Itoa(j) + "]"
	}
	return func(d *rd, _ mask, out *fieldSet) {
		if d.fullNested {
			d.val(out, n("QuestID"), WI32)
			d.val(out, n("StateFlags"), WU16)
			for j := 0; j < 24; j++ {
				d.val(out, prog(j), WI16)
			}
			d.val(out, n("EndTime"), WI64)
			d.val(out, n("ObjectiveFlags"), WU32)
			d.val(out, n("EnabledObjectivesMask"), WU32)
			return
		}
		d.group()
		var m uint32
		if d.bitsTraced(1) != 0 {
			m = uint32(d.bitsTraced(32))
		}
		if m&1 != 0 {
			for b, f := range []struct {
				name string
				w    Width
			}{{"QuestID", WI32}, {"StateFlags", WU16}, {"EndTime", WI64}, {"ObjectiveFlags", WU32}, {"EnabledObjectivesMask", WU32}} {
				if m&(1<<uint(b+1)) != 0 {
					d.val(out, n(f.name), f.w)
				}
			}
		}
		if m&(1<<6) != 0 {
			for j := 0; j < 24; j++ {
				if m&(1<<uint(7+j)) != 0 {
					d.val(out, prog(j), WI16)
				}
			}
		}
	}
}

// visibleItemDelta is one VisibleItem, 0x1008e2974: a 12-bit mask
// (0x1006ca7e8) on a new byte; nothing unless bit 0. Then in the same bit
// stream 1 → HasTransmog (+0x14), 2 → HasIllusion (+0x15); then values:
// 3 ItemID i32 (+0), 4 SecondaryItemModifiedAppearanceID i32 (+4),
// 5 ConditionalItemAppearanceID i32 (+8), 6 Unk_0xC i32 (+0xC),
// 7 ItemAppearanceModID u16 (+0x10), 8 ItemVisual u16 (+0x12),
// 9 ItemModifiedAppearanceID u32 (+0x18), 10 TransmogSlotOption u8 (+0x1C),
// 11 SheatheCategory u8 (+0x1D). Names follow the create (visibleItem).
func visibleItemDelta(prefix string, i int) func(d *rd, _ mask, out *fieldSet) {
	n := func(f string) string { return idx(prefix+f, i) }
	return func(d *rd, _ mask, out *fieldSet) {
		d.group()
		m := d.bitsTraced(12)
		if m&1 == 0 {
			return
		}
		if m&(1<<1) != 0 {
			out.add(Field{Name: n("HasTransmog"), Width: WBit, Value: d.bitsTraced(1) != 0})
		}
		if m&(1<<2) != 0 {
			out.add(Field{Name: n("HasIllusion"), Width: WBit, Value: d.bitsTraced(1) != 0})
		}
		for b, f := range []struct {
			name string
			w    Width
		}{
			{"ItemID", WI32}, {"SecondaryItemModifiedAppearanceID", WI32}, {"ConditionalItemAppearanceID", WI32},
			{"Unk_0xC", WI32}, {"ItemAppearanceModID", WU16}, {"ItemVisual", WU16},
			{"ItemModifiedAppearanceID", WU32}, {"TransmogSlotOption", WU8}, {"SheatheCategory", WU8},
		} {
			if m&(1<<uint(b+3)) != 0 {
				d.val(out, n(f.name), f.w)
			}
		}
	}
}

// forcedReactionDelta is ForcedReactions[i], 0x1008e33a0: a 3-bit mask on a
// new byte (0x1008346ac); bit 0 groups 1 FactionID i32 (+0), 2 Reaction i32
// (+4). Names follow the create.
func forcedReactionDelta(i int) func(d *rd, _ mask, out *fieldSet) {
	return func(d *rd, _ mask, out *fieldSet) {
		d.group()
		m := d.bitsTraced(3)
		if m&1 == 0 {
			return
		}
		if m&(1<<1) != 0 {
			d.val(out, idx("ForcedReactions.FactionID", i), WI32)
		}
		if m&(1<<2) != 0 {
			d.val(out, idx("ForcedReactions.Reaction", i), WI32)
		}
	}
}
