package updateobject

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// Fragment data and the per-type chains (docs/EVIDENCE.md E20, E23).
//
// Create (0x102cddad0): for each fragment of the entity that is not a tag,
// an indirect fragment first has a u8 "active" byte (0x100036c2c, then
// 0x102cde284), then the fragment's create data. Of the updateable
// fragments only CGObject (wire id 3) is decoded; its create data is the
// type's chain of update-field structs:
//
//	TypeActivePlayer  0x101725d88: Object, Unit, Player, ActivePlayer
//	TypePlayer        0x1018cb440: Object, Unit, Player
//	TypeUnit          0x1019ea338: Object, Unit
//
// Values (0x102cde4a8): u8 owner, u8 ids-changed [u8 kind, fragment ids],
// then a change mask of ceil(n/8) bytes, LSB first, two bits per indirect
// updateable fragment (active, contents changed). For CGObject the update
// data is a u32 type mask, then the delta of each struct whose TypeID bit is
// set (0 ObjectData, 5 UnitData, 6 PlayerData, 7 ActivePlayerData; chains
// 0x101725e3c active player, 0x1018cb4c8 player, 0x1019ea398 unit). The client ignores
// other bits.

// isTagFragment reports a tag fragment (no data). The registry kind of tags
// is 4 (0x100597740); the 200..226 range is the fork's [public].
func isTagFragment(id uint8) bool { return id >= 200 && id <= 226 }

// ObjectData is the decoded ObjectData struct.
type ObjectData struct {
	EntryID      int32
	DynamicFlags uint32
	Scale        float32
}

// UnitData holds the UnitData fields this package models; Fields has every
// decoded field in read order, so nothing is dropped.
type UnitData struct {
	DisplayID       int32
	Target          GUID
	Race            uint8
	ClassID         uint8
	Sex             uint8
	DisplayPower    uint8
	Health          int64
	MaxHealth       int64
	Level           int32
	FactionTemplate int32
	// Power and MaxPower are indexed by the class's power slot, not by power
	// type: mapping a slot to a type needs the class's power table.
	Power    [10]int32
	MaxPower [10]int32
	Flags    uint32
	// VirtualItems are the three weapon slots a unit shows (+0x468).
	VirtualItems [3]VisibleItem
	Fields       []Field
}

// PlayerData holds the PlayerData fields this package models. QuestLog and
// QuestLogQuestIDToIndex are visible to the owner and party members only;
// they stay zero/nil when the create did not carry them.
type PlayerData struct {
	Name        string
	PlayerFlags uint32
	// QuestLog is the 175-entry quest log (+0x470, 0x48 each).
	QuestLog [175]QuestLogEntry
	// QuestLogQuestIDToIndex maps a quest ID to its QuestLog index (+0xF0).
	QuestLogQuestIDToIndex map[int32]int32
	// VisibleItems are the 19 equipment slots as others see them (+0x35A8).
	VisibleItems [19]VisibleItem
	Fields       []Field
}

// QuestLogEntry is one QuestLog slot (client readers 0x1008e2dac full,
// 0x1008e3080 delta).
type QuestLogEntry struct {
	QuestID               int32
	StateFlags            uint16
	ObjectiveProgress     [24]int16
	EndTime               int64
	ObjectiveFlags        uint32
	EnabledObjectivesMask uint32
}

// VisibleItem is one VisibleItem (client readers 0x1008e2858 create,
// 0x1008e2974 delta); UnitData's VirtualItems share it.
type VisibleItem struct {
	ItemID                            int32
	SecondaryItemModifiedAppearanceID int32
	ConditionalItemAppearanceID       int32
	Unk0xC                            int32
	ItemAppearanceModID               uint16
	ItemVisual                        uint16
	ItemModifiedAppearanceID          uint32
	TransmogSlotOption                uint8
	SheatheCategory                   uint8
	HasTransmog                       bool
	HasIllusion                       bool
}

// ActivePlayerData is not decoded yet. In a create it is the last thing in
// the block (CGObject is the only fragment with data, and ActivePlayerData
// ends its chain), so its bytes are kept exactly.
type ActivePlayerData struct {
	Raw []byte
}

// GameObjectData holds the GameObjectData fields this package models.
type GameObjectData struct {
	DisplayID       int32
	Flags           uint32
	ParentRotation  Quat
	FactionTemplate int32
	State           int8
	TypeID          int8
	PercentHealth   uint8
	ArtKit          uint32
	Level           int32
	CreatedBy       GUID
	Fields          []Field
}

// ItemData holds the ItemData fields this package models. The owner-only
// ones (StackCount, Durability, MaxDurability) are zero unless the create
// carried the owner visibility flag.
type ItemData struct {
	Owner         GUID
	ContainedIn   GUID
	Creator       GUID
	StackCount    uint32
	DynamicFlags  uint32
	Durability    uint32
	MaxDurability uint32
	// Enchantments are the 14 enchantment slots (+0x180, 0xC each; the
	// delta reader 0x1008c862c).
	Enchantments [14]ItemEnchantment
	Fields       []Field
}

// ItemEnchantment is one item enchantment slot.
type ItemEnchantment struct {
	ID       int32
	Duration uint32
	Charges  int16
	Inactive uint16
}

// VendorData is the FVendor_C fragment (wire id 18).
type VendorData struct {
	Flags int32
}

// ObjectFields is a decoded create: the CGObject fragment's structs and any
// other updateable fragment.
type ObjectFields struct {
	Object       *ObjectData
	Unit         *UnitData
	Player       *PlayerData
	ActivePlayer *ActivePlayerData
	GameObject   *GameObjectData
	Item         *ItemData
	Vendor       *VendorData
	// Other has ObjectData's fields in read order (ObjectData has no
	// unmodelled ones today; kept for symmetry).
	Other []Field
}

// Wire ids of the updateable fragments this package decodes. The registry
// entry's kind byte (+0x1c) is 1 for CGObject (indirect: an active byte
// precedes its data) and 0 for FVendor_C.
const FragmentVendor uint8 = 18

// DecodeFields decodes a create block's fragment data. The client reads the
// updateable fragments in id order (0x102cddad0), each preceded by an active
// byte if it is indirect.
func (c *CreateBlock) DecodeFields() (*ObjectFields, error) {
	ids := append([]uint8(nil), c.Fragments...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		switch {
		case id == FragmentCGObject, id == FragmentVendor, isTagFragment(id):
		default:
			return nil, fmt.Errorf("%w: entity fragment %d", ErrUnsupported, id)
		}
	}
	d := &rd{r: codec.NewReader(c.FragmentData)}
	of := &ObjectFields{}
	for _, id := range ids {
		switch id {
		case FragmentCGObject:
			active := d.u8()
			if d.err != nil {
				return nil, fmt.Errorf("%w: CGObject active byte: %v", ErrMalformed, d.err)
			}
			if active == 0 {
				continue
			}
			if err := decodeChain(d, c.ObjectType, c.FieldFlags, of); err != nil {
				return nil, err
			}
		case FragmentVendor:
			var fs fieldSet
			d.readLayout(vendorDataCreate, c.FieldFlags, &fs)
			if d.err != nil {
				return nil, fmt.Errorf("%w: FVendor_C: %v", ErrMalformed, d.err)
			}
			of.Vendor = &VendorData{Flags: get[int32](&fs, "Flags")}
		}
	}
	if d.r.Remaining() != 0 {
		return nil, fmt.Errorf("%w: %d bytes after the fragments of a type %d create", ErrMalformed, d.r.Remaining(), c.ObjectType)
	}
	return of, nil
}

func decodeChain(d *rd, t ObjectType, flags uint8, of *ObjectFields) error {
	var parts [][]op
	switch t {
	case TypeActivePlayer, TypePlayer:
		parts = [][]op{objectDataCreate, unitDataCreate, playerDataCreate}
	case TypeUnit:
		parts = [][]op{objectDataCreate, unitDataCreate}
	case TypeGameObject:
		parts = [][]op{objectDataCreate, gameObjectDataCreate}
	case TypeItem:
		parts = [][]op{objectDataCreate, itemDataCreate}
	default:
		return fmt.Errorf("%w: update fields of object type %d", ErrUnsupported, t)
	}
	sets := make([]fieldSet, len(parts))
	for i, p := range parts {
		d.readLayout(p, flags, &sets[i])
		d.inBits = false
		if d.err != nil {
			return d.err
		}
	}
	of.Object, of.Other = objectFrom(&sets[0]), sets[0].list
	switch t {
	case TypeActivePlayer, TypePlayer, TypeUnit:
		of.Unit = unitFrom(&sets[1])
		if len(sets) > 2 {
			of.Player = playerFrom(&sets[2])
		}
	case TypeGameObject:
		of.GameObject = gameObjectFrom(&sets[1])
	case TypeItem:
		of.Item = itemFrom(&sets[1])
	}
	if t == TypeActivePlayer {
		// ActivePlayerData is not decoded: it ends the chain, and CGObject
		// is the only fragment with data in a player's create, so it is
		// the rest of the block.
		raw, _ := d.r.Bytes(d.r.Remaining())
		of.ActivePlayer = &ActivePlayerData{Raw: raw}
	}
	return nil
}

func gameObjectFrom(s *fieldSet) *GameObjectData {
	g := &GameObjectData{Fields: s.list}
	g.apply(s)
	return g
}

func (g *GameObjectData) apply(s *fieldSet) {
	set := func(name string, f func(any)) {
		if v, ok := s.get(name); ok {
			f(v)
		}
	}
	set("DisplayID", func(v any) { g.DisplayID = v.(int32) })
	set("Flags", func(v any) { g.Flags = v.(uint32) })
	set("ParentRotation.X", func(v any) { g.ParentRotation.X = v.(float32) })
	set("ParentRotation.Y", func(v any) { g.ParentRotation.Y = v.(float32) })
	set("ParentRotation.Z", func(v any) { g.ParentRotation.Z = v.(float32) })
	set("ParentRotation.W", func(v any) { g.ParentRotation.W = v.(float32) })
	set("FactionTemplate", func(v any) { g.FactionTemplate = v.(int32) })
	set("State", func(v any) { g.State = v.(int8) })
	set("TypeID", func(v any) { g.TypeID = v.(int8) })
	set("PercentHealth", func(v any) { g.PercentHealth = v.(uint8) })
	set("ArtKit", func(v any) { g.ArtKit = v.(uint32) })
	set("Level", func(v any) { g.Level = v.(int32) })
	set("CreatedBy", func(v any) { g.CreatedBy = v.(GUID) })
}

// ApplyTo updates the modelled GameObjectData fields from a delta.
func (g *GameObjectData) ApplyTo(fields []Field) {
	var s fieldSet
	for _, f := range fields {
		s.add(f)
	}
	g.apply(&s)
}

func itemFrom(s *fieldSet) *ItemData {
	it := &ItemData{Fields: s.list}
	it.apply(s)
	return it
}

func (it *ItemData) apply(s *fieldSet) {
	set := func(name string, f func(any)) {
		if v, ok := s.get(name); ok {
			f(v)
		}
	}
	set("Owner", func(v any) { it.Owner = v.(GUID) })
	set("ContainedIn", func(v any) { it.ContainedIn = v.(GUID) })
	set("Creator", func(v any) { it.Creator = v.(GUID) })
	set("StackCount", func(v any) { it.StackCount = v.(uint32) })
	set("DynamicFlags", func(v any) { it.DynamicFlags = v.(uint32) })
	set("Durability", func(v any) { it.Durability = v.(uint32) })
	set("MaxDurability", func(v any) { it.MaxDurability = v.(uint32) })
	for _, f := range s.list {
		base, ix := splitIndex(f.Name)
		if len(ix) != 1 || ix[0] >= len(it.Enchantments) {
			continue
		}
		e := &it.Enchantments[ix[0]]
		switch base {
		case "Enchantment.ID":
			e.ID = f.Value.(int32)
		case "Enchantment.Duration":
			e.Duration = f.Value.(uint32)
		case "Enchantment.Charges":
			e.Charges = f.Value.(int16)
		case "Enchantment.Inactive":
			e.Inactive = f.Value.(uint16)
		}
	}
}

// ApplyTo updates the modelled ItemData fields from a delta.
func (it *ItemData) ApplyTo(fields []Field) {
	var s fieldSet
	for _, f := range fields {
		s.add(f)
	}
	it.apply(&s)
}

func get[T any](s *fieldSet, name string) T {
	var zero T
	v, ok := s.get(name)
	if !ok {
		return zero
	}
	x, _ := v.(T)
	return x
}

func objectFrom(s *fieldSet) *ObjectData {
	return &ObjectData{
		EntryID:      get[int32](s, "EntryID"),
		DynamicFlags: get[uint32](s, "DynamicFlags"),
		Scale:        get[float32](s, "Scale"),
	}
}

func unitFrom(s *fieldSet) *UnitData {
	u := &UnitData{Fields: s.list}
	u.apply(s)
	return u
}

// apply copies the modelled fields present in s into u.
func (u *UnitData) apply(s *fieldSet) {
	set := func(name string, f func(any)) {
		if v, ok := s.get(name); ok {
			f(v)
		}
	}
	set("DisplayID", func(v any) { u.DisplayID = v.(int32) })
	set("Target", func(v any) { u.Target = v.(GUID) })
	set("Race", func(v any) { u.Race = v.(uint8) })
	set("ClassId", func(v any) { u.ClassID = v.(uint8) })
	set("Sex", func(v any) { u.Sex = v.(uint8) })
	set("DisplayPower", func(v any) { u.DisplayPower = v.(uint8) })
	set("Health", func(v any) { u.Health = v.(int64) })
	set("MaxHealth", func(v any) { u.MaxHealth = v.(int64) })
	set("Level", func(v any) { u.Level = v.(int32) })
	set("FactionTemplate", func(v any) { u.FactionTemplate = v.(int32) })
	set("Flags", func(v any) { u.Flags = v.(uint32) })
	for i := range u.Power {
		set(idx("Power", i), func(v any) { u.Power[i] = v.(int32) })
		set(idx("MaxPower", i), func(v any) { u.MaxPower[i] = v.(int32) })
	}
	for _, f := range s.list {
		base, ix := splitIndex(f.Name)
		if len(ix) == 1 && ix[0] < len(u.VirtualItems) && strings.HasPrefix(base, "VirtualItems.") {
			u.VirtualItems[ix[0]].set(base[len("VirtualItems."):], f.Value)
		}
	}
}

func playerFrom(s *fieldSet) *PlayerData {
	p := &PlayerData{Fields: s.list}
	p.apply(s)
	return p
}

func (p *PlayerData) apply(s *fieldSet) {
	if v, ok := s.get("Name"); ok {
		p.Name = v.(string)
	}
	if v, ok := s.get("PlayerFlags"); ok {
		p.PlayerFlags = v.(uint32)
	}
	p.applyNested(s.list)
}

// ApplyTo updates the modelled PlayerData fields from a delta (or a
// create's field list). Map changes are applied in order: a full map
// (QuestLogQuestIdToIndex.size) replaces it, a change with state 2
// removes its key.
func (p *PlayerData) ApplyTo(fields []Field) {
	var s fieldSet
	for _, f := range fields {
		s.add(f)
	}
	p.apply(&s)
}

func (p *PlayerData) applyNested(fields []Field) {
	const qm = "QuestLogQuestIdToIndex."
	var key int32
	var state uint8
	for _, f := range fields {
		switch f.Name {
		case qm + "size":
			p.QuestLogQuestIDToIndex = map[int32]int32{}
			state = 0
			continue
		case qm + "changes":
			if p.QuestLogQuestIDToIndex == nil {
				p.QuestLogQuestIDToIndex = map[int32]int32{}
			}
			continue
		case qm + "Key":
			key, state = f.Value.(int32), 0
			if p.QuestLogQuestIDToIndex == nil {
				p.QuestLogQuestIDToIndex = map[int32]int32{}
			}
			continue
		case qm + "State":
			if state = f.Value.(uint8); state == MapDeleted {
				delete(p.QuestLogQuestIDToIndex, key)
			}
			continue
		case qm + "Value":
			p.QuestLogQuestIDToIndex[key] = f.Value.(int32)
			continue
		}
		base, ix := splitIndex(f.Name)
		if len(ix) == 0 {
			continue
		}
		i := ix[0]
		switch {
		case strings.HasPrefix(base, "QuestLog.") && i < len(p.QuestLog):
			q := &p.QuestLog[i]
			switch base[len("QuestLog."):] {
			case "QuestID":
				q.QuestID = f.Value.(int32)
			case "StateFlags":
				q.StateFlags = f.Value.(uint16)
			case "EndTime":
				q.EndTime = f.Value.(int64)
			case "ObjectiveFlags":
				q.ObjectiveFlags = f.Value.(uint32)
			case "EnabledObjectivesMask":
				q.EnabledObjectivesMask = f.Value.(uint32)
			case "ObjectiveProgress":
				if len(ix) == 2 && ix[1] < len(q.ObjectiveProgress) {
					q.ObjectiveProgress[ix[1]] = f.Value.(int16)
				}
			}
		case strings.HasPrefix(base, "VisibleItems.") && i < len(p.VisibleItems):
			p.VisibleItems[i].set(base[len("VisibleItems."):], f.Value)
		}
	}
}

func (v *VisibleItem) set(name string, x any) {
	switch name {
	case "ItemID":
		v.ItemID = x.(int32)
	case "SecondaryItemModifiedAppearanceID":
		v.SecondaryItemModifiedAppearanceID = x.(int32)
	case "ConditionalItemAppearanceID":
		v.ConditionalItemAppearanceID = x.(int32)
	case "Unk_0xC":
		v.Unk0xC = x.(int32)
	case "ItemAppearanceModID":
		v.ItemAppearanceModID = x.(uint16)
	case "ItemVisual":
		v.ItemVisual = x.(uint16)
	case "ItemModifiedAppearanceID":
		v.ItemModifiedAppearanceID = x.(uint32)
	case "TransmogSlotOption":
		v.TransmogSlotOption = x.(uint8)
	case "SheatheCategory":
		v.SheatheCategory = x.(uint8)
	case "HasTransmog":
		v.HasTransmog = x.(bool)
	case "HasIllusion":
		v.HasIllusion = x.(bool)
	}
}

// splitIndex splits "A.B[1][2]" into "A.B" and [1, 2].
func splitIndex(name string) (string, []int) {
	i := strings.IndexByte(name, '[')
	if i < 0 {
		return name, nil
	}
	base, rest := name[:i], name[i:]
	var ix []int
	for len(rest) > 0 && rest[0] == '[' {
		j := strings.IndexByte(rest, ']')
		if j < 0 {
			return name, nil
		}
		n, err := strconv.Atoi(rest[1:j])
		if err != nil {
			return name, nil
		}
		ix = append(ix, n)
		rest = rest[j+1:]
	}
	return base, ix
}

// ObjectDelta is a decoded values block for CGObject: the changed fields of
// each struct, in read order. Nil means the struct did not change.
type ObjectDelta struct {
	Object     []Field
	Unit       []Field
	Player     []Field
	GameObject []Field
	Item       []Field
	// ActivePlayerRaw is the undecoded ActivePlayerData delta of the active
	// player (TypeID bit 7), nil when that bit is clear.
	ActivePlayerRaw []byte
	// TypeMask is the u32 of TypeID bits; bits the client ignores for this
	// object type are kept here and not decoded (they carry no data).
	TypeMask uint32
}

// ApplyTo updates the modelled UnitData fields from a delta.
func (u *UnitData) ApplyTo(fields []Field) {
	var s fieldSet
	for _, f := range fields {
		s.add(f)
	}
	u.apply(&s)
}

// deltaStep is one struct of an update chain: the TypeID bit that gates it.
type deltaStep struct {
	bit uint
	run func(d *rd, out *ObjectDelta)
}

// The update chains (E23, E31): a u32 TypeID mask, then each struct whose
// bit is set, in this order. The client tests only these bits per type.
var (
	stepObject = deltaStep{0, func(d *rd, o *ObjectDelta) { o.Object = d.readDelta(&objectDataDelta, nil) }}
	stepUnit   = deltaStep{5, func(d *rd, o *ObjectDelta) { o.Unit = d.readDelta(&unitDataDelta, unitDataPrelude) }}
	stepPlayer = deltaStep{6, func(d *rd, o *ObjectDelta) { o.Player = d.readDelta(&playerDataDelta, nil) }}
	// ActivePlayerData (0x10087b9e8) is not decoded. It is the last struct
	// of the chain and CGObject is the only fragment with data, so it is
	// the rest of the block: keep it raw, as the create does.
	stepActive = deltaStep{7, func(d *rd, o *ObjectDelta) {
		o.ActivePlayerRaw, _ = d.r.Bytes(d.r.Remaining())
		if o.ActivePlayerRaw == nil {
			o.ActivePlayerRaw = []byte{}
		}
	}}
	stepGameObject = deltaStep{8, func(d *rd, o *ObjectDelta) { o.GameObject = d.readDelta(&gameObjectDataDelta, nil) }}
	stepItem       = deltaStep{1, func(d *rd, o *ObjectDelta) { o.Item = d.readDelta(&itemDataDelta, nil) }}
)

var updateChains = map[ObjectType][]deltaStep{
	TypeActivePlayer: {stepObject, stepUnit, stepPlayer, stepActive}, // 0x101725e3c
	TypePlayer:       {stepObject, stepUnit, stepPlayer},             // 0x1018cb4c8
	TypeUnit:         {stepObject, stepUnit},                         // 0x1019ea398
	TypeGameObject:   {stepObject, stepGameObject},                   // 0x1017ffd18
	TypeItem:         {stepObject, stepItem},                         // after 0x10183d678
}

// DecodeValues decodes a values block for an object of type t whose
// fragments are CGObject and tags (as every decoded create has).
//
// On an error past the header, the returned delta still holds the structs
// that decoded completely before the failure (the chain reads them in
// order, so they are exact); the caller may apply them.
func DecodeValues(t ObjectType, v *ValuesBlock) (*ObjectDelta, error) {
	d := &rd{r: codec.NewReader(v.Data)}
	_ = d.u8() // owner: the client uses it only for the fragment list
	if d.u8() != 0 {
		kind := d.u8()
		if d.err == nil && kind == 1 {
			return nil, fmt.Errorf("%w: partial fragment-id update (0x102cde940)", ErrUnsupported)
		}
		for d.err == nil {
			id := d.u8()
			if d.err != nil || id == FragmentEnd {
				break
			}
			if id != FragmentCGObject && !isTagFragment(id) {
				return nil, fmt.Errorf("%w: entity fragment %d", ErrUnsupported, id)
			}
		}
	}
	changed := d.u8() // CGObject: bit 0 active, bit 1 contents changed
	if d.err != nil {
		return nil, fmt.Errorf("%w: values header: %v", ErrMalformed, d.err)
	}
	if changed&1 == 0 {
		return nil, fmt.Errorf("%w: values block deactivating CGObject", ErrUnsupported)
	}
	if changed&^3 != 0 {
		// CGObject has the lowest id of the updateable fragments, so its
		// bits are 0 and 1; higher bits belong to another fragment (FVendor_C
		// is bit 2 on a vendor), whose list this function is not given.
		return nil, fmt.Errorf("%w: change mask %#02x has bits for fragments after CGObject", ErrUnsupported, changed)
	}
	out := &ObjectDelta{}
	if changed&2 != 0 {
		out.TypeMask = d.u32()
		chain, ok := updateChains[t]
		if !ok {
			return nil, fmt.Errorf("%w: values of object type %d", ErrUnsupported, t)
		}
		for _, st := range chain {
			if d.err != nil {
				break
			}
			if out.TypeMask&(1<<st.bit) != 0 {
				st.run(d, out)
			}
		}
	}
	if d.err != nil {
		return out, d.err
	}
	if d.r.Remaining() != 0 {
		return out, fmt.Errorf("%w: %d bytes after the values", ErrMalformed, d.r.Remaining())
	}
	return out, nil
}
