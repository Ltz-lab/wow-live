package main

// Enemy players: the other faction's players near us, from the races the
// update fields carry (Forever; retail's fields do not decode yet), with how
// fast each is closing on us.

import (
	"math"
	"sort"
)

var allianceRaces = map[uint8]bool{1: true, 3: true, 4: true, 7: true, 11: true, 22: true, 25: true,
	29: true, 30: true, 32: true, 34: true, 37: true, 52: true}
var hordeRaces = map[uint8]bool{2: true, 5: true, 6: true, 8: true, 9: true, 10: true, 26: true,
	27: true, 28: true, 31: true, 35: true, 36: true, 70: true}

// faction is "alliance", "horde" or "" (unknown, or a neutral pandaren).
func faction(race uint8) string {
	switch {
	case allianceRaces[race]:
		return "alliance"
	case hordeRaces[race]:
		return "horde"
	}
	return ""
}

var classNames = map[uint8]string{1: "Warrior", 2: "Paladin", 3: "Hunter", 4: "Rogue", 5: "Priest",
	6: "Death Knight", 7: "Shaman", 8: "Mage", 9: "Warlock", 10: "Monk", 11: "Druid", 12: "Demon Hunter", 13: "Evoker"}

var raceNames = map[uint8]string{1: "Human", 2: "Orc", 3: "Dwarf", 4: "Night Elf", 5: "Undead", 6: "Tauren",
	7: "Gnome", 8: "Troll", 9: "Goblin", 10: "Blood Elf", 11: "Draenei"}

type threat struct {
	GUID  string  `json:"g"`
	Name  string  `json:"n,omitempty"`
	Level uint8   `json:"lvl,omitempty"`
	Class string  `json:"cls,omitempty"`
	Race  string  `json:"race,omitempty"`
	D     float64 `json:"d"`
	DZ    float64 `json:"dz"`
	Close float64 `json:"close"` // yards per second toward us (negative: moving away)
	Warn  bool    `json:"warn,omitempty"`
}

// Warning distances: anyone this close, or closing on us fast inside the
// outer one.
const (
	warnNear  = 60.0
	warnFar   = 120.0
	warnSpeed = 3.0
)

// threats lists the hostile players in objs, nearest first. A player is
// hostile when both factions are known and differ.
func threats(self *ent, objs []ent) []threat {
	mine := faction(self.Race)
	if mine == "" {
		return nil
	}
	var out []threat
	for i := range objs {
		o := &objs[i]
		if o.Kind != "player" {
			continue
		}
		f := faction(o.Race)
		if f == "" || f == mine {
			continue
		}
		o.Hostile = true
		dx, dy := float64(o.X-self.X), float64(o.Y-self.Y)
		d := math.Hypot(dx, dy)
		var closing float64
		if d > 0.1 {
			// their velocity toward us, less ours toward them
			closing = -(dx*float64(o.VX) + dy*float64(o.VY)) / d
			closing += (dx*float64(self.VX) + dy*float64(self.VY)) / d
		}
		t := threat{GUID: o.GUID, Name: o.Name, Level: o.Level, Class: classNames[o.Class], Race: raceNames[o.Race],
			D: math.Round(d*10) / 10, DZ: math.Round(float64(o.Z-self.Z)*10) / 10, Close: math.Round(closing*10) / 10}
		t.Warn = d < warnNear || d < warnFar && closing > warnSpeed
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].D < out[j].D })
	return out
}
