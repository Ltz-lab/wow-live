package opcodes

import (
	"errors"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/version"
)

func TestForeverTableLoads(t *testing.T) {
	tbl, err := ForBuild(70707)
	if err != nil {
		t.Fatalf("ForBuild(70707): %v", err)
	}
	if err := tbl.CheckVersion(version.Build70707); err != nil {
		t.Fatalf("CheckVersion: %v", err)
	}
	if _, ok := tbl.ByName("CMSG_CONNECT_TO"); !ok {
		t.Fatal("expected CMSG_CONNECT_TO entry")
	}
}

func TestNameOnlyCannotResolveToNumber(t *testing.T) {
	tbl, err := ForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = tbl.RequireID("CMSG_CONNECT_TO")
	var nameOnly *ErrNameOnly
	if !errors.As(err, &nameOnly) {
		t.Fatalf("expected ErrNameOnly, got %v", err)
	}
	// Only the session/world entries (tools/sessionentries.py) carry numbers.
	if got := tbl.VerifiedCount(); got != 26 {
		t.Fatalf("verified entries = %d, want the 26 session entries", got)
	}
}

// Every numbered entry must be an opcode of its own table, and session/world
// must be able to resolve every name they ask for.
func TestNumberedEntriesAreKnown(t *testing.T) {
	for _, v := range version.Known() {
		tbl, err := ForBuild(v.Build)
		if err != nil {
			t.Fatal(err)
		}
		if err := tbl.CheckVersion(v); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range tbl.Entries {
			if e.ID == nil {
				continue
			}
			n++
			if !tbl.Known(e.Direction, *e.ID) {
				t.Errorf("%s %s %#08x is not an opcode of the table", v, e.Name, *e.ID)
			}
			if _, ok := tbl.GroupOf(e.Direction, *e.ID); !ok {
				t.Errorf("%s %s %#08x is in no group", v, e.Name, *e.ID)
			}
		}
		if n != wantNumbered[v.Build] {
			t.Errorf("%s: %d numbered entries, want %d", v, n, wantNumbered[v.Build])
		}
	}
}

func TestUnknownOpcodeIsAnError(t *testing.T) {
	tbl, _ := ForBuild(70707)
	_, err := tbl.ByID(ServerToClient, 0x1234)
	var unk *ErrUnknownOpcode
	if !errors.As(err, &unk) {
		t.Fatalf("expected ErrUnknownOpcode, got %v", err)
	}
}

func TestLoadRejectsDuplicates(t *testing.T) {
	raw := []byte(`{"product":"wowf","build":1,"entries":[
		{"name":"A","direction":"server-to-client","id":1},
		{"name":"B","direction":"server-to-client","id":1}]}`)
	if _, err := Load(raw); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestVersionMismatchIsRejected(t *testing.T) {
	tbl, _ := ForBuild(70707)
	v := version.Build70707
	v.Build++
	if err := tbl.CheckVersion(v); !errors.Is(err, version.ErrBuildMismatch) {
		t.Fatalf("expected build mismatch, got %v", err)
	}
}

// wantNumbered is how many entries each shipped table numbers. 70755 adds
// the world-entry messages (SMSG_CONNECT_TO and the second connection) and
// the four hotfix messages and the live housing check (E46).
var wantNumbered = map[int]int{70707: 27, 70755: 46}
