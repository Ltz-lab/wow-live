package opcodes

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/version"
)

// Pinned to the 70755 client (tools/opcodegroups.py on the 70755 dump) and to
// the code-identity rebase against 70707 (tools/opcoderebase.py,
// research/opcode_rebase_70755.json, EVIDENCE.md E31).

func bothTables(t *testing.T) (old, cur *Table) {
	t.Helper()
	old, err := ForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	cur, err = ForBuild(70755)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Protocol != "wowf 1.60.1.70755" || cur.CheckVersion(version.Build70755) != nil {
		t.Fatalf("70755 table identity %q %d", cur.Protocol, cur.Build)
	}
	return old, cur
}

func TestForeverIs70755(t *testing.T) {
	tbl, err := Forever()
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Build != 70755 || version.Forever().Build != 70755 {
		t.Fatalf("Forever() is build %d", tbl.Build)
	}
	if _, err := ForBuild(70658); err == nil {
		t.Fatal("ForBuild(70658) should fail")
	}
}

// Group counts: every group equal to 70707's except the two main groups, which
// each gained exactly one message.
func Test70755GroupCounts(t *testing.T) {
	old, cur := bothTables(t)
	if len(old.Groups) != 46 || len(cur.Groups) != 46 {
		t.Fatalf("groups: 70707 %d, 70755 %d", len(old.Groups), len(cur.Groups))
	}
	if got := cur.NumberedCount(ServerToClient); got != 1683 {
		t.Errorf("70755 s2c = %d, want 1683 (70707: %d)", got, old.NumberedCount(ServerToClient))
	}
	if got := cur.NumberedCount(ClientToServer); got != 1046 {
		t.Errorf("70755 c2s = %d, want 1046 (70707: %d)", got, old.NumberedCount(ClientToServer))
	}
	for i, g := range cur.Groups {
		o := old.Groups[i]
		if g.Name != o.Name || g.Base != o.Base || g.Direction != o.Direction {
			t.Fatalf("group %d: %s %#x vs 70707 %s %#x", i, g.Name, uint32(g.Base), o.Name, uint32(o.Base))
		}
		want := o.Count()
		if g.Name == "Client" || g.Name == "c2s_44" {
			want++
		}
		if g.Count() != want {
			t.Errorf("%s: %d opcodes, want %d (70707 %d)", g.Name, g.Count(), want, o.Count())
		}
	}
	if g, _ := cur.GroupOf(ServerToClient, 0x470000); uint32(g.End) != 0x47038F {
		t.Errorf("Client end %#x, want 0x47038f", uint32(g.End))
	}
}

// The shift boundaries, as the 70755 getters show them.
func Test70755ShiftBoundaries(t *testing.T) {
	old, cur := bothTables(t)
	c2s := func(tb *Table, idx uint32) bool { return tb.Known(ClientToServer, 0x440000|idx) }
	// 0x44: 0x6D, 0x6E, 0x70 keep their numbers (no 0x6F in either build);
	// 0x71 is gone, 0x72 is new, 70707 0x71.. sit two higher.
	for _, c := range []struct {
		idx      uint32
		old, now bool
	}{
		{0x6D, true, true}, {0x6E, true, true}, {0x6F, false, false}, {0x70, true, true},
		{0x71, true, false}, {0x72, true, true}, {0x73, true, true},
		{0x1B3, true, true}, {0x1B4, false, true}, {0x1B5, false, true}, {0x1B6, false, false},
	} {
		if c2s(old, c.idx) != c.old || c2s(cur, c.idx) != c.now {
			t.Errorf("c2s 0x44%04X: 70707 %v (want %v), 70755 %v (want %v)", c.idx, c2s(old, c.idx), c.old, c2s(cur, c.idx), c.now)
		}
	}
	if !cur.Known(ServerToClient, 0x47038E) || old.Known(ServerToClient, 0x47038E) {
		t.Error("s2c 0x47038e must exist in 70755 only")
	}
}

func rebaseMap(t *testing.T) map[uint32]struct{ old, code string } {
	t.Helper()
	raw, err := os.ReadFile("../../research/opcode_rebase_70755.json")
	if err != nil {
		t.Skip("the research inventories live in wow-forever:", err)
	}
	var doc struct {
		Opcodes []struct {
			Opcode string  `json:"opcode"`
			Old    *string `json:"old"`
			Code   string  `json:"code"`
		} `json:"opcodes"`
		VtableCheck map[string]struct {
			OK  int      `json:"ok"`
			Bad []string `json:"bad"`
		} `json:"vtable_check"`
		Unmapped []string `json:"unmapped_old"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for b, v := range doc.VtableCheck {
		if len(v.Bad) != 0 || v.OK < 1679 {
			t.Errorf("vtable check %s: %d ok, bad %v", b, v.OK, v.Bad)
		}
	}
	if len(doc.Unmapped) != 0 {
		t.Errorf("70707 opcodes with no 70755 counterpart: %v", doc.Unmapped)
	}
	m := map[uint32]struct{ old, code string }{}
	for _, o := range doc.Opcodes {
		n, _ := strconv.ParseUint(o.Opcode, 0, 32)
		old := ""
		if o.Old != nil {
			old = *o.Old
		}
		m[uint32(n)] = struct{ old, code string }{old, o.Code}
	}
	return m
}

// The client's own code decides where each 70707 message went. It agrees with
// the measured client shift (0x44: +2 from 70707 0x71) and disagrees with the
// measured server shift: the new server message is 0x4700EA, not 0x470125,
// and 70707 0x4700EA..0x470124 are one higher too.
func Test70755RebaseMap(t *testing.T) {
	m := rebaseMap(t)
	want := func(op uint32) uint32 {
		g, i := op>>16, op&0xFFFF
		switch {
		case g == 0x44 && i >= 0x73:
			return op - 2
		case g == 0x47 && i >= 0xEB:
			return op - 1
		}
		return op
	}
	for op, r := range m {
		if r.code == "new" {
			if op != 0x440072 && op != 0x4700EA {
				t.Errorf("unexpected new opcode %#08x", op)
			}
			continue
		}
		o, _ := strconv.ParseUint(r.old, 0, 32)
		if uint32(o) != want(op) {
			t.Errorf("70755 %#08x carries 70707 %s, want %#08x", op, r.old, want(op))
		}
	}
	for _, c := range []struct {
		op   uint32
		old  string
		code string
	}{
		{0x440014, "0x440014", "identical"},
		{0x440070, "0x440070", "identical-ambiguous"},
		{0x440073, "0x440071", "identical"},
		{0x470018, "0x470018", "changed"},
		{0x4700EC, "0x4700eb", "identical"},
		{0x470125, "0x470124", "identical"},
		{0x470126, "0x470125", "identical-ambiguous"},
	} {
		if r := m[c.op]; r.old != c.old || r.code != c.code {
			t.Errorf("%#08x: carries %q (%s), want %s (%s)", c.op, r.old, r.code, c.old, c.code)
		}
	}
}

func Test70755NameSpotChecks(t *testing.T) {
	old, err := NamesForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := NamesForBuild(70755)
	if err != nil {
		t.Fatal(err)
	}
	name := func(n *Names, d Direction, op uint32) string {
		e, _ := n.Lookup(d, op)
		return e.Name
	}
	for _, c := range []struct {
		dir  Direction
		op   uint32
		name string
	}{
		{ClientToServer, 0x440014, "CMSG_ENUM_CHARACTERS"},
		{ClientToServer, 0x440070, "CMSG_CREATE_CHARACTER"},
		{ClientToServer, 0x440073, "CMSG_CHECK_CHARACTER_NAME_AVAILABILITY"},
		{ClientToServer, 0x4400B7, "CMSG_QUERY_REALM_NAME"},
		{ClientToServer, 0x4400CC, "CMSG_SERVER_TIME_OFFSET_REQUEST"},
		{ClientToServer, 0x460001, "CMSG_AUTH_SESSION"},
		{ServerToClient, 0x470018, "SMSG_ENUM_CHARACTERS_RESULT"},
		{ServerToClient, 0x4700ED, "SMSG_ADDON_LIST_REQUEST"},
		{ServerToClient, 0x4700EE, "SMSG_ACHIEVEMENT_EARNED"},
		{ServerToClient, 0x470125, "SMSG_SET_TIME_ZONE_INFORMATION"},
		{ServerToClient, 0x470126, "SMSG_BATTLE_PET_CAGE_DATE_ERROR"},
		{ServerToClient, 0x470316, "SMSG_LATENCY_REPORT_PING"},
		{ServerToClient, 0x4C0005, "SMSG_MOTD"},
	} {
		if got := name(cur, c.dir, c.op); got != c.name {
			t.Errorf("70755 %#08x = %q, want %s", c.op, got, c.name)
		}
	}
	// name check: 70755 0x440073 is 70707 0x440071's name, two higher
	if name(cur, ClientToServer, 0x440073) != name(old, ClientToServer, 0x440071) {
		t.Error("0x440073 must carry 70707 0x440071's name")
	}
	// 0x126 is 70707 0x125's name; 0x125 is 70707 0x124's (not new)
	if name(cur, ServerToClient, 0x470126) != name(old, ServerToClient, 0x470125) ||
		name(cur, ServerToClient, 0x470125) != name(old, ServerToClient, 0x470124) {
		t.Error("0x470125/0x470126 must carry 70707 0x470124/0x470125")
	}
	// the two new messages are unnamed
	for _, c := range []struct {
		dir Direction
		op  uint32
	}{{ClientToServer, 0x440072}, {ServerToClient, 0x4700EA}} {
		e := entryAt(cur, c.dir, c.op)
		if e == nil || e.Name != "" || e.Confidence != NameNone || e.RebaseCode != "new" {
			t.Errorf("%#08x: %+v, want an unnamed new entry", c.op, e)
		}
	}
}

// Every carried entry has the 70707 name of the opcode it carries, and keeps
// its grade unless it moved on a shared body (then capped at medium).
func Test70755NamesCarried(t *testing.T) {
	old, _ := NamesForBuild(70707)
	cur, err := NamesForBuild(70755)
	if err != nil {
		t.Fatal(err)
	}
	m := rebaseMap(t)
	tbl, _ := ForBuild(70755)
	for _, dir := range []Direction{ServerToClient, ClientToServer} {
		if got, want := len(entriesFor(cur, dir)), tbl.NumberedCount(dir); got != want {
			t.Errorf("%s: %d name records, want %d", dir, got, want)
		}
	}
	rank := map[NameConfidence]int{NameHigh: 3, NameMedium: 2, NameLow: 1, NameNone: 0}
	for i := range cur.Entries {
		e := &cur.Entries[i]
		if e.RebaseCode == "new" {
			continue
		}
		from, _ := strconv.ParseUint(e.RebaseFrom, 0, 32)
		o := entryAt(old, e.Direction, uint32(from))
		if o == nil || o.Name != e.Name {
			t.Errorf("%s %s: name %q, 70707 %s has %+v", e.Direction, e.Opcode, e.Name, e.RebaseFrom, o)
			continue
		}
		if r, ok := m[e.ID()]; ok && r.code != e.RebaseCode {
			t.Errorf("%s: rebase_code %s, map says %s", e.Opcode, e.RebaseCode, r.code)
		}
		moved := uint32(from) != e.ID()
		switch {
		case !moved || e.RebaseCode == "identical":
			if e.Confidence != o.Confidence {
				t.Errorf("%s: confidence %s, 70707 %s", e.Opcode, e.Confidence, o.Confidence)
			}
		case rank[o.Confidence] > rank[NameMedium]:
			if e.Confidence != NameMedium || e.Confidence70707 != o.Confidence {
				t.Errorf("%s moved on a shared body: confidence %s (70707 %s)", e.Opcode, e.Confidence, o.Confidence)
			}
		default:
			if e.Confidence != o.Confidence {
				t.Errorf("%s: confidence %s, 70707 %s", e.Opcode, e.Confidence, o.Confidence)
			}
		}
	}
}

func entryAt(n *Names, d Direction, op uint32) *PublicName {
	for i := range n.Entries {
		if n.Entries[i].Direction == d && n.Entries[i].ID() == op {
			return &n.Entries[i]
		}
	}
	return nil
}

// The session/world entries move with the rebase.
func Test70755SessionEntries(t *testing.T) {
	old, cur := bothTables(t)
	for _, c := range []struct {
		name     string
		old, new uint32
	}{
		{"CMSG_ENUM_CHARACTERS", 0x440014, 0x440014},
		{"SMSG_ENUM_CHARACTERS_RESULT", 0x470018, 0x470018},
		{"CMSG_AUTH_SESSION", 0x460001, 0x460001},
		{"SMSG_AUTH_CHALLENGE", 0x4E0000, 0x4E0000},
		{"CMSG_SERVER_TIME_OFFSET_REQUEST", 0x4400CA, 0x4400CC},
		{"CMSG_QUERY_REALM_NAME", 0x4400B5, 0x4400B7},
		{"CMSG_TUTORIAL", 0x44010C, 0x44010E},
		{"SMSG_LOGOUT_COMPLETE", 0x470131, 0x470132},
	} {
		for _, x := range []struct {
			tb   *Table
			want uint32
		}{{old, c.old}, {cur, c.new}} {
			_, id, err := x.tb.RequireID(c.name)
			if err != nil || id != x.want {
				t.Errorf("%s %s = %#08x, %v; want %#08x", x.tb.Protocol, c.name, id, err, x.want)
			}
		}
	}
}
