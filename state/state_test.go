package state

import (
	"testing"
)

func TestUpsertAndSnapshotAreIndependent(t *testing.T) {
	s := New()
	o := s.Upsert(Object{GUID: LowGUID(1), Kind: KindPlayer, Name: "Jack", Health: 100, Power: map[string]int64{"MANA": 50}})
	if o.GUID != LowGUID(1) {
		t.Fatalf("guid %v", o.GUID)
	}
	snap := s.Snapshot()
	if len(snap.Objects) != 1 || snap.Version == 0 {
		t.Fatalf("snapshot %+v", snap)
	}
	// Mutating the snapshot must not touch the store, and vice versa.
	snap.Objects[0].Power["MANA"] = 1
	live, _ := s.Object(LowGUID(1))
	if live.Power["MANA"] != 50 {
		t.Fatalf("snapshot aliased live power map: %d", live.Power["MANA"])
	}
	live.Power["MANA"] = 99
	if s.Snapshot().Objects[0].Power["MANA"] != 50 {
		t.Fatal("Object returned an aliased map")
	}
}

func TestMutateMissingObjectFails(t *testing.T) {
	s := New()
	if err := s.Mutate(LowGUID(0xdead), func(*Object) {}); err == nil {
		t.Fatal("expected error for missing object")
	}
}

func TestVersionIncreasesOnChange(t *testing.T) {
	s := New()
	v0 := s.Version()
	s.Upsert(Object{GUID: LowGUID(1), Kind: KindUnit})
	v1 := s.Version()
	if v1 <= v0 {
		t.Fatalf("version did not advance: %d -> %d", v0, v1)
	}
	s.Remove(LowGUID(1))
	if s.Version() <= v1 {
		t.Fatal("remove did not advance version")
	}
	if s.Remove(LowGUID(1)) {
		t.Fatal("second remove reported present")
	}
}

func TestSnapshotSortedByGUID(t *testing.T) {
	s := New()
	s.Upsert(Object{GUID: LowGUID(3), Kind: KindUnit})
	s.Upsert(Object{GUID: LowGUID(1), Kind: KindUnit})
	s.Upsert(Object{GUID: LowGUID(2), Kind: KindUnit})
	snap := s.Snapshot()
	for i, want := range []GUID{LowGUID(1), {Lo: 2}, {Lo: 3}} {
		if snap.Objects[i].GUID != want {
			t.Fatalf("position %d = %v, want %v", i, snap.Objects[i].GUID, want)
		}
	}
}

func TestConcurrentAccessIsSafe(t *testing.T) {
	s := New()
	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(w int) {
			for i := 0; i < 200; i++ {
				s.Upsert(Object{GUID: LowGUID(uint64(w*1000 + i)), Kind: KindUnit})
				_ = s.Snapshot()
				_ = s.Version()
			}
			done <- struct{}{}
		}(w)
	}
	for w := 0; w < 8; w++ {
		<-done
	}
	if s.Len() != 1600 {
		t.Fatalf("len %d", s.Len())
	}
}
