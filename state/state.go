// Package state holds the persistent world model that agents query.
//
// The object model mirrors the client's own object manager, whose classes are
// visible in shipped strings: CGObject_C, CGUnit_C, CGPlayer_C,
// CGActivePlayer_C, CGItem_C, CGContainer_C, CGGameObject_C, CGDynamicObject_C,
// CGCorpse_C, CGAreaTrigger_C, CGConversation_C, CGSceneObject_C.
//
// Only fields an agent needs for decisions are modelled. Everything is
// versioned: a snapshot carries the state version it was taken at so an agent
// can tell whether it acted on stale data.
package state

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// GUID is a 128-bit object identifier, as the client keys objects. The low
// half alone is not unique: in a real session the player's counter and a
// creature's spawn counter collide (both 3 in the 70755 Goldshire capture),
// so the state is keyed by both halves. It marshals as 32 hex digits, high
// half first (the same text as codec.GUID.String).
type GUID struct {
	Hi uint64
	Lo uint64
}

// LowGUID is a GUID with only the low half set (synthetic fixtures).
func LowGUID(lo uint64) GUID { return GUID{Lo: lo} }

// IsZero reports the empty GUID.
func (g GUID) IsZero() bool { return g.Hi == 0 && g.Lo == 0 }

// Less orders GUIDs by high half, then low half.
func (g GUID) Less(o GUID) bool {
	if g.Hi != o.Hi {
		return g.Hi < o.Hi
	}
	return g.Lo < o.Lo
}

// String formats g as 32 hex digits, high half first.
func (g GUID) String() string { return fmt.Sprintf("%016x%016x", g.Hi, g.Lo) }

// MarshalText implements encoding.TextMarshaler.
func (g GUID) MarshalText() ([]byte, error) { return []byte(g.String()), nil }

// UnmarshalText accepts 1..32 hex digits (a short form is the low half).
func (g *GUID) UnmarshalText(b []byte) error {
	s := string(b)
	if len(s) == 0 || len(s) > 32 {
		return fmt.Errorf("state: bad guid %q", s)
	}
	var hi, lo uint64
	if len(s) > 16 {
		if _, err := fmt.Sscanf(s[:len(s)-16], "%x", &hi); err != nil {
			return fmt.Errorf("state: bad guid %q: %v", s, err)
		}
		s = s[len(s)-16:]
	}
	if _, err := fmt.Sscanf(s, "%x", &lo); err != nil {
		return fmt.Errorf("state: bad guid %q: %v", string(b), err)
	}
	g.Hi, g.Lo = hi, lo
	return nil
}

// Kind classifies an object, one-to-one with the client's object classes.
type Kind string

// Object kinds.
const (
	KindPlayer        Kind = "player"
	KindUnit          Kind = "unit"
	KindGameObject    Kind = "gameobject"
	KindItem          Kind = "item"
	KindContainer     Kind = "container"
	KindDynamicObject Kind = "dynamicobject"
	KindCorpse        Kind = "corpse"
	KindAreaTrigger   Kind = "areatrigger"
	KindConversation  Kind = "conversation"
	KindSceneObject   Kind = "sceneobject"
)

// Vec3 is a world position in yards.
type Vec3 struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Object is one entity in the world.
type Object struct {
	GUID      GUID             `json:"guid"`
	Kind      Kind             `json:"kind"`
	Entry     uint32           `json:"entry,omitempty"`
	Name      string           `json:"name,omitempty"`
	Level     uint8            `json:"level,omitempty"`
	Class     uint8            `json:"class,omitempty"`
	Race      uint8            `json:"race,omitempty"`
	Position  Vec3             `json:"position"`
	Facing    float32          `json:"facing"`
	Health    int64            `json:"health,omitempty"`
	MaxHealth int64            `json:"max_health,omitempty"`
	Power     map[string]int64 `json:"power,omitempty"`
	Target    GUID             `json:"target"`
	// NpcFlags is UnitData.NpcFlags (0x2: quest giver).
	NpcFlags uint32   `json:"npc_flags,omitempty"`
	Auras    []string `json:"auras,omitempty"`
	Dead     bool     `json:"dead,omitempty"`
	// Equipment is the item id shown in each visible slot (PlayerData
	// VisibleItems, 0-18), only the slots with an item.
	Equipment map[int]int32 `json:"equipment,omitempty"`
	// GameObject and Item are set for those kinds only.
	GameObject *GameObjectInfo `json:"gameobject,omitempty"`
	Item       *ItemInfo       `json:"item,omitempty"`
	Updated    time.Time       `json:"updated"`
}

// Quat is a rotation quaternion.
type Quat struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
	W float32 `json:"w"`
}

// GameObjectInfo is what an agent needs about a game object (door, chest,
// herb, mailbox, ...). Values are as the server sends them: State is the
// GOState (0 active/open, 1 ready/closed, 2 alternative), Type the
// GameObject type (e.g. 3 chest, 19 mailbox), Flags the GameObjectFlags.
type GameObjectInfo struct {
	Display       uint32 `json:"display"`
	Type          int8   `json:"type"`
	State         int8   `json:"state"`
	Flags         uint32 `json:"flags,omitempty"`
	Faction       uint32 `json:"faction,omitempty"`
	Level         int32  `json:"level,omitempty"`
	ArtKit        uint32 `json:"art_kit,omitempty"`
	PercentHealth uint8  `json:"percent_health,omitempty"`
	// Rotation is the object's local rotation, from the create block.
	Rotation Quat `json:"rotation"`
	// CreatedBy is set for objects a player made (fishing bobbers, fires).
	CreatedBy GUID `json:"created_by"`
}

// ItemInfo describes an item object. Owner-only fields (stack count,
// durability) are zero for items the session does not own.
type ItemInfo struct {
	Owner         GUID   `json:"owner"`
	ContainedIn   GUID   `json:"contained_in"`
	StackCount    uint32 `json:"stack_count,omitempty"`
	Durability    uint32 `json:"durability,omitempty"`
	MaxDurability uint32 `json:"max_durability,omitempty"`
	DynamicFlags  uint32 `json:"dynamic_flags,omitempty"`
}

// clone deep-copies the parts of o that are shared by reference.
func (o Object) clone() Object {
	o.Power = copyIntMap(o.Power)
	if o.Auras != nil {
		o.Auras = append([]string(nil), o.Auras...)
	}
	if o.GameObject != nil {
		g := *o.GameObject
		o.GameObject = &g
	}
	if o.Item != nil {
		it := *o.Item
		o.Item = &it
	}
	if o.Equipment != nil {
		eq := make(map[int]int32, len(o.Equipment))
		for k, v := range o.Equipment {
			eq[k] = v
		}
		o.Equipment = eq
	}
	return o
}

// Snapshot is an immutable view of the world at one version.
type Snapshot struct {
	Version   uint64    `json:"version"`
	Taken     time.Time `json:"taken"`
	Realm     string    `json:"realm,omitempty"`
	Character string    `json:"character,omitempty"`
	Self      GUID      `json:"self"`
	Tick      uint32    `json:"tick"`
	Objects   []Object  `json:"objects"`
}

// State is the mutable world model. All methods are safe for concurrent use.
type State struct {
	mu        sync.RWMutex
	version   uint64
	realm     string
	character string
	self      GUID
	tick      uint32
	objects   map[GUID]Object
	clock     func() time.Time
}

// New returns an empty world model.
func New() *State {
	return &State{objects: make(map[GUID]Object), clock: time.Now}
}

func (s *State) bump() { s.version++ }

// Version reports the current state version.
func (s *State) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// SetSession records the realm and character name once the logon sequence
// reaches the world.
func (s *State) SetSession(realm, character string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realm, s.character = realm, character
	s.bump()
}

// SetSelf marks the object controlled by this session.
func (s *State) SetSelf(g GUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.self = g
	s.bump()
}

// Self returns the controlled GUID.
func (s *State) Self() GUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.self
}

// AdvanceTick records the server tick counter.
func (s *State) AdvanceTick(tick uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tick != s.tick {
		s.tick = tick
		s.bump()
	}
}

// Upsert inserts or replaces an object and returns a copy of the stored value.
func (s *State) Upsert(o Object) Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.Updated.IsZero() {
		o.Updated = s.clock()
	}
	if prev, ok := s.objects[o.GUID]; ok && o.Kind == "" {
		o.Kind = prev.Kind
	}
	o = o.clone()
	s.objects[o.GUID] = o
	s.bump()
	return o.clone()
}

// Mutate applies fn to an existing object.
func (s *State) Mutate(g GUID, fn func(*Object)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[g]
	if !ok {
		return fmt.Errorf("state: object %v not present", g)
	}
	o = o.clone()
	fn(&o)
	o.Updated = s.clock()
	s.objects[g] = o
	s.bump()
	return nil
}

// Remove deletes an object, reporting whether it was present.
func (s *State) Remove(g GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[g]
	delete(s.objects, g)
	if ok {
		s.bump()
	}
	return ok
}

// Object returns a deep copy of one object.
func (s *State) Object(g GUID) (Object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objects[g]
	if !ok {
		return Object{}, false
	}
	return o.clone(), true
}

// Len reports how many objects are tracked.
func (s *State) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.objects)
}

// Snapshot returns a deep copy of the whole world, sorted by GUID for stable
// output.
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{
		Version:   s.version,
		Taken:     s.clock(),
		Realm:     s.realm,
		Character: s.character,
		Self:      s.self,
		Tick:      s.tick,
		Objects:   make([]Object, 0, len(s.objects)),
	}
	for _, o := range s.objects {
		out.Objects = append(out.Objects, o.clone())
	}
	sort.Slice(out.Objects, func(i, j int) bool { return out.Objects[i].GUID.Less(out.Objects[j].GUID) })
	return out
}

func copyIntMap(in map[string]int64) map[string]int64 {
	if in == nil {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
