package opcodes

import "testing"

// Numbers below are pinned to the decrypted 1.60.1.70707 client
// (tools/opcodegroups.py); regenerating the table must not move them.
func TestForeverGroupNumbering(t *testing.T) {
	tbl, err := ForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		dir   Direction
		op    uint32
		group string
	}{
		{ServerToClient, 0x470000, "Client"},
		{ServerToClient, 0x47038d, "Client"},
		{ServerToClient, 0x4c0026, "ClientChat"},
		{ServerToClient, 0x600081, "ClientMovement"},
		{ServerToClient, 0x6c0000, "ClientWalkIn"},
		{ServerToClient, 0x4e000d, "NetClientConnection"},
		{ServerToClient, 0x4e000e, "NetClientConnection"},
		{ClientToServer, 0x3e0005, "c2s_3e"},
	}
	for _, c := range cases {
		g, ok := tbl.GroupOf(c.dir, c.op)
		if !ok || g.Name != c.group {
			t.Errorf("GroupOf(%s, %#x) = %q, %v; want %q", c.dir, c.op, g.Name, ok, c.group)
		}
		if !tbl.Known(c.dir, c.op) {
			t.Errorf("Known(%s, %#x) = false", c.dir, c.op)
		}
	}
}

func TestForeverGroupBoundaries(t *testing.T) {
	tbl, _ := ForBuild(70707)
	for _, op := range []uint32{
		0x47038e, // one past the end of "Client"
		0x4c0027, // one past "ClientChat"
		0x4e000f, // one past the connection group
		0x490000, // gap between ClientAreaTrigger and ClientCache
	} {
		if tbl.Known(ServerToClient, op) {
			t.Errorf("Known(s2c, %#x) = true, want false", op)
		}
	}
	// Server-to-client numbers are never valid client-to-server numbers.
	if tbl.Known(ClientToServer, 0x470000) {
		t.Error("0x470000 must not be a client-to-server opcode")
	}
	// Client-to-server groups are sparse: 0x3e0000..0x3e031d has holes.
	g, _ := tbl.GroupOf(ClientToServer, 0x3e0005)
	missing := 0
	for i := uint32(0); i < 0x31e; i++ {
		if !g.Contains(0x3e0000 + i) {
			missing++
		}
	}
	if missing == 0 {
		t.Error("expected holes in sparse group c2s_3e")
	}
}

func TestForeverGroupTotals(t *testing.T) {
	tbl, _ := ForBuild(70707)
	if n := tbl.NumberedCount(ServerToClient); n != 1667+15 {
		t.Errorf("server-to-client numbered = %d, want %d (1667 registry + 15 connection)", n, 1667+15)
	}
	if n := tbl.NumberedCount(ClientToServer); n != 1045 {
		t.Errorf("client-to-server numbered = %d, want 1045", n)
	}
	g, _ := tbl.GroupOf(ServerToClient, 0x4c0026)
	if got := g.Label(0x4c0026); got != "ClientChat/0x26" {
		t.Errorf("Label = %q", got)
	}
}

func TestLoadRejectsBadGroups(t *testing.T) {
	bad := []string{
		`{"groups":[{"name":"A","direction":"server-to-client","base":"0x470001","end":"0x470002"}]}`,
		`{"groups":[{"name":"A","direction":"server-to-client","base":"0x470000","end":"0x470000"}]}`,
		`{"groups":[{"name":"A","direction":"server-to-client","base":"0x470000","end":"0x480001"}]}`,
		`{"groups":[{"name":"A","direction":"sideways","base":"0x470000","end":"0x470001"}]}`,
		`{"groups":[{"name":"A","direction":"server-to-client","base":"0x470000","end":"0x470002"},
		            {"name":"B","direction":"server-to-client","base":"0x470000","end":"0x470001"}]}`,
		`{"groups":[{"name":"A","direction":"client-to-server","base":"0x3e0000","end":"0x3e0004","indices":[3,1]}]}`,
		`{"groups":[{"name":"A","direction":"client-to-server","base":"0x3e0000","end":"0x3e0004","indices":[4]}]}`,
	}
	for i, raw := range bad {
		if _, err := Load([]byte(raw)); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
}
