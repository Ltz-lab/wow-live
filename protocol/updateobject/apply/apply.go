// Package apply replays a decoded SMSG_UPDATE_OBJECT into the world model
// (package state).
//
// Mapping (docs/STATE_SCHEMA.md): position and facing come from the create
// block's movement section; entry from ObjectData; class, race, level,
// health, max health and target from UnitData; name from PlayerData. A game
// object's display, type, state, flags, faction and level come from
// GameObjectData and its rotation from the create block; an item's owner,
// container, stack count and durability from ItemData. The client's
// ThisIsYou flag sets the session's self GUID.
//
// A values block whose later struct cannot be decoded (an unsupported
// PlayerData bit, say) still applies the structs before it: the client reads
// them in a fixed order, so they are exact. The block is reported in
// Result.Skipped and in Result.Updated.
//
// Not mapped, on purpose:
//   - power: UnitData.Power[i] is indexed by the class's power slot, and the
//     slot -> power type table (ChrClassesXPowerTypes) is client data this
//     package does not have; state keys power by type name. The raw slots
//     are kept in updateobject.UnitData.
//   - auras: they are not update fields in this client (SMSG_AURA_UPDATE
//     carries them), so an update leaves an object's auras as they were.
//
// Objects are keyed by the full 128-bit wire GUID. The low half alone is not
// unique: players and creatures number their low halves independently, and
// in the 70755 Goldshire capture the player and a creature both have low
// half 3.
package apply

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/updateobject"
	"github.com/Ltz-lab/wow-live/state"
)

// Skip is a block (or part of one) that was not applied, and why.
type Skip struct {
	GUID updateobject.GUID
	Type updateobject.BlockType
	Err  error
}

func (s Skip) Error() string { return fmt.Sprintf("%v block for %v: %v", s.Type, s.GUID, s.Err) }

// Result reports what Apply did.
type Result struct {
	Created []state.GUID
	Updated []state.GUID
	Removed []state.GUID
	Skipped []Skip
}

// GUIDOf maps a wire GUID to the state key.
func GUIDOf(g updateobject.GUID) state.GUID { return state.GUID{Hi: g.Hi, Lo: g.Lo} }

var kinds = map[updateobject.ObjectType]state.Kind{
	updateobject.TypeItem:                 state.KindItem,
	updateobject.TypeContainer:            state.KindContainer,
	updateobject.TypeAzeriteEmpoweredItem: state.KindItem,
	updateobject.TypeAzeriteItem:          state.KindItem,
	updateobject.TypeUnit:                 state.KindUnit,
	updateobject.TypePlayer:               state.KindPlayer,
	updateobject.TypeActivePlayer:         state.KindPlayer,
	updateobject.TypeGameObject:           state.KindGameObject,
	updateobject.TypeDynamicObject:        state.KindDynamicObject,
	updateobject.TypeCorpse:               state.KindCorpse,
	updateobject.TypeAreaTrigger:          state.KindAreaTrigger,
	updateobject.TypeSceneObject:          state.KindSceneObject,
	updateobject.TypeConversation:         state.KindConversation,
}

// ErrNoKind marks an object type the state model has no kind for.
var ErrNoKind = errors.New("apply: object type has no state kind")

// ErrUnknownObject marks a values block for an object the state does not
// hold (the client skips these by their size too).
var ErrUnknownObject = errors.New("apply: values for an unknown object")

// Apply applies u to s. Blocks that cannot be applied are reported in
// Result.Skipped; the rest still apply.
func Apply(s *state.State, u *updateobject.UpdateObject) Result {
	var res Result
	if rm := u.Removed; rm != nil {
		self := s.Self()
		for _, list := range [][]updateobject.GUID{rm.Destroyed, rm.OutOfRange} {
			for _, g := range list {
				// The client never removes its own player this way
				// (0x102cd627c skips the active player's GUID).
				if k := GUIDOf(g); k != self && s.Remove(k) {
					res.Removed = append(res.Removed, k)
				}
			}
		}
	}
	for _, b := range u.Blocks {
		switch {
		case b.Create != nil:
			applyCreate(s, b, &res)
		case b.Values != nil:
			applyValues(s, b, &res)
		}
	}
	return res
}

func applyCreate(s *state.State, b updateobject.Block, res *Result) {
	c := b.Create
	kind, ok := kinds[c.ObjectType]
	if !ok {
		res.Skipped = append(res.Skipped, Skip{b.GUID, b.Type, fmt.Errorf("%w: %d", ErrNoKind, c.ObjectType)})
		return
	}
	key := GUIDOf(b.GUID)
	o, existed := s.Object(key)
	o.GUID, o.Kind = key, kind
	if m := c.Movement; m != nil {
		var p *updateobject.Pos4
		switch {
		case m.Movement != nil:
			p = &m.Movement.Info.Position
		case m.Stationary != nil:
			p = m.Stationary
		}
		if p != nil {
			o.Position = state.Vec3{X: p.X, Y: p.Y, Z: p.Z}
			o.Facing = p.O
		}
	}
	of, err := c.DecodeFields()
	if err != nil {
		res.Skipped = append(res.Skipped, Skip{b.GUID, b.Type, err})
	} else {
		applyFields(&o, of)
	}
	if kind == state.KindGameObject && c.Movement != nil && c.Movement.Rotation != nil {
		if o.GameObject == nil {
			o.GameObject = &state.GameObjectInfo{}
		}
		q := updateobject.UnpackRotation(*c.Movement.Rotation)
		o.GameObject.Rotation = state.Quat{X: q.X, Y: q.Y, Z: q.Z, W: q.W}
	}
	o.Updated = time.Time{} // Upsert stamps it
	s.Upsert(o)
	if c.Movement != nil && c.Movement.ThisIsYou {
		s.SetSelf(key)
	}
	if existed {
		res.Updated = append(res.Updated, key)
	} else {
		res.Created = append(res.Created, key)
	}
}

func applyFields(o *state.Object, of *updateobject.ObjectFields) {
	if of.Object != nil {
		o.Entry = uint32(of.Object.EntryID)
	}
	if of.Unit != nil {
		applyUnit(o, of.Unit.Fields)
	}
	if of.Player != nil && of.Player.Name != "" {
		o.Name = of.Player.Name
	}
	if of.Player != nil {
		applyPlayer(o, of.Player.Fields)
	}
	if of.GameObject != nil {
		if o.GameObject == nil {
			o.GameObject = &state.GameObjectInfo{}
		}
		applyGameObject(o.GameObject, of.GameObject.Fields)
	}
	if of.Item != nil {
		if o.Item == nil {
			o.Item = &state.ItemInfo{}
		}
		applyItem(o.Item, of.Item.Fields)
	}
}

func applyGameObject(g *state.GameObjectInfo, fields []updateobject.Field) {
	for _, f := range fields {
		switch f.Name {
		case "DisplayID":
			g.Display = uint32(f.Value.(int32))
		case "TypeID":
			g.Type = f.Value.(int8)
		case "State":
			g.State = f.Value.(int8)
		case "Flags":
			g.Flags = f.Value.(uint32)
		case "FactionTemplate":
			g.Faction = uint32(f.Value.(int32))
		case "Level":
			g.Level = f.Value.(int32)
		case "ArtKit":
			g.ArtKit = f.Value.(uint32)
		case "PercentHealth":
			g.PercentHealth = f.Value.(uint8)
		case "CreatedBy":
			g.CreatedBy = GUIDOf(f.Value.(updateobject.GUID))
		}
	}
}

func applyItem(it *state.ItemInfo, fields []updateobject.Field) {
	for _, f := range fields {
		switch f.Name {
		case "Owner":
			it.Owner = GUIDOf(f.Value.(updateobject.GUID))
		case "ContainedIn":
			it.ContainedIn = GUIDOf(f.Value.(updateobject.GUID))
		case "StackCount":
			it.StackCount = f.Value.(uint32)
		case "Durability":
			it.Durability = f.Value.(uint32)
		case "MaxDurability":
			it.MaxDurability = f.Value.(uint32)
		case "DynamicFlags":
			it.DynamicFlags = f.Value.(uint32)
		}
	}
}

// applyUnit copies the UnitData fields state models.
func applyUnit(o *state.Object, fields []updateobject.Field) {
	for _, f := range fields {
		switch f.Name {
		case "Health":
			o.Health = f.Value.(int64)
		case "MaxHealth":
			o.MaxHealth = f.Value.(int64)
		case "Level":
			if l := f.Value.(int32); l >= 0 && l <= 255 {
				o.Level = uint8(l)
			}
		case "ClassId":
			o.Class = f.Value.(uint8)
		case "Race":
			o.Race = f.Value.(uint8)
		case "Target":
			o.Target = GUIDOf(f.Value.(updateobject.GUID))
		case "NpcFlags":
			o.NpcFlags = f.Value.(uint32)
		}
	}
}

// valuesType is the object type a values block is decoded as. The state
// keeps the kind, not the create's object type; the kinds with more than
// one type are told apart by the self GUID (players) or default to the
// plain type (items).
func valuesType(s *state.State, key state.GUID, k state.Kind) (updateobject.ObjectType, bool) {
	switch k {
	case state.KindPlayer:
		if key == s.Self() {
			return updateobject.TypeActivePlayer, true
		}
		return updateobject.TypePlayer, true
	case state.KindUnit:
		return updateobject.TypeUnit, true
	case state.KindGameObject:
		return updateobject.TypeGameObject, true
	case state.KindItem:
		return updateobject.TypeItem, true
	}
	return 0, false
}

func applyValues(s *state.State, b updateobject.Block, res *Result) {
	key := GUIDOf(b.GUID)
	o, ok := s.Object(key)
	if !ok {
		res.Skipped = append(res.Skipped, Skip{b.GUID, b.Type, ErrUnknownObject})
		return
	}
	t, ok := valuesType(s, key, o.Kind)
	if !ok {
		res.Skipped = append(res.Skipped, Skip{b.GUID, b.Type, fmt.Errorf("%w: values for a %s", updateobject.ErrUnsupported, o.Kind)})
		return
	}
	d, err := updateobject.DecodeValues(t, b.Values)
	if err != nil {
		res.Skipped = append(res.Skipped, Skip{b.GUID, b.Type, err})
		if d == nil || (d.Object == nil && d.Unit == nil && d.GameObject == nil && d.Item == nil) {
			return
		}
	}
	if err := s.Mutate(key, func(o *state.Object) {
		for _, f := range d.Object {
			if f.Name == "EntryID" {
				o.Entry = uint32(f.Value.(int32))
			}
		}
		applyUnit(o, d.Unit)
		applyPlayer(o, d.Player)
		if d.GameObject != nil {
			if o.GameObject == nil {
				o.GameObject = &state.GameObjectInfo{}
			}
			applyGameObject(o.GameObject, d.GameObject)
		}
		if d.Item != nil {
			if o.Item == nil {
				o.Item = &state.ItemInfo{}
			}
			applyItem(o.Item, d.Item)
		}
	}); err != nil {
		res.Skipped = append(res.Skipped, Skip{b.GUID, b.Type, err})
		return
	}
	res.Updated = append(res.Updated, key)
}

// applyPlayer copies the PlayerData fields state models: the item id in each
// visible equipment slot ("VisibleItems.ItemID[n]").
func applyPlayer(o *state.Object, fields []updateobject.Field) {
	const pre = "VisibleItems.ItemID["
	for _, f := range fields {
		if !strings.HasPrefix(f.Name, pre) || !strings.HasSuffix(f.Name, "]") {
			continue
		}
		slot, err := strconv.Atoi(f.Name[len(pre) : len(f.Name)-1])
		if err != nil {
			continue
		}
		id, _ := f.Value.(int32)
		if id == 0 {
			delete(o.Equipment, slot)
			continue
		}
		if o.Equipment == nil {
			o.Equipment = map[int]int32{}
		}
		o.Equipment[slot] = id
	}
}
