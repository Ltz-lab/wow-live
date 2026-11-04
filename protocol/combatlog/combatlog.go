// Package combatlog rebuilds the combat log from the server's messages: the
// same events COMBAT_LOG_EVENT_UNFILTERED gave addons before the 12.x client
// stopped handing them out. The data never left the wire: the 70755 client
// still handles every message below (names_1.60.1.70755.json), and they
// arrive in the decrypted stream of the client's own session.
//
// Layouts are the fork's writers (TrinityCore forever branch, which the
// 70755 client accepts): SpellPackets.cpp, CombatLogPackets.cpp,
// CombatLogPacketsCommon.cpp, PartyPackets.cpp. A message carries the
// advanced-log block (health, attack power, powers) only when the player
// has Advanced Combat Logging on; the bit is read either way.
package combatlog

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// Event is one combat log line, named as CLEU named them.
type Event struct {
	TimeMs   uint32
	Sub      string // SPELL_CAST_START, SPELL_DAMAGE, SWING_DAMAGE, SPELL_AURA_APPLIED, ...
	Source   codec.GUID
	Dest     codec.GUID
	SpellID  int32
	Amount   int64 // damage, heal, energize amount; stacks for doses; remaining ms for auras
	Over     int32 // overkill, overheal, overenergize
	School   int32 // school mask; power type for SPELL_ENERGIZE
	Absorbed int32
	Resisted int32
	Crit     bool
	Periodic bool
	Extra    int32 // interrupted spell; miss reason; aura slot
	// Powers is what SPELL_GO says the caster has left after the cast (type,
	// amount); the server fills it for the player's own casts.
	Powers []Power
	// Health is the target's (damage, heal) or caster's (cast) health after the
	// event, from the advanced-log block; 0 when the block is absent.
	Health int64
}

// Power is one power type's amount (0 mana, 1 rage, 2 focus, 3 energy, 17
// fury, 18 pain, ...).
type Power struct {
	Type   int8
	Amount int32
}

// Message names this package decodes (the client's names; on the fork the
// numbers differ, the names do not).
var Messages = []string{
	"SMSG_SPELL_START", "SMSG_SPELL_GO", "SMSG_AURA_UPDATE",
	"SMSG_SPELL_NON_MELEE_DAMAGE_LOG", "SMSG_SPELL_PERIODIC_AURA_LOG",
	"SMSG_SPELL_HEAL_LOG", "SMSG_SPELL_ENERGIZE_LOG", "SMSG_SPELL_MISS_LOG",
	"SMSG_SPELL_INTERRUPT_LOG", "SMSG_ATTACKER_STATE_UPDATE", "SMSG_PARTY_KILL_LOG",
}

// Log turns messages into events. It keeps the aura slots of every unit, so
// SMSG_AURA_UPDATE becomes applied / removed / refresh / dose events.
type Log struct {
	auras map[codec.GUID]map[uint16]aura
}

type aura struct {
	spell    int32
	caster   codec.GUID
	stacks   uint8
	duration uint32 // ms, 0 for no duration
	expires  uint32 // log time in ms, 0 for no duration
}

// Aura is one aura on a unit, as the log last saw it.
type Aura struct {
	Slot       uint16
	SpellID    int32
	Caster     codec.GUID
	Stacks     uint8
	DurationMs uint32 // 0: no duration (passive, aura until cancelled)
	ExpiresMs  uint32 // log time it runs out; 0 with no duration
}

// Auras returns the unit's auras, by slot.
func (l *Log) Auras(unit codec.GUID) []Aura {
	var out []Aura
	for slot, a := range l.auras[unit] {
		out = append(out, Aura{Slot: slot, SpellID: a.spell, Caster: a.caster, Stacks: a.stacks, DurationMs: a.duration, ExpiresMs: a.expires})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

// New returns an empty log.
func New() *Log { return &Log{auras: map[codec.GUID]map[uint16]aura{}} }

// ErrUnknown is returned for a message name the log does not decode.
var ErrUnknown = errors.New("combatlog: not a combat message")

// Feed decodes one message.
func (l *Log) Feed(name string, t uint32, body []byte) ([]Event, error) {
	r := codec.NewReader(body)
	var evs []Event
	var err error
	switch name {
	case "SMSG_SPELL_START":
		var c cast
		if c, err = readCast(r); err == nil {
			evs = []Event{{Sub: "SPELL_CAST_START", Source: c.caster, Dest: c.target, SpellID: c.spell, Amount: int64(c.castTime)}}
		}
	case "SMSG_SPELL_GO":
		var c cast
		if c, err = readCast(r); err == nil {
			dest := c.target
			if len(c.hits) > 0 {
				dest = c.hits[0]
			}
			evs = append(evs, Event{Sub: "SPELL_CAST_SUCCESS", Source: c.caster, Dest: dest, SpellID: c.spell, Powers: c.powers})
			for i, m := range c.misses {
				var reason uint8
				if i < len(c.missReason) {
					reason = c.missReason[i]
				}
				evs = append(evs, Event{Sub: "SPELL_MISSED", Source: c.caster, Dest: m, SpellID: c.spell, Extra: int32(reason)})
			}
			var h int64
			if h, err = readLogData(r); err == nil && h != 0 {
				evs[0].Health = h
			}
		}
	case "SMSG_AURA_UPDATE":
		evs, err = l.auraUpdate(r, t)
	case "SMSG_SPELL_NON_MELEE_DAMAGE_LOG":
		evs, err = nonMelee(r)
	case "SMSG_SPELL_PERIODIC_AURA_LOG":
		evs, err = periodic(r)
	case "SMSG_SPELL_HEAL_LOG":
		evs, err = heal(r)
	case "SMSG_SPELL_ENERGIZE_LOG":
		evs, err = energize(r)
	case "SMSG_SPELL_MISS_LOG":
		evs, err = missLog(r)
	case "SMSG_SPELL_INTERRUPT_LOG":
		evs, err = interrupt(r)
	case "SMSG_ATTACKER_STATE_UPDATE":
		evs, err = swing(r)
	case "SMSG_PARTY_KILL_LOG":
		var p, v codec.GUID
		if p, err = r.GUID(); err == nil {
			if v, err = r.GUID(); err == nil {
				evs = []Event{{Sub: "PARTY_KILL", Source: p, Dest: v}}
			}
		}
	default:
		return nil, ErrUnknown
	}
	if err == nil && r.Remaining() != 0 && name != "SMSG_ATTACKER_STATE_UPDATE" {
		err = fmt.Errorf("%d of %d bytes left over", r.Remaining(), len(body))
	}
	if err != nil {
		return nil, fmt.Errorf("combatlog: %s: %w", name, err)
	}
	for i := range evs {
		evs[i].TimeMs = t
	}
	return evs, nil
}

// e collects the first read error so decoders read straight through.
type e struct {
	r   *codec.Reader
	err error
}

func (x *e) guid() codec.GUID {
	if x.err != nil {
		return codec.GUID{}
	}
	g, err := x.r.GUID()
	x.err = err
	return g
}
func (x *e) i32() int32 {
	if x.err != nil {
		return 0
	}
	v, err := x.r.Int32()
	x.err = err
	return v
}
func (x *e) u32() uint32 { return uint32(x.i32()) }
func (x *e) u16() uint16 {
	if x.err != nil {
		return 0
	}
	v, err := x.r.Uint16()
	x.err = err
	return v
}
func (x *e) u8() uint8 {
	if x.err != nil {
		return 0
	}
	v, err := x.r.Uint8()
	x.err = err
	return v
}
func (x *e) i64() int64 {
	if x.err != nil {
		return 0
	}
	v, err := x.r.Uint64()
	x.err = err
	return int64(v)
}
func (x *e) bits(n uint) uint64 {
	if x.err != nil {
		return 0
	}
	v, err := x.r.Bits(n)
	x.err = err
	return v
}
func (x *e) bit() bool { return x.bits(1) == 1 }
func (x *e) skip(n int) {
	if x.err != nil || n == 0 {
		return
	}
	_, x.err = x.r.Bytes(n)
}

// contentTuning is ContentTuningParams: 59 bytes then a 4-bit type, flushed.
func (x *e) contentTuning() {
	x.skip(4 + 4 + 2 + 4 + 4 + 4 + 1 + 1 + 1 + 4 + 4 + 4 + 4 + 4)
	x.bits(4)
	x.r.ResetBits()
}

func (x *e) supporters(n uint32) {
	for i := uint32(0); i < n && x.err == nil; i++ {
		x.guid()
		x.skip(12)
	}
}

// logData is SpellCastLogData, present when its bit was set.
func (x *e) logData(present bool) (health int64) {
	if !present {
		return 0
	}
	health = x.i64()
	x.skip(20)
	x.bit() // HideFromCombatLog
	n := x.bits(9)
	x.r.ResetBits()
	x.skip(int(n) * 9)
	return health
}

func readLogData(r *codec.Reader) (int64, error) {
	x := &e{r: r}
	h := x.logData(x.bit())
	return h, x.err
}

type cast struct {
	caster, target codec.GUID
	spell          int32
	castTime       uint32
	hits, misses   []codec.GUID
	missReason     []uint8
	powers         []Power
}

// readCast is SpellCastData.
func readCast(r *codec.Reader) (cast, error) {
	x := &e{r: r}
	var c cast
	c.caster = x.guid()
	x.guid() // CasterUnit
	x.guid() // CastID
	x.guid() // OriginalCastID
	c.spell = x.i32()
	x.skip(8)  // Visual
	x.skip(12) // CastFlags, Ex, Ex2
	c.castTime = x.u32()
	// SpellTargetData
	x.skip(4)
	c.target = x.guid()
	x.guid() // Item
	x.guid() // Housing
	x.bit()  // HousingIsResident
	src, dst, ori, mapID := x.bit(), x.bit(), x.bit(), x.bit()
	nameLen := x.bits(7)
	x.r.ResetBits()
	for _, has := range []bool{src, dst} {
		if has {
			x.guid()
			x.skip(12)
		}
	}
	if ori {
		x.skip(4)
	}
	if mapID {
		x.skip(4)
	}
	x.skip(int(nameLen))
	x.skip(8) // MissileTrajectory
	x.skip(4) // AmmoDisplayID
	x.skip(1) // DestLocSpellCastIndex
	x.skip(8) // Immunities
	x.skip(8) // Predict points, type
	x.guid()  // Predict beacon
	nHit, nMiss := x.bits(16), x.bits(16)
	nHitSt, nMissSt := x.bits(16), x.bits(16)
	nPower := x.bits(9)
	runes := x.bit()
	nPoints := x.bits(16)
	x.r.ResetBits()
	for i := uint64(0); i < nHit && x.err == nil; i++ {
		c.hits = append(c.hits, x.guid())
	}
	for i := uint64(0); i < nMiss && x.err == nil; i++ {
		c.misses = append(c.misses, x.guid())
	}
	x.skip(int(nHitSt))
	for i := uint64(0); i < nMissSt && x.err == nil; i++ {
		reason := x.u8()
		if reason == 11 { // SPELL_MISS_REFLECT
			x.u8()
		}
		c.missReason = append(c.missReason, reason)
	}
	for i := uint64(0); i < nPower && x.err == nil; i++ {
		c.powers = append(c.powers, Power{Type: int8(x.u8()), Amount: x.i32()})
	}
	if runes {
		x.skip(2)
		x.skip(int(x.u32()))
	}
	for i := uint64(0); i < nPoints && x.err == nil; i++ {
		x.guid()
		x.skip(12)
	}
	return c, x.err
}

func (l *Log) auraUpdate(r *codec.Reader, t uint32) ([]Event, error) {
	x := &e{r: r}
	all := x.bit()
	n := x.bits(9)
	x.r.ResetBits()
	unit := x.guid()
	slots := l.auras[unit]
	if slots == nil || all {
		old := slots
		slots = map[uint16]aura{}
		l.auras[unit] = slots
		_ = old // an UpdateAll resends every aura; removals of unlisted ones are not reported
	}
	var evs []Event
	for i := uint64(0); i < n && x.err == nil; i++ {
		slot := x.u16()
		has := x.bit()
		x.r.ResetBits()
		prev, had := slots[slot]
		if !has {
			if had {
				evs = append(evs, Event{Sub: "SPELL_AURA_REMOVED", Source: prev.caster, Dest: unit, SpellID: prev.spell, Extra: int32(slot)})
				delete(slots, slot)
			}
			continue
		}
		x.guid() // CastID
		spell := x.i32()
		x.skip(8) // Visual
		x.u16()   // Flags
		x.u32()   // ActiveFlags
		x.u16()   // CastLevel
		stacks := x.u8()
		x.i32()    // ContentTuningID
		x.skip(12) // DstLocation (x, y, z)
		hasCaster, hasItem := x.bit(), x.bit()
		hasDur, hasRem, hasMod := x.bit(), x.bit(), x.bit()
		nPts, nEst := x.bits(6), x.bits(6)
		hasCT := x.bit()
		x.r.ResetBits()
		var caster codec.GUID
		if hasCaster {
			caster = x.guid()
		}
		if hasItem {
			x.guid()
		}
		var dur, rem uint32
		if hasDur {
			dur = x.u32()
		}
		if hasRem {
			rem = x.u32()
		}
		if hasMod {
			x.skip(4)
		}
		x.skip(int(nPts+nEst) * 4)
		if hasCT {
			x.contentTuning()
		}
		a := aura{spell: spell, caster: caster, stacks: stacks, duration: dur}
		if hasRem && dur > 0 {
			a.expires = t + rem
		}
		slots[slot] = a
		ev := Event{Source: caster, Dest: unit, SpellID: spell, Amount: int64(rem), Extra: int32(slot)}
		switch {
		case !had || prev.spell != spell:
			if had {
				evs = append(evs, Event{Sub: "SPELL_AURA_REMOVED", Source: prev.caster, Dest: unit, SpellID: prev.spell, Extra: int32(slot)})
			}
			ev.Sub = "SPELL_AURA_APPLIED"
		case stacks > prev.stacks:
			ev.Sub, ev.Amount = "SPELL_AURA_APPLIED_DOSE", int64(stacks)
		case stacks < prev.stacks:
			ev.Sub, ev.Amount = "SPELL_AURA_REMOVED_DOSE", int64(stacks)
		default:
			ev.Sub = "SPELL_AURA_REFRESH"
		}
		evs = append(evs, ev)
	}
	return evs, x.err
}

func nonMelee(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	ev := Event{Sub: "SPELL_DAMAGE"}
	ev.Dest = x.guid()
	ev.Source = x.guid()
	x.guid() // CastID
	ev.SpellID = x.i32()
	x.skip(8) // Visual
	ev.Amount = int64(x.i32())
	x.i32() // OriginalDamage
	ev.Over = x.i32()
	ev.School = int32(x.u8())
	ev.Absorbed = x.i32()
	ev.Resisted = x.i32()
	x.i32()                // ShieldBlock
	x.i32()                // ReflectingSpellID
	flags := x.i32()       // Flags
	ev.Crit = flags&2 != 0 // SPELL_HIT_TYPE_CRIT
	nViewers := x.u32()
	nSupp := x.u32()
	for i := uint32(0); i < nViewers && x.err == nil; i++ {
		x.guid()
		c, s := x.bit(), x.bit()
		x.r.ResetBits()
		if c {
			x.u8()
		}
		if s {
			x.u8()
		}
	}
	x.supporters(nSupp)
	ev.Periodic = x.bit()
	x.bit() // debug
	hasLog := x.bit()
	hasCT := x.bit()
	x.r.ResetBits()
	ev.Health = x.logData(hasLog)
	if hasCT {
		x.contentTuning()
	}
	if ev.Periodic {
		ev.Sub = "SPELL_PERIODIC_DAMAGE"
	}
	return []Event{ev}, x.err
}

// Periodic aura effect types that heal (SPELL_AURA_PERIODIC_HEAL and
// OBS_MOD_HEALTH) or give power (PERIODIC_ENERGIZE, OBS_MOD_POWER).
var periodicKind = map[int32]string{
	3: "SPELL_PERIODIC_DAMAGE", 89: "SPELL_PERIODIC_DAMAGE", // PERIODIC_DAMAGE, PERIODIC_DAMAGE_PERCENT
	8: "SPELL_PERIODIC_HEAL", 20: "SPELL_PERIODIC_HEAL", // PERIODIC_HEAL, OBS_MOD_HEALTH
	24: "SPELL_PERIODIC_ENERGIZE", 21: "SPELL_PERIODIC_ENERGIZE", // PERIODIC_ENERGIZE, OBS_MOD_POWER
	64: "SPELL_PERIODIC_LEECH", 53: "SPELL_PERIODIC_LEECH", // PERIODIC_MANA_LEECH, PERIODIC_LEECH
}

func periodic(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	dest := x.guid()
	src := x.guid()
	spell := x.i32()
	n := x.u32()
	var evs []Event
	for i := uint32(0); i < n && x.err == nil; i++ {
		ev := Event{Source: src, Dest: dest, SpellID: spell, Periodic: true}
		kind := x.i32()
		ev.Amount = int64(x.i32())
		x.i32() // OriginalDamage
		ev.Over = x.i32()
		ev.School = x.i32()
		ev.Absorbed = x.i32()
		ev.Resisted = x.i32()
		x.supporters(x.u32())
		ev.Crit = x.bit()
		dbg, ct := x.bit(), x.bit()
		x.r.ResetBits()
		if dbg {
			x.skip(8)
		}
		if ct {
			x.contentTuning()
		}
		ev.Sub = periodicKind[kind]
		if ev.Sub == "" {
			ev.Sub = fmt.Sprintf("SPELL_PERIODIC_AURA_%d", kind)
		}
		evs = append(evs, ev)
	}
	hasLog := x.bit()
	x.r.ResetBits()
	h := x.logData(hasLog)
	for i := range evs {
		evs[i].Health = h
	}
	return evs, x.err
}

func heal(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	ev := Event{Sub: "SPELL_HEAL"}
	ev.Dest = x.guid()
	ev.Source = x.guid()
	ev.SpellID = x.i32()
	ev.Amount = int64(x.i32())
	x.i32() // OriginalHeal
	ev.Over = x.i32()
	ev.Absorbed = x.i32()
	x.supporters(x.u32())
	ev.Crit = x.bit()
	made, needed := x.bit(), x.bit()
	hasLog := x.bit()
	hasCT := x.bit()
	x.r.ResetBits()
	if made {
		x.skip(4)
	}
	if needed {
		x.skip(4)
	}
	ev.Health = x.logData(hasLog)
	if hasCT {
		x.contentTuning()
	}
	return []Event{ev}, x.err
}

func energize(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	ev := Event{Sub: "SPELL_ENERGIZE"}
	ev.Dest = x.guid()
	ev.Source = x.guid()
	ev.SpellID = x.i32()
	ev.School = int32(int8(x.u8()))
	ev.Amount = int64(x.i32())
	ev.Over = x.i32()
	hasLog := x.bit()
	x.r.ResetBits()
	ev.Health = x.logData(hasLog)
	return []Event{ev}, x.err
}

func missLog(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	spell := x.i32()
	src := x.guid()
	n := x.u32()
	var evs []Event
	for i := uint32(0); i < n && x.err == nil; i++ {
		v := x.guid()
		reason := x.u8()
		if x.bit() {
			x.r.ResetBits()
			x.skip(8)
		}
		x.r.ResetBits()
		evs = append(evs, Event{Sub: "SPELL_MISSED", Source: src, Dest: v, SpellID: spell, Extra: int32(reason)})
	}
	x.bit() // HideFromCombatLog
	return evs, x.err
}

func interrupt(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	ev := Event{Sub: "SPELL_INTERRUPT"}
	ev.Source = x.guid()
	ev.Dest = x.guid()
	ev.Extra = x.i32()
	ev.SpellID = x.i32()
	x.bit() // HideFromCombatLog
	return []Event{ev}, x.err
}

// Hit info flags (TrinityCore HitInfo) the swing decoder needs.
const (
	hitFullAbsorb    = 0x00000020
	hitPartialAbsorb = 0x00000040
	hitFullResist    = 0x00000080
	hitPartialResist = 0x00000100
	hitCritical      = 0x00000200
	hitMiss          = 0x00000010
)

func swing(r *codec.Reader) ([]Event, error) {
	x := &e{r: r}
	hasLog := x.bit()
	x.r.ResetBits()
	health := x.logData(hasLog)
	size := x.u32()
	if x.err != nil {
		return nil, x.err
	}
	if int(size) > r.Remaining() {
		return nil, fmt.Errorf("attack round %d B, %d left", size, r.Remaining())
	}
	flags := x.u32()
	ev := Event{Sub: "SWING_DAMAGE", Health: health}
	ev.Source = x.guid()
	ev.Dest = x.guid()
	ev.Amount = int64(x.i32())
	x.i32() // OriginalDamage
	ev.Over = x.i32()
	if x.u8() != 0 {
		ev.School = x.i32()
		x.skip(4) // FDamage
		x.i32()   // Damage
		if flags&(hitFullAbsorb|hitPartialAbsorb) != 0 {
			ev.Absorbed = x.i32()
		}
		if flags&(hitFullResist|hitPartialResist) != 0 {
			ev.Resisted = x.i32()
		}
	}
	victimState := x.u8()
	ev.Crit = flags&hitCritical != 0
	if flags&hitMiss != 0 || victimState != 1 && ev.Amount == 0 {
		ev.Sub = "SWING_MISSED"
		ev.Extra = int32(victimState)
	}
	return []Event{ev}, x.err
}
