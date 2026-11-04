package combatlog

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

func fixtures(t *testing.T) map[string][]byte {
	f, err := os.Open("testdata/fork-hogger.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string][]byte{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "#") {
			continue
		}
		p := strings.Split(sc.Text(), "\t")
		b, err := hex.DecodeString(p[2])
		if err != nil {
			t.Fatal(err)
		}
		out[p[0]] = b
	}
	return out
}

func one(t *testing.T, l *Log, fx map[string][]byte, name string) []Event {
	t.Helper()
	evs, err := l.Feed(name, 0, fx[name])
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatalf("%s: no events", name)
	}
	return evs
}

// Values read off the fork's World.pkt and its Server.log for the same fight.
func TestForkHoggerFight(t *testing.T) {
	fx := fixtures(t)
	l := New()

	auras := one(t, l, fx, "SMSG_AURA_UPDATE")
	if auras[0].Sub != "SPELL_AURA_APPLIED" || auras[0].SpellID != 1259719 { // Earth Fury
		t.Errorf("aura update: %+v", auras[0])
	}
	if again, _ := l.Feed("SMSG_AURA_UPDATE", 0, fx["SMSG_AURA_UPDATE"]); again[0].Sub != "SPELL_AURA_REFRESH" {
		t.Errorf("same slot, same spell again: %+v", again[0])
	}

	if e := one(t, l, fx, "SMSG_SPELL_START")[0]; e.Sub != "SPELL_CAST_START" || e.SpellID != 331 || e.Amount != 1500 {
		t.Errorf("Healing Wave start: %+v", e)
	}
	if e := one(t, l, fx, "SMSG_SPELL_HEAL_LOG")[0]; e.SpellID != 331 || e.Amount != 52 || e.Over != 52 {
		t.Errorf("Healing Wave heal: %+v", e)
	}
	if e := one(t, l, fx, "SMSG_SPELL_NON_MELEE_DAMAGE_LOG")[0]; e.Sub != "SPELL_DAMAGE" || e.SpellID != 17364 || e.Amount != 71 || e.Dest.Hi>>6&0x7FFFFF != 448 {
		t.Errorf("Stormstrike on Hogger: %+v", e)
	}
	if e := one(t, l, fx, "SMSG_ATTACKER_STATE_UPDATE")[0]; e.Sub != "SWING_DAMAGE" || e.Amount != 18 || !e.Crit {
		t.Errorf("Hogger's crit: %+v", e)
	}
	if e := one(t, l, fx, "SMSG_SPELL_MISS_LOG")[0]; e.SpellID != 6730 || e.Extra != 7 {
		t.Errorf("Head Butt miss: %+v", e)
	}
	if e := one(t, l, fx, "SMSG_PARTY_KILL_LOG")[0]; e.Sub != "PARTY_KILL" || e.Dest.Hi>>6&0x7FFFFF != 448 {
		t.Errorf("Hogger killed: %+v", e)
	}
	one(t, l, fx, "SMSG_SPELL_GO")
	one(t, l, fx, "SMSG_SPELL_ENERGIZE_LOG")

	if _, err := l.Feed("SMSG_CHAT", 0, nil); err != ErrUnknown {
		t.Errorf("SMSG_CHAT: %v", err)
	}
}

// A timed aura, written in the order of the fork's AuraUpdate::Write and
// AuraDataInfo (the fork's own packets never set AFLAG_DURATION: 0 of 8,229
// aura entries in its World.pkt), then removed.
func TestTimedAura(t *testing.T) {
	var w codec.Writer
	unit := codec.GUID{Hi: 0x0801180000000000, Lo: 3}
	caster := codec.GUID{Hi: 0x0801180000000000, Lo: 7}
	_ = w.Bits(0, 1) // UpdateAll
	_ = w.Bits(1, 9) // one aura
	w.FlushBits()
	w.GUID(unit)
	w.Uint16(5) // slot
	w.Bit(true)
	w.FlushBits()
	w.GUID(codec.GUID{}) // CastID
	w.Int32(1243)        // Power Word: Fortitude
	w.Int32(0)
	w.Int32(0)          // Visual
	w.Uint16(0x4 | 0x2) // DURATION | POSITIVE
	w.Uint32(1)         // ActiveFlags
	w.Uint16(60)        // CastLevel
	w.Uint8(3)          // Applications
	w.Int32(0)          // ContentTuningID
	w.Float32(0)
	w.Float32(0)
	w.Float32(0)                                               // DstLocation
	for _, b := range []bool{true, false, true, true, false} { // caster, item, duration, remaining, time mod
		w.Bit(b)
	}
	_ = w.Bits(0, 6)
	_ = w.Bits(0, 6)
	w.Bit(false) // content tuning
	w.FlushBits()
	w.GUID(caster)
	w.Uint32(1_800_000) // 30 min
	w.Uint32(1_799_000)

	l := New()
	evs, err := l.Feed("SMSG_AURA_UPDATE", 10_000, w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Sub != "SPELL_AURA_APPLIED" || evs[0].Source != caster || evs[0].Amount != 1_799_000 {
		t.Fatalf("events %+v", evs)
	}
	a := l.Auras(unit)
	if len(a) != 1 || a[0].DurationMs != 1_800_000 || a[0].ExpiresMs != 1_809_000 || a[0].Stacks != 3 {
		t.Fatalf("auras %+v", a)
	}

	var rm codec.Writer
	_ = rm.Bits(0, 1)
	_ = rm.Bits(1, 9)
	rm.FlushBits()
	rm.GUID(unit)
	rm.Uint16(5)
	rm.Bit(false)
	rm.FlushBits()
	evs, err = l.Feed("SMSG_AURA_UPDATE", 20_000, rm.Bytes())
	if err != nil || len(evs) != 1 || evs[0].Sub != "SPELL_AURA_REMOVED" || evs[0].SpellID != 1243 || len(l.Auras(unit)) != 0 {
		t.Fatalf("removal: %+v %v", evs, err)
	}
}
