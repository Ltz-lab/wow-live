// Package spellstate tracks the player's cooldowns, charges, global cooldown
// and power from the world stream: what a rotation helper needs beside auras.
//
// The server does not send a cooldown per cast. Retail's capture of
// 2026-11-05 03:02 has 9 SMSG_SPELL_COOLDOWN against 1,363 casts: the client
// starts cooldowns itself from the spell's own data. So does this package:
// a cast of the player's (SPELL_GO) starts the spell's recovery, category
// recovery, charge and GCD from the game's tables (wago.tools CSV exports of
// SpellCooldowns, SpellCategories and SpellCategory), and the server's own
// messages correct it: forced cooldowns (SPELL_COOLDOWN), the login snapshot
// (SEND_SPELL_HISTORY, SEND_SPELL_CHARGES), charge state (SET_SPELL_CHARGES),
// clears, delta changes (MODIFY_COOLDOWN) and rate changes
// (UPDATE_CHARGE_CATEGORY_COOLDOWN, UPDATE_COOLDOWN). Haste is not known, so
// hasted cooldowns and the GCD run at their base length unless a rate
// message says otherwise.
//
// Power: SPELL_GO's remaining-power list for the player's casts sets it
// (authoritative), SPELL_ENERGIZE adds to it between casts.
package spellstate

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/Ltz-lab/wow-live/protocol/codec"
	"github.com/Ltz-lab/wow-live/protocol/combatlog"
)

// SpellInfo is a spell's cooldown data.
type SpellInfo struct {
	Recovery    uint32 // ms, the spell's own cooldown
	CatRecovery uint32 // ms, its category's cooldown
	GCD         uint32 // ms, StartRecoveryTime
	Category    int32
	ChargeCat   int32
}

// Category is a cooldown or charge category.
type Category struct {
	Name           string
	MaxCharges     int
	ChargeRecovery uint32 // ms
}

// Data is the spell tables the tracker needs.
type Data struct {
	Spells     map[int32]SpellInfo
	Categories map[int32]Category
	Names      map[int32]string
	bycat      map[int32][]int32
}

// CategorySpells lists the spells of a cooldown or charge category (at most
// 20), so a consumer can tell which spells a category cooldown holds back.
func (d *Data) CategorySpells(c int32) []int32 {
	if d.bycat == nil {
		d.bycat = map[int32][]int32{}
		for id, s := range d.Spells {
			for i, k := range []int32{s.Category, s.ChargeCat} {
				if i == 1 && k == s.Category {
					continue
				}
				if k != 0 && len(d.bycat[k]) < 20 {
					d.bycat[k] = append(d.bycat[k], id)
				}
			}
		}
		for _, l := range d.bycat {
			sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
		}
	}
	return d.bycat[c]
}

// LoadWago reads SpellCooldowns.csv, SpellCategories.csv, SpellCategory.csv
// and (if present) SpellName.csv from dir: wago.tools exports, e.g.
// https://wago.tools/db2/SpellCooldowns/csv?build=12.1.5.70783. Rows for
// difficulty 0 win over others.
func LoadWago(dir string) (*Data, error) {
	d := &Data{Spells: map[int32]SpellInfo{}, Categories: map[int32]Category{}, Names: map[int32]string{}}
	cd, err := readCSV(filepath.Join(dir, "SpellCooldowns.csv"))
	if err != nil {
		return nil, err
	}
	for _, r := range cd.rows {
		if r.int("DifficultyID") != 0 {
			continue
		}
		id := int32(r.int("SpellID"))
		s := d.Spells[id]
		s.CatRecovery, s.Recovery, s.GCD = uint32(r.int("CategoryRecoveryTime")), uint32(r.int("RecoveryTime")), uint32(r.int("StartRecoveryTime"))
		d.Spells[id] = s
	}
	cats, err := readCSV(filepath.Join(dir, "SpellCategories.csv"))
	if err != nil {
		return nil, err
	}
	for _, r := range cats.rows {
		if r.int("DifficultyID") != 0 {
			continue
		}
		id := int32(r.int("SpellID"))
		s := d.Spells[id]
		s.Category, s.ChargeCat = int32(r.int("Category")), int32(r.int("ChargeCategory"))
		d.Spells[id] = s
	}
	cat, err := readCSV(filepath.Join(dir, "SpellCategory.csv"))
	if err != nil {
		return nil, err
	}
	for _, r := range cat.rows {
		d.Categories[int32(r.int("ID"))] = Category{Name: r.str("Name_lang"), MaxCharges: r.int("MaxCharges"), ChargeRecovery: uint32(r.int("ChargeRecoveryTime"))}
	}
	if names, err := readCSV(filepath.Join(dir, "SpellName.csv")); err == nil {
		for _, r := range names.rows {
			d.Names[int32(r.int("ID"))] = r.str("Name_lang")
		}
	}
	return d, nil
}

type table struct{ rows []row }
type row struct {
	cols map[string]int
	v    []string
}

func (r row) str(c string) string {
	if i, ok := r.cols[c]; ok && i < len(r.v) {
		return r.v[i]
	}
	return ""
}
func (r row) int(c string) int { n, _ := strconv.Atoi(r.str(c)); return n }

func readCSV(path string) (*table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	all, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(all) == 0 {
		return &table{}, nil
	}
	cols := map[string]int{}
	for i, h := range all[0] {
		cols[h] = i
	}
	t := &table{}
	for _, v := range all[1:] {
		t.rows = append(t.rows, row{cols, v})
	}
	return t, nil
}

type charges struct {
	consumed int
	next     uint32 // log ms the next charge comes back (when consumed > 0)
}

// Tracker is the player's cooldown, charge, GCD and power state.
type Tracker struct {
	Data   *Data
	Player codec.GUID

	gcdEnd    uint32
	spell     map[int32]uint32 // spell -> ready at (log ms)
	cat       map[int32]uint32 // category -> ready at (for the casting spell's length)
	catStart  map[int32]uint32 // category -> when its cooldown started (from a cast)
	charge    map[int32]*charges
	rate      map[int32]float32 // charge category -> recovery length multiplier (1 = base, <1 faster)
	power     map[int8]int32
	maxPower  map[int8]int32
	powerSeen bool
}

// New returns a tracker over the spell tables (nil: server messages only).
func New(d *Data) *Tracker {
	if d == nil {
		d = &Data{Spells: map[int32]SpellInfo{}, Categories: map[int32]Category{}, Names: map[int32]string{}}
	}
	return &Tracker{Data: d, spell: map[int32]uint32{}, cat: map[int32]uint32{}, catStart: map[int32]uint32{}, charge: map[int32]*charges{},
		rate: map[int32]float32{}, power: map[int8]int32{}, maxPower: map[int8]int32{}}
}

// Event takes a combat log event; it returns whether the state changed.
func (tr *Tracker) Event(e combatlog.Event) bool {
	if tr.Player.IsEmpty() {
		return false
	}
	switch {
	case e.Sub == "SPELL_CAST_SUCCESS" && e.Source == tr.Player:
		for _, p := range e.Powers {
			tr.power[p.Type] = p.Amount
			tr.powerSeen = true
		}
		return tr.cast(e.SpellID, e.TimeMs) || len(e.Powers) > 0
	case (e.Sub == "SPELL_ENERGIZE" || e.Sub == "SPELL_PERIODIC_ENERGIZE") && e.Dest == tr.Player:
		// Amount is what was gained; Over is what was wasted past the cap,
		// so an overcap means the player is now at max: learn it from there.
		t := int8(e.School)
		tr.power[t] += int32(e.Amount)
		if e.Over > 0 {
			tr.maxPower[t] = tr.power[t]
		}
		if m := tr.maxPower[t]; m > 0 && tr.power[t] > m {
			tr.power[t] = m
		}
		tr.power[t] = max(tr.power[t], 0)
		tr.powerSeen = true
		return true
	}
	return false
}

func (tr *Tracker) chargeRecovery(c int32) uint32 {
	r := tr.Data.Categories[c].ChargeRecovery
	if f := tr.rate[c]; f > 0 {
		r = uint32(float32(r) * f)
	}
	return r
}

func (tr *Tracker) cast(id int32, t uint32) bool {
	s, ok := tr.Data.Spells[id]
	if !ok {
		return false
	}
	if s.GCD > 0 {
		tr.gcdEnd = t + s.GCD
	}
	changed := s.GCD > 0
	if c := tr.Data.Categories[s.ChargeCat]; s.ChargeCat != 0 && c.MaxCharges > 0 {
		ch := tr.charges(s.ChargeCat, t)
		if ch.consumed == 0 {
			ch.next = t + tr.chargeRecovery(s.ChargeCat)
		}
		if ch.consumed < c.MaxCharges {
			ch.consumed++
		}
		changed = true
	} else if s.Recovery > 0 {
		tr.spell[id] = t + s.Recovery
		changed = true
	}
	if s.Category != 0 && s.CatRecovery > 0 && s.Category != s.ChargeCat {
		tr.cat[s.Category] = t + s.CatRecovery
		tr.catStart[s.Category] = t
		changed = true
	}
	return changed
}

// charges brings a charge category up to time t.
func (tr *Tracker) charges(c int32, t uint32) *charges {
	ch := tr.charge[c]
	if ch == nil {
		ch = &charges{}
		tr.charge[c] = ch
	}
	for ch.consumed > 0 && t >= ch.next {
		ch.consumed--
		if ch.consumed > 0 {
			ch.next += tr.chargeRecovery(c)
		}
	}
	return ch
}

// Message takes a server message (by its bare name); it returns whether it
// was a cooldown message and the state changed.
func (tr *Tracker) Message(name string, t uint32, body []byte) bool {
	r := codec.NewReader(body)
	u32 := func() uint32 { v, _ := r.Uint32(); return v }
	f32 := func() float32 { v, _ := r.Float32(); return v }
	switch name {
	case "SMSG_SPELL_COOLDOWN": // caster, u8 flags, n × {spell, forced ms, rate}
		g, err := r.GUID()
		if err != nil || g != tr.Player {
			return false
		}
		r.Uint8()
		n := u32()
		for i := uint32(0); i < n && r.Remaining() >= 12; i++ {
			id, forced := int32(u32()), u32()
			f32()
			if forced > 0 {
				tr.spell[id] = t + forced
			} else {
				tr.cast(id, t)
			}
		}
		return n > 0
	case "SMSG_SEND_SPELL_HISTORY": // n × {spell, item, category, recovery, cat recovery, rate, bits, offsets}
		n := u32()
		for i := uint32(0); i < n && r.Remaining() >= 25; i++ {
			id, _, cat := int32(u32()), u32(), int32(u32())
			rec, catRec := int32(u32()), int32(u32())
			f32()
			a, _ := r.Bit()
			b, _ := r.Bit()
			r.Bit()
			r.ResetBits()
			if a {
				u32()
			}
			if b {
				u32()
			}
			if rec > 0 {
				tr.spell[id] = t + uint32(rec)
			}
			if cat != 0 && catRec > 0 {
				tr.cat[cat] = t + uint32(catRec)
			}
		}
		return n > 0
	case "SMSG_SEND_SPELL_CHARGES": // n × {category, next recovery, rate, u8 consumed}
		n := u32()
		for i := uint32(0); i < n && r.Remaining() >= 13; i++ {
			c, next := int32(u32()), u32()
			rate := f32()
			used, _ := r.Uint8()
			tr.setCharges(c, t, next, int(used), rate)
		}
		return n > 0
	case "SMSG_SET_SPELL_CHARGES": // category, next recovery, u8 consumed, rate, bit
		if len(body) < 13 {
			return false
		}
		c, next := int32(u32()), u32()
		used, _ := r.Uint8()
		tr.setCharges(c, t, next, int(used), f32())
		return true
	case "SMSG_CLEAR_COOLDOWN":
		if len(body) >= 4 {
			id := int32(u32())
			delete(tr.spell, id)
			if s, ok := tr.Data.Spells[id]; ok && s.Category != 0 {
				delete(tr.cat, s.Category)
			}
			return true
		}
	case "SMSG_CLEAR_COOLDOWNS":
		n := u32()
		for i := uint32(0); i < n && r.Remaining() >= 4; i++ {
			delete(tr.spell, int32(u32()))
		}
		return n > 0
	case "SMSG_CLEAR_SPELL_CHARGES":
		if len(body) >= 4 {
			delete(tr.charge, int32(u32()))
			return true
		}
	case "SMSG_CLEAR_ALL_SPELL_CHARGES":
		tr.charge = map[int32]*charges{}
		return true
	case "SMSG_MODIFY_COOLDOWN":
		// retail's message under this name (by the group shift) is 13 bytes,
		// {category, float change, float rate, bit}: the charge-category rate
		// update (categories 1552/1553/1676 Fracture, Infernal Strike,
		// Immolation Aura in the 03:02 capture). The fork's is 9 bytes
		// {spell, i32 delta ms, bits}.
		switch len(body) {
		case 13:
			tr.speedUp(int32(u32()), f32(), t)
			return true
		case 9:
			id, delta := int32(u32()), int32(u32())
			if at, ok := tr.spell[id]; ok {
				tr.spell[id] = uint32(max(int64(at)+int64(delta), int64(t)))
			}
			return true
		}
	case "SMSG_UPDATE_CHARGE_CATEGORY_COOLDOWN":
		if len(body) >= 12 {
			tr.speedUp(int32(u32()), f32(), t)
			return true
		}
	}
	return false
}

// speedUp applies a recovery speed change (ModChange: >1 faster, as haste
// rises; the capture's values sit between 0.94 and 1.04) to a charge
// category: the charge in flight and later ones.
func (tr *Tracker) speedUp(c int32, change float32, t uint32) {
	if change <= 0 {
		return
	}
	if ch := tr.charges(c, t); ch.consumed > 0 && ch.next > t {
		ch.next = t + uint32(float32(ch.next-t)/change)
	}
	m := tr.rate[c]
	if m == 0 {
		m = 1
	}
	tr.rate[c] = m / change
}

func (tr *Tracker) setCharges(c int32, t, next uint32, used int, rate float32) {
	ch := tr.charges(c, t)
	ch.consumed = used
	ch.next = t + next
	if rate > 0 {
		tr.rate[c] = rate
	}
}

// Cooldown is one spell or category that is not ready.
type Cooldown struct {
	Spell       int32   `json:"spell,omitempty"`
	Category    int32   `json:"category,omitempty"`
	Name        string  `json:"name,omitempty"`
	RemainingMs uint32  `json:"remaining_ms"`
	Charges     *int    `json:"charges,omitempty"`     // available now, for charge categories
	MaxCharges  int     `json:"max_charges,omitempty"` // for charge categories
	Spells      []int32 `json:"spells,omitempty"`      // for categories: the spells it holds back
}

// GCD returns the GCD left at t.
func (tr *Tracker) GCD(t uint32) uint32 {
	if tr.gcdEnd > t {
		return tr.gcdEnd - t
	}
	return 0
}

// Cooldowns returns everything still recovering at t: spells, categories
// (by one spell that uses them, for a name) and charge categories with a
// charge missing. Anything absent is ready.
func (tr *Tracker) Cooldowns(t uint32) []Cooldown {
	var out []Cooldown
	for id, at := range tr.spell {
		if at > t {
			out = append(out, Cooldown{Spell: id, Name: tr.Data.Names[id], RemainingMs: at - t})
		} else {
			delete(tr.spell, id)
		}
	}
	for c, at := range tr.cat {
		start, cast := tr.catStart[c]
		if !cast {
			if at > t {
				out = append(out, Cooldown{Category: c, Name: tr.Data.Categories[c].Name, RemainingMs: at - t, Spells: tr.Data.CategorySpells(c)})
			} else {
				delete(tr.cat, c)
			}
			continue
		}
		// One start, a length per spell: Blade Dance (15 s) and Death Sweep
		// (9 s) share category 1640, so Death Sweep is ready 9 s after either.
		byLen := map[uint32][]int32{}
		var longest uint32
		for _, id := range tr.Data.CategorySpells(c) {
			if l := tr.Data.Spells[id].CatRecovery; l > 0 {
				byLen[l] = append(byLen[l], id)
				longest = max(longest, l)
			}
		}
		if start+longest <= t {
			delete(tr.cat, c)
			delete(tr.catStart, c)
			continue
		}
		for l, ids := range byLen {
			if start+l <= t {
				continue
			}
			cd := Cooldown{Category: c, Name: tr.Data.Categories[c].Name, RemainingMs: start + l - t, Spells: ids}
			if len(byLen) > 1 && len(ids) == 1 {
				cd.Spell, cd.Name = ids[0], tr.Data.Names[ids[0]]
			}
			out = append(out, cd)
		}
	}
	for c := range tr.charge {
		ch := tr.charges(c, t)
		if ch.consumed == 0 {
			continue
		}
		maxc := tr.Data.Categories[c].MaxCharges
		left := max(maxc-ch.consumed, 0)
		out = append(out, Cooldown{Category: c, Name: tr.Data.Categories[c].Name, RemainingMs: ch.next - t,
			Charges: &left, MaxCharges: maxc, Spells: tr.Data.CategorySpells(c)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemainingMs < out[j].RemainingMs })
	return out
}

// Power returns the known power amounts by type, and whether any is known.
func (tr *Tracker) Power() (map[int8]int32, bool) {
	out := map[int8]int32{}
	for k, v := range tr.power {
		out[k] = v
	}
	return out, tr.powerSeen
}

// SpellCategories returns the cooldown and charge categories of a spell, so
// a consumer can tell a spell is not ready because its category is cooling.
func (tr *Tracker) SpellCategories(id int32) (cat, chargeCat int32) {
	s := tr.Data.Spells[id]
	return s.Category, s.ChargeCat
}

// PowerNames names the power types a rotation is likely to read.
var PowerNames = map[int8]string{0: "mana", 1: "rage", 2: "focus", 3: "energy", 4: "combo_points", 5: "runes",
	6: "runic_power", 7: "soul_shards", 8: "lunar_power", 9: "holy_power", 11: "maelstrom", 12: "chi",
	13: "insanity", 16: "arcane_charges", 17: "fury", 18: "pain", 19: "essence"}
