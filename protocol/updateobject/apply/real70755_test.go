package apply

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Ltz-lab/wow-live/state"
)

// TestReal70755Session replays every SMSG_UPDATE_OBJECT of the 70755
// Goldshire capture (private local server; ../testdata/real70755/README.md)
// into a fresh state, in capture order, and checks the world it ends with.
func TestReal70755Session(t *testing.T) {
	files, err := filepath.Glob("../testdata/real70755/*_SMSG_UPDATE_OBJECT.bin")
	if err != nil || len(files) != 88 {
		t.Fatalf("%d packets (%v)", len(files), err)
	}
	sort.Strings(files)
	s := state.New()
	var skipped []Skip
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		skipped = append(skipped, mustApply(t, s, b).Skipped...)
	}
	// Every block applies, the quest log and equipment deltas included.
	if len(skipped) != 0 {
		t.Fatalf("skipped %+v", skipped)
	}

	snap := s.Snapshot()
	kinds := map[state.Kind]int{}
	for _, o := range snap.Objects {
		kinds[o.Kind]++
	}
	want := map[state.Kind]int{state.KindGameObject: 70, state.KindItem: 19, state.KindPlayer: 1, state.KindUnit: 51}
	if len(snap.Objects) != 141 || len(kinds) != len(want) {
		t.Errorf("%d objects %v, want 141 %v", len(snap.Objects), kinds, want)
	}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%d %s, want %d", kinds[k], k, n)
		}
	}

	// Self: the level-61 dwarf shaman, at full health after the fight; the
	// maximum went 2260 -> 2430 when an item was equipped (packet 1403).
	selfGUID := state.GUID{Hi: 0x0801180000000000, Lo: 3}
	if snap.Self != selfGUID {
		t.Fatalf("self %v", snap.Self)
	}
	me, _ := s.Object(selfGUID)
	if me.Kind != state.KindPlayer || me.Name != "Qweqwee" || me.Level != 61 || me.Race != 3 || me.Class != 7 ||
		me.Health != 2430 || me.MaxHealth != 2430 {
		t.Errorf("self %+v", me)
	}
	// The item equipped in packet 1403 (VisibleItems[14], a nested delta).
	if me.Equipment[14] != 21710 || me.Equipment[15] != 36 {
		t.Errorf("equipment %v", me.Equipment)
	}

	byEntry := map[uint32][]state.Object{}
	for _, o := range snap.Objects {
		byEntry[o.Entry] = append(byEntry[o.Entry], o)
	}
	near := func(a, b float32) bool { return a-b < 0.01 && b-a < 0.01 }
	for _, w := range []struct {
		entry   uint32
		level   uint8
		health  int64
		max     int64
		x, y, z float32
	}{
		{240, 25, 713, 713, -9465.52, 74.01, 56.78},    // Marshal Dughan
		{295, 30, 1003, 1003, -9462.66, 16.19, 57.05},  // Innkeeper Farley
		{54, 10, 198, 198, -9464.87, 93.99, 58.53},     // Corina Steele (a vendor: FVendor_C)
		{113, 5, 0, 102, -9574.88, 121.39, 59.56},      // a Stonetusk Boar, killed
		{6749, 30, 1003, 1003, -9465.95, 48.14, 56.97}, // Erma
	} {
		cands := byEntry[w.entry]
		found := false
		for _, o := range cands {
			if o.Kind == state.KindUnit && o.Level == w.level && o.Health == w.health && o.MaxHealth == w.max &&
				near(o.Position.X, w.x) && near(o.Position.Y, w.y) && near(o.Position.Z, w.z) {
				found = true
			}
		}
		if !found {
			t.Errorf("no unit %d level %d %d/%d at (%.2f, %.2f, %.2f); have %+v", w.entry, w.level, w.health, w.max, w.x, w.y, w.z, cands)
		}
	}

	mailbox := byEntry[142075]
	if len(mailbox) != 1 || mailbox[0].GameObject == nil || mailbox[0].GameObject.Type != 19 || mailbox[0].GameObject.Display != 1907 ||
		!near(mailbox[0].Position.X, -9456.0) {
		t.Errorf("mailbox %+v", mailbox)
	}
	worn := byEntry[36] // Worn Mace
	if len(worn) != 1 || worn[0].Item == nil || worn[0].Item.Owner != selfGUID || worn[0].Item.Durability != 32 || worn[0].Item.MaxDurability != 35 {
		t.Errorf("item 36 %+v", worn)
	}
}
