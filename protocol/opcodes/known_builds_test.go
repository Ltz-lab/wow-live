package opcodes

import (
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/version"
)

// TestBuild70755Table pins the 70755 opcode table. The numbering was rebased
// onto the 70755 client by code identity (docs/EVIDENCE.md E31): the server
// insert is at 0x4700EA, the client insert at 0x440072.
func TestBuild70755Table(t *testing.T) {
	v, ok := version.Lookup(70755)
	if !ok {
		t.Skip("70755 is not in the version registry")
	}
	tbl, err := ForBuild(v.Build)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tbl.NumberedCount(ServerToClient), 1683; got != want {
		t.Errorf("server-to-client numbered = %d, want %d", got, want)
	}
	if got, want := tbl.NumberedCount(ClientToServer), 1046; got != want {
		t.Errorf("client-to-server numbered = %d, want %d", got, want)
	}
	if !tbl.Known(ServerToClient, 0x4700ea) {
		t.Error("0x4700ea (new server opcode in 70755) is not known")
	}
	if !tbl.Known(ClientToServer, 0x440072) {
		t.Error("0x440072 (new client opcode in 70755) is not known")
	}
	n, err := NamesForBuild(v.Build)
	if err != nil {
		t.Fatalf("NamesForBuild(70755): %v", err)
	}
	if n.Protocol != tbl.Protocol {
		t.Errorf("names protocol = %q, table = %q", n.Protocol, tbl.Protocol)
	}
}
