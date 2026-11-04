package main

import "testing"

func TestThreats(t *testing.T) {
	self := &ent{Kind: "self", Race: 3} // dwarf: alliance
	objs := []ent{
		{GUID: "orc", Kind: "player", Race: 2, Class: 4, Level: 24, X: 100, VX: -7}, // 100 yd, running at us
		{GUID: "gnome", Kind: "player", Race: 7, X: 10},                             // our side
		{GUID: "troll", Kind: "player", Race: 8, X: 0, Y: 150},                      // far, standing
		{GUID: "?", Kind: "player", X: 5},                                           // race unknown
		{GUID: "undead", Kind: "player", Race: 5, Y: -40, VY: -5},                   // near, running away
	}
	got := threats(self, objs)
	if len(got) != 3 {
		t.Fatalf("%d threats, want 3: %+v", len(got), got)
	}
	want := []struct {
		g    string
		warn bool
	}{{"undead", true}, {"orc", true}, {"troll", false}}
	for i, w := range want {
		if got[i].GUID != w.g || got[i].Warn != w.warn {
			t.Errorf("threat %d = %+v, want %s warn=%v", i, got[i], w.g, w.warn)
		}
	}
	if got[1].Close != 7 || got[1].Class != "Rogue" || got[0].Close != -5 {
		t.Errorf("closing speeds / class: %+v", got)
	}
	if !objs[0].Hostile || objs[1].Hostile {
		t.Error("hostile flags not set on the objects")
	}
	if threats(&ent{Kind: "self"}, objs) != nil {
		t.Error("threats with our own race unknown")
	}
}
