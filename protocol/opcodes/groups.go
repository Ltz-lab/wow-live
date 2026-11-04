package opcodes

import (
	"fmt"
	"sort"
	"strconv"
)

// Group is one protocol group: a contiguous opcode range owned by one
// subsystem. Build 70707 numbers every world message as
//
//	opcode = group << 16 | index
//
// and the client registers server-to-client groups by range (for example
// "ClientChat" owns 0x4C0000..0x4C0026). See docs/EVIDENCE.md E13.
type Group struct {
	Name      string    `json:"name"`
	Direction Direction `json:"direction"`
	Base      Hex32     `json:"base"`
	End       Hex32     `json:"end"` // exclusive
	// Indices, when present, lists the only indices known to exist in the
	// range. Client-to-server groups are sparse in this build: the client
	// carries a writer only for the messages it actually sends.
	Indices    []uint16   `json:"indices,omitempty"`
	Confidence Confidence `json:"confidence"`
	Evidence   string     `json:"evidence,omitempty"`
}

// Hex32 is a uint32 that is written as a hex string in table documents, so
// opcodes read the way they appear in disassembly.
type Hex32 uint32

// UnmarshalJSON accepts "0x4c0000" or a plain number.
func (h *Hex32) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return fmt.Errorf("opcodes: bad opcode value %s: %w", b, err)
	}
	*h = Hex32(v)
	return nil
}

// MarshalJSON writes the value as a hex string.
func (h Hex32) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%#06x"`, uint32(h))), nil
}

// ID returns the group number (the high 16 bits of its opcodes).
func (g Group) ID() uint16 { return uint16(g.Base >> 16) }

// Count reports how many opcodes the group is known to contain.
func (g Group) Count() int {
	if g.Indices != nil {
		return len(g.Indices)
	}
	return int(g.End - g.Base)
}

// Contains reports whether op is a known opcode of this group.
func (g Group) Contains(op uint32) bool {
	if op < uint32(g.Base) || op >= uint32(g.End) {
		return false
	}
	if g.Indices == nil {
		return true
	}
	idx := uint16(op - uint32(g.Base))
	i := sort.Search(len(g.Indices), func(i int) bool { return g.Indices[i] >= idx })
	return i < len(g.Indices) && g.Indices[i] == idx
}

// Label names an opcode of this group by position, e.g. "ClientChat/0x26".
// It is a locator, not a message name: per-message names are not shipped.
func (g Group) Label(op uint32) string {
	return fmt.Sprintf("%s/%#x", g.Name, op-uint32(g.Base))
}

func (t *Table) indexGroups() error {
	t.groupByID = map[Direction]map[uint16]*Group{ClientToServer: {}, ServerToClient: {}}
	for i := range t.Groups {
		g := &t.Groups[i]
		m := t.groupByID[g.Direction]
		switch {
		case g.Name == "":
			return fmt.Errorf("opcodes: group %d has no name", i)
		case m == nil:
			return fmt.Errorf("opcodes: group %s has invalid direction %q", g.Name, g.Direction)
		case g.Base&0xFFFF != 0:
			return fmt.Errorf("opcodes: group %s base %#x is not group-aligned", g.Name, uint32(g.Base))
		case g.End <= g.Base || g.End-g.Base > 0x10000:
			return fmt.Errorf("opcodes: group %s has invalid range [%#x,%#x)", g.Name, uint32(g.Base), uint32(g.End))
		}
		for j, idx := range g.Indices {
			if uint32(idx) >= uint32(g.End-g.Base) {
				return fmt.Errorf("opcodes: group %s index %#x outside its range", g.Name, idx)
			}
			if j > 0 && idx <= g.Indices[j-1] {
				return fmt.Errorf("opcodes: group %s indices are not strictly increasing", g.Name)
			}
		}
		if prev, dup := m[g.ID()]; dup {
			return fmt.Errorf("opcodes: group %#x claimed by both %s and %s", g.ID(), prev.Name, g.Name)
		}
		m[g.ID()] = g
	}
	return nil
}

// GroupOf returns the group that contains op in direction dir.
func (t *Table) GroupOf(dir Direction, op uint32) (Group, bool) {
	g, ok := t.groupByID[dir][uint16(op>>16)]
	if !ok || !g.Contains(op) {
		return Group{}, false
	}
	return *g, true
}

// Known reports whether op is a verified opcode number for dir, either as a
// named entry or as a member of a verified group.
func (t *Table) Known(dir Direction, op uint32) bool {
	if _, err := t.ByID(dir, op); err == nil {
		return true
	}
	g, ok := t.GroupOf(dir, op)
	return ok && g.Confidence == Verified
}

// NumberedCount reports how many opcodes in dir have a verified number.
func (t *Table) NumberedCount(dir Direction) int {
	n := 0
	for _, g := range t.Groups {
		if g.Direction == dir && g.Confidence == Verified {
			n += g.Count()
		}
	}
	return n
}
