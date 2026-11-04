package updateobject

import (
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// Synthetic deltas for the UnitData, GameObjectData and ItemData nested
// parts (delta_lists.go); none occurs in the captures. The client's readers
// run over them in the emulator with the PlayerData ones
// (nested70755_test.go).

// bw is a writer that knows its bit position, to mirror the client's
// choice between whole u32 mask words and 32-bit reads.
type bw struct {
	w    *codec.Writer
	bits int // bits used in the current byte, 0..7
}

func (b *bw) Bits(v uint64, n int) {
	_ = b.w.Bits(v, uint(n))
	b.bits = (b.bits + n) % 8
}
func (b *bw) Bit(v bool) {
	x := uint64(0)
	if v {
		x = 1
	}
	b.Bits(x, 1)
}
func (b *bw) Flush()            { b.w.FlushBits(); b.bits = 0 }
func (b *bw) U8(v uint8)        { b.w.Uint8(v); b.bits = 0 }
func (b *bw) U16(v uint16)      { b.w.Uint16(v); b.bits = 0 }
func (b *bw) U32(v uint32)      { b.w.Uint32(v); b.bits = 0 }
func (b *bw) U64(v uint64)      { b.w.Uint64(v); b.bits = 0 }
func (b *bw) Raw(p []byte)      { b.w.RawBytes(p); b.bits = 0 }
func (b *bw) GUID(g codec.GUID) { b.w.GUID(g); b.bits = 0 }
func (b *bw) aligned() bool     { return b.bits == 0 }

// mask writes l's change mask for bits and their group bits.
func (b *bw) mask(l *deltaLayout, bits []int) {
	n := l.blocks
	if n == 0 {
		n = 1
	}
	m := make(mask, n)
	set := func(x int) { m[x/32] |= 1 << uint(x%32) }
	for _, x := range bits {
		set(x)
		if g, ok := l.groupOf(x); ok {
			set(g)
		}
	}
	if l.blocks == 0 {
		b.Bits(uint64(m[0]), l.plainBits)
		return
	}
	var present uint64
	for k := range m {
		if m[k] != 0 {
			present |= 1 << uint(k)
		}
	}
	b.Bits(present, l.blocks)
	for k := range m {
		if m[k] != 0 {
			b.Bits(uint64(m[k]), 32)
		}
	}
}

// dynMask writes a dynamic list's size and mask (readDynMask's mirror).
func (b *bw) dynMask(n int, changed []int) {
	b.Bits(uint64(n), 32)
	b.dynMaskWords(n, changed)
}

func (b *bw) dynMaskWords(n int, changed []int) {
	words := (n + 31) / 32
	m := make([]uint32, words)
	for _, i := range changed {
		m[i/32] |= 1 << uint(i%32)
	}
	rem, k := n, 0
	if n > 32 && words > 0 {
		aligned := b.aligned()
		for k < words {
			prev := rem
			if aligned {
				b.U32(m[k])
			} else {
				b.Bits(uint64(m[k]), 32)
			}
			k++
			rem = prev - 32
			if prev < 65 {
				break
			}
		}
	}
	if rem != 0 && k < words {
		b.Bits(uint64(m[k]), rem)
	}
}

func (b *bw) visibleItem(c *counter, m uint32) {
	b.Flush()
	b.Bits(uint64(m), 12)
	if m&1 != 0 {
		if m&2 != 0 {
			b.Bit(true)
		}
		if m&4 != 0 {
			b.Bit(true)
		}
	}
	b.Flush()
	if m&1 == 0 {
		return
	}
	for i, n := range []int{4, 4, 4, 4, 2, 2, 4, 1, 1} {
		if m&(1<<uint(i+3)) != 0 {
			b.val(c, n)
		}
	}
}

func (b *bw) val(c *counter, n int) {
	switch n {
	case 1:
		b.U8(uint8(c.next()))
	case 2:
		b.U16(uint16(c.next()))
	case 4:
		b.U32(c.next())
	case 8:
		b.U64(uint64(c.next())<<32 | uint64(c.next()))
	}
}

func build(f func(b *bw)) []byte {
	b := &bw{w: codec.NewWriter()}
	f(b)
	return b.w.Bytes()
}

// unitPrelude writes UnitData's bits after the mask.
func (b *bw) unitPrelude(full bool, f314 *bool, swe []uint32) {
	b.Flush()
	b.Bit(full)
	if f314 != nil {
		b.Bit(*f314)
	}
	if swe != nil {
		b.Bits(uint64(len(swe)), 32)
		for _, v := range swe {
			b.U32(v)
		}
	}
}

func syntheticListDeltas(t *testing.T) []nestedCase {
	var c counter
	ud, gd, id := &unitDataDelta, &gameObjectDataDelta, &itemDataDelta
	yes := true
	cases := []nestedCase{
		{name: "UnitData/synthetic/lists-full-flag", b: build(func(b *bw) {
			b.mask(ud, []int{1, 2, 3, 4, 5, 6, 143, 151, 194, 196})
			b.unitPrelude(true, &yes, []uint32{c.next(), c.next(), c.next()})
			b.Flush()
			b.dynMask(40, []int{0, 33, 39}) // aligned: a whole word, then 8 bits
			b.dynMask(5, []int{1, 4})       // unaligned now
			b.dynMask(70, []int{0, 69})     // unaligned: 32-bit words as bits
			for range 3 {
				b.U32(c.next())
				b.U32(c.next())
			}
			b.U32(c.next())
			b.U32(c.next())
			b.GUID(codec.GUID{Lo: uint64(c.next()), Hi: uint64(c.next()) << 32})
			b.GUID(codec.GUID{Lo: 0x1122334455, Hi: 0x0801180000000000})
			b.U32(c.next()) // DisplayID (6)
			b.U32(2)        // bit 143, full map: count, pairs
			for range 2 {
				b.U32(c.next())
				b.U32(c.next())
			}
			b.U8(0x80) // HasAssistActionData
			b.Flush()
			b.Bits(0xf, 4) // Type, VirtualRealmAddress, name
			b.Flush()
			b.U8(uint8(c.next()))
			b.U32(c.next())
			b.U8(3 << 2)
			b.Raw([]byte("Bob"))
			b.visibleItem(&c, 0xfff)
			b.visibleItem(&c, 0x0e9)
		})},
		{name: "UnitData/synthetic/lists-changes", b: build(func(b *bw) {
			b.mask(ud, []int{3, 4, 5, 143, 151, 195})
			b.unitPrelude(false, nil, nil)
			b.Flush()
			b.dynMask(64, []int{31, 32, 63}) // a word, then 32 bits
			b.dynMask(0, nil)
			b.dynMask(33, []int{32})
			for range 3 {
				b.U32(c.next())
				b.U32(c.next())
			}
			b.GUID(codec.GUID{Lo: 7})
			b.U8(0) // map: changes
			b.U16(3)
			for _, st := range []uint8{1, 2, 0} {
				b.U32(c.next())
				b.U8(st)
				if st != MapDeleted {
					b.U32(c.next())
				}
			}
			b.U8(0) // no AssistActionData: bit 151 reads nothing
			b.visibleItem(&c, 0x006)
		})},
		{name: "UnitData/synthetic/assist-name-only", b: build(func(b *bw) {
			b.mask(ud, []int{151})
			b.unitPrelude(false, nil, nil)
			b.U8(0x80)
			b.Flush()
			b.Bits(0x5, 4)
			b.Flush()
			b.U8(0)
		})},
		{name: "GameObjectData/synthetic/lists-assist", b: build(func(b *bw) {
			b.mask(gd, []int{1, 2, 3, 4, 27})
			b.Bits(2, 32) // StateWorldEffectIDs, in the mask's stream
			b.U32(c.next())
			b.U32(c.next())
			b.Flush()
			b.dynMask(3, []int{0, 2})
			b.dynMask(40, []int{39})
			b.U32(c.next())
			b.U32(c.next())
			b.U32(c.next())
			b.U32(c.next()) // DisplayID (4)
			b.U8(0x80)
			b.Flush()
			b.Bits(3, 6)
			b.Bits(5, 11)
			b.Flush()
			b.U32(c.next())
			b.U8(1)
			b.U64(uint64(c.next()))
			b.U32(c.next())
			b.Raw([]byte("Ann"))
			b.Raw([]byte("Wolf\x00"))
		})},
		{name: "GameObjectData/synthetic/no-assist", b: build(func(b *bw) {
			b.mask(gd, []int{1, 27})
			b.Bits(0, 32)
			b.U8(0)
		})},
		{name: "ItemData/synthetic/nested", b: build(func(b *bw) {
			b.mask(id, []int{1, 2, 7, 17, 19, 22, 28, 41})
			b.dynMask(2, []int{0, 1})
			b.dynMask(33, []int{0, 32})
			for range 2 {
				b.U16(uint16(c.next()))
				b.U8(uint8(c.next()))
				b.U8(uint8(c.next()))
			}
			for _, gm := range []uint32{0x7fffffff, 1 | 1<<3 | 1<<19} {
				b.Flush()
				b.Bit(true)
				b.Bits(uint64(gm), 32)
				b.Flush()
				if gm&1 != 0 {
					if gm&2 != 0 {
						b.U32(c.next())
					}
					if gm&4 != 0 {
						b.U8(uint8(c.next()))
					}
				}
				for j := range 16 {
					if gm&8 != 0 && gm&(1<<uint(4+j)) != 0 {
						b.U16(uint16(c.next()))
					}
				}
			}
			b.U32(c.next()) // StackCount (7)
			b.U8(0x80 | 5)  // Modifiers: 5, mask of 5 bits
			b.Flush()
			b.dynMaskWords(5, []int{0, 4})
			for range 2 {
				b.U8(uint8(c.next()))
				b.U32(c.next())
			}
			b.U32(c.next()) // ItemBonusKey: ItemID, 2 bonus lists, 1 modification
			b.U32(2)
			b.U32(1)
			b.U32(c.next())
			b.U32(c.next())
			b.U8(uint8(c.next()))
			b.U32(c.next())
			b.U32(c.next()) // SpellCharges[0] (22)
			b.Flush()
			b.Bits(0x1f, 5) // Enchantment[0]: all
			b.Flush()
			b.U32(c.next())
			b.U32(c.next())
			b.U16(uint16(c.next()))
			b.U16(uint16(c.next()))
			b.Bits(0x11, 5) // Enchantment[13]: Inactive
			b.Flush()
			b.U16(uint16(c.next()))
		})},
		{name: "ItemData/synthetic/quiet", b: build(func(b *bw) {
			b.mask(id, []int{2, 17, 30})
			b.dynMask(1, []int{0})
			b.Flush()
			b.Bit(false) // gem: no mask block
			b.Flush()
			b.U8(0x05) // Modifiers: top bit clear, nothing more
			b.Bits(0x1e, 5)
			b.Flush()
		})},
		{name: "ItemData/synthetic/modifiers-100", b: build(func(b *bw) {
			b.mask(id, []int{17})
			b.U8(0x80 | 100)
			b.Flush()
			b.dynMaskWords(100, []int{0, 50, 99})
			for range 3 {
				b.U8(uint8(c.next()))
				b.U32(c.next())
			}
		})},
	}
	return cases
}

// TestListDeltasDecode checks decoded values of the synthetic cases (their
// reads are checked against the client in TestNestedAgainstClient).
func TestListDeltasDecode(t *testing.T) {
	byName := map[string][]byte{}
	for _, c := range syntheticListDeltas(t) {
		byName[c.name] = c.b
	}
	dec := func(name string, l *deltaLayout) map[string]any {
		d := &rd{r: codec.NewReader(byName[name])}
		fs := d.readDelta(l, nil)
		if d.err != nil || d.r.Remaining() != 0 {
			t.Fatalf("%s: %v, %d left", name, d.err, d.r.Remaining())
		}
		return fieldMap(fs)
	}
	u := dec("UnitData/synthetic/lists-full-flag", &unitDataDelta)
	if u["AssistActionData.PlayerName"] != "Bob" || u["StateWorldEffectIDs.size"] != uint32(3) ||
		u["ChannelObjects[69]"] != (GUID{Lo: 0x1122334455, Hi: 0x0801180000000000}) || u["Unk_0x368.size"] != uint32(2) {
		t.Errorf("unit: %v", u)
	}
	g := dec("GameObjectData/synthetic/lists-assist", &gameObjectDataDelta)
	if g["AssistActionData.PlayerName"] != "Ann" || g["AssistActionData.MonsterName"] != "Wolf" || g["WorldEffects.size"] != uint32(40) {
		t.Errorf("game object: %v", g)
	}
	d := &rd{r: codec.NewReader(byName["ItemData/synthetic/nested"])}
	fs := d.readDelta(&itemDataDelta, nil)
	if d.err != nil {
		t.Fatal(d.err)
	}
	it := &ItemData{}
	it.ApplyTo(fs)
	if e := it.Enchantments[0]; e.ID == 0 || e.Duration == 0 || e.Charges == 0 || e.Inactive == 0 {
		t.Errorf("enchantment 0: %+v", e)
	}
	if e := it.Enchantments[13]; e.ID != 0 || e.Inactive == 0 {
		t.Errorf("enchantment 13: %+v", e)
	}
	i := fieldMap(fs)
	if i["ItemBonusKey.BonusListIDs.size"] != uint32(2) || i["Modifiers.size"] != uint64(5) || i["Gems.BonusListIDs[32][15]"] == nil {
		t.Errorf("item: %v", i)
	}
}
