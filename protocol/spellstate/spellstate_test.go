package spellstate

import (
	"encoding/hex"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
	"github.com/Ltz-lab/wow-live/protocol/combatlog"
)

// Retail 12.1.5.70783 data for three Vengeance spells (wago.tools CSVs):
// Fracture 263642 charge category 1552 (2 charges, 5.5 s), Spirit Bomb
// 247454 category 2455 (25 s), Soul Cleave 228477 (GCD only).
func data() *Data {
	return &Data{
		Spells: map[int32]SpellInfo{
			263642: {GCD: 1500, ChargeCat: 1552},
			247454: {GCD: 1500, Category: 2455, CatRecovery: 25000},
			228477: {GCD: 1500},
		},
		Categories: map[int32]Category{1552: {Name: "Demon Hunter - Fracture", MaxCharges: 2, ChargeRecovery: 5500}, 2455: {Name: "Demon Hunter - Spirit Bomb"}},
		Names:      map[int32]string{},
	}
}

func cast(tr *Tracker, spell int32, t uint32, fury int32) {
	tr.Event(combatlog.Event{TimeMs: t, Sub: "SPELL_CAST_SUCCESS", Source: tr.Player, SpellID: spell, Powers: []combatlog.Power{{Type: 17, Amount: fury}}})
}

func find(cds []Cooldown, cat int32) *Cooldown {
	for i := range cds {
		if cds[i].Category == cat {
			return &cds[i]
		}
	}
	return nil
}

func TestChargesGCDCategoryAndFury(t *testing.T) {
	tr := New(data())
	tr.Player = codec.GUID{Hi: 0x0801180000000000, Lo: 9}

	cast(tr, 263642, 0, 28)
	if g := tr.GCD(100); g != 1400 {
		t.Errorf("GCD at 100 ms: %d", g)
	}
	c := find(tr.Cooldowns(100), 1552)
	if c == nil || *c.Charges != 1 || c.RemainingMs != 5400 {
		t.Fatalf("one Fracture used: %+v", c)
	}
	cast(tr, 263642, 2000, 56)
	if c := find(tr.Cooldowns(2000), 1552); *c.Charges != 0 || c.RemainingMs != 3500 {
		t.Errorf("both used: %+v", c)
	}
	// the first charge returns at 5.5 s, the second 5.5 s after that
	if c := find(tr.Cooldowns(6000), 1552); *c.Charges != 1 || c.RemainingMs != 5000 {
		t.Errorf("one back: %+v", c)
	}
	if find(tr.Cooldowns(11000), 1552) != nil {
		t.Errorf("both back by 11 s")
	}

	cast(tr, 247454, 20000, 26)
	if c := find(tr.Cooldowns(21000), 2455); c == nil || c.RemainingMs != 24000 {
		t.Errorf("Spirit Bomb category: %+v", c)
	}
	if p, _ := tr.Power(); p[17] != 26 {
		t.Errorf("fury after Spirit Bomb: %v", p)
	}
	tr.Event(combatlog.Event{TimeMs: 21500, Sub: "SPELL_ENERGIZE", Dest: tr.Player, SpellID: 263642, School: 17, Amount: 28})
	if p, _ := tr.Power(); p[17] != 54 {
		t.Errorf("fury after +28: %v", p)
	}
	// Demon Blades at the cap, as retail logs it: 2 gained, 13 wasted. The
	// waste is not a spend: fury goes up by 2 and that is the max from now on.
	tr.Event(combatlog.Event{TimeMs: 22000, Sub: "SPELL_ENERGIZE", Dest: tr.Player, SpellID: 203796, School: 17, Amount: 2, Over: 13})
	if p, _ := tr.Power(); p[17] != 56 {
		t.Errorf("fury after an overcap: %v", p)
	}
	tr.Event(combatlog.Event{TimeMs: 22500, Sub: "SPELL_ENERGIZE", Dest: tr.Player, SpellID: 203796, School: 17, Amount: 0, Over: 15})
	tr.Event(combatlog.Event{TimeMs: 23000, Sub: "SPELL_ENERGIZE", Dest: tr.Player, SpellID: 263642, School: 17, Amount: 20})
	if p, _ := tr.Power(); p[17] != 56 {
		t.Errorf("fury past the learned cap: %v", p)
	}
}

// Server messages from the retail capture of 2026-11-05 03:02 (no GUIDs in
// these bodies): Metamorphosis's charge category 2469 with 106,292 ms to go
// and one charge used; a recovery speed change of 1.0104 for Fracture (1552).
func TestRetailServerMessages(t *testing.T) {
	d := data()
	d.Categories[2469] = Category{Name: "Demon Hunter - Metamorphosis (Vengeance)", MaxCharges: 1, ChargeRecovery: 120000}
	tr := New(d)
	tr.Player = codec.GUID{Lo: 1}

	set, _ := hex.DecodeString("a5090000349f0100010000803f00")
	if !tr.Message("SMSG_SET_SPELL_CHARGES", 1000, set) {
		t.Fatal("SET_SPELL_CHARGES not taken")
	}
	if c := find(tr.Cooldowns(1000), 2469); c == nil || c.RemainingMs != 106292 || *c.Charges != 0 {
		t.Errorf("Metamorphosis: %+v", c)
	}

	cast(tr, 263642, 0, 0)
	rate, _ := hex.DecodeString("11060000ae47813f0000803f00") // category 0x611 is Infernal Strike: no effect on 1552
	tr.Message("SMSG_MODIFY_COOLDOWN", 500, rate)
	if c := find(tr.Cooldowns(500), 1552); c.RemainingMs != 5000 {
		t.Errorf("another category's rate moved Fracture: %+v", c)
	}
	frac, _ := hex.DecodeString("10060000ae47813f0000803f00") // 0x610 = 1552, change 1.01
	tr.Message("SMSG_MODIFY_COOLDOWN", 500, frac)
	if c := find(tr.Cooldowns(500), 1552); c.RemainingMs != 4950 { // 5000 / 1.01
		t.Errorf("Fracture after a 1.01 speed-up: %+v", c)
	}
}

// Blade Dance and Death Sweep share category 1640 with lengths of 15 s and
// 9 s (retail tables): after a Blade Dance, Death Sweep is ready at 9 s.
func TestSharedCategoryLengths(t *testing.T) {
	d := data()
	d.Spells[188499] = SpellInfo{GCD: 1500, Category: 1640, CatRecovery: 15000}
	d.Spells[210152] = SpellInfo{GCD: 1500, Category: 1640, CatRecovery: 9000}
	d.Names[188499], d.Names[210152] = "Blade Dance", "Death Sweep"
	tr := New(d)
	tr.Player = codec.GUID{Lo: 1}
	cast(tr, 188499, 0, 0)
	got := map[int32]uint32{}
	for _, c := range tr.Cooldowns(5000) {
		got[c.Spell] = c.RemainingMs
	}
	if got[188499] != 10000 || got[210152] != 4000 {
		t.Fatalf("at 5 s: %v", got)
	}
	for _, c := range tr.Cooldowns(9500) {
		if c.Spell == 210152 {
			t.Fatalf("Death Sweep still listed at 9.5 s: %+v", c)
		}
	}
}
