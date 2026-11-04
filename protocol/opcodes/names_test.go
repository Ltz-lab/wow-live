package opcodes

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestForeverNamesLoad(t *testing.T) {
	n, err := NamesForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := ForBuild(70707)
	for _, dir := range []Direction{ServerToClient, ClientToServer} {
		if got, want := len(entriesFor(n, dir)), tbl.NumberedCount(dir); got != want {
			t.Errorf("%s: %d name records, want one per numbered opcode (%d)", dir, got, want)
		}
	}
	// Pinned so a regeneration that changes coverage is noticed.
	// 1,422 / 1,023 public names, minus stale copies a binary verdict moved
	// to another opcode (20 net per direction).
	if got := n.Count(ServerToClient, NameLow); got != 1402 {
		t.Errorf("named S2C = %d, want 1402", got)
	}
	if got := n.Count(ClientToServer, NameLow); got != 1003 {
		t.Errorf("named C2S = %d, want 1003", got)
	}
}

func entriesFor(n *Names, dir Direction) []PublicName {
	var out []PublicName
	for _, e := range n.Entries {
		if e.Direction == dir {
			out = append(out, e)
		}
	}
	return out
}

func TestForeverNamesSpotChecks(t *testing.T) {
	n, err := NamesForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dir  Direction
		op   uint32
		name string
	}{
		{ServerToClient, 0x4E0000, "SMSG_AUTH_CHALLENGE"},
		{ServerToClient, 0x4E000D, "SMSG_MULTIPLE_PACKETS"},
		{ServerToClient, 0x4E000E, "SMSG_COMPRESSED_PACKET"},
		{ServerToClient, 0x4C0001, "SMSG_CHAT"},
		// The fork's chat fix (Classic has two extra messages at 4-5) is
		// what the 70707 group size (39) supports; plain retail+1 is not.
		{ServerToClient, 0x4C001B, "SMSG_CHANNEL_NOTIFY_JOINED"},
		{ClientToServer, 0x2D0023, "CMSG_CHAT_MESSAGE_SAY"},
		// Binary verdicts that contradict names both public sources agree on.
		{ServerToClient, 0x4C0005, "SMSG_MOTD"},
		{ServerToClient, 0x530019, "SMSG_GUILD_CHALLENGE_UPDATE"},
		{ServerToClient, 0x53001A, "SMSG_GUILD_CHALLENGE_COMPLETED"},
	} {
		e, ok := n.Lookup(c.dir, c.op)
		if !ok || e.Name != c.name {
			t.Errorf("%s %#x = %q, want %s", c.dir, c.op, e.Name, c.name)
		}
	}
}

func TestLoadNamesRejectsUnknownOpcode(t *testing.T) {
	tbl, _ := ForBuild(70707)
	doc := `{"protocol":"wowf 1.60.1.70707","entries":[{"opcode":"0x7F0000","direction":"server-to-client","name":"SMSG_X","confidence":"low"}]}`
	if _, err := LoadNames([]byte(doc), tbl); err == nil || !strings.Contains(err.Error(), "not an opcode") {
		t.Fatalf("err = %v", err)
	}
	doc = `{"protocol":"other","entries":[]}`
	if _, err := LoadNames([]byte(doc), tbl); err == nil {
		t.Fatal("protocol mismatch accepted")
	}
}

func TestPublicNamesNeverVerify(t *testing.T) {
	// A public name must not turn into a verified table entry.
	tbl, _ := ForBuild(70707)
	if _, err := tbl.ByID(ServerToClient, 0x4C0001); err == nil {
		t.Fatal("SMSG_CHAT resolved as a table entry; public names must stay hints")
	}
}

func TestDescribe(t *testing.T) {
	if got := DescribeBuild(70707, ServerToClient, 0x4C0001); got != "ClientChat/0x1 ~SMSG_CHAT (high)" {
		t.Errorf("Describe = %q", got)
	}
	if got := DescribeBuild(70707, ServerToClient, 0x7F0000); got != "0x007f0000" {
		t.Errorf("Describe(outside) = %q", got)
	}
}

// Every verified or supported verdict from the client binary must be the name
// in the table, at high or medium confidence. This is the regression test for
// verdicts being dropped when the public sources agreed with each other.
func TestBinaryVerdictsApplied(t *testing.T) {
	n, err := NamesForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		dir  Direction
	}{
		{"../../research/s2c_handlers.json", ServerToClient},
		{"../../research/c2s_writes.json", ClientToServer},
	} {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			t.Skip("the research inventories live in wow-forever:", err)
		}
		var doc struct {
			Opcodes []struct {
				Opcode  string `json:"opcode"`
				Verdict struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"verdict"`
			} `json:"opcodes"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		applied := 0
		for _, o := range doc.Opcodes {
			v := o.Verdict
			want := map[string]NameConfidence{"verified": NameHigh, "supported": NameMedium}[v.Status]
			if v.Name == "" || want == "" {
				continue
			}
			op, _ := strconv.ParseUint(o.Opcode, 0, 32)
			e, ok := n.Lookup(c.dir, uint32(op))
			switch {
			case !ok || e.Name != v.Name:
				t.Errorf("%s %s: table has %q, binary verdict (%s) says %s", c.dir, o.Opcode, e.Name, v.Status, v.Name)
			case e.Confidence != NameHigh && !(want == NameMedium && e.Confidence == NameMedium):
				t.Errorf("%s %s %s: confidence %s, want at least %s", c.dir, o.Opcode, v.Name, e.Confidence, want)
			default:
				applied++
			}
		}
		if applied == 0 {
			t.Errorf("%s: no verdicts found in %s", c.dir, c.path)
		}
	}
}

// A name on two opcodes of one direction is only allowed as an explicitly
// contested pair: low confidence, each pointing at the other.
func TestDuplicateNamesAreMarked(t *testing.T) {
	n, err := NamesForBuild(70707)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[[2]string][]PublicName{}
	for _, e := range n.Entries {
		if e.Name != "" {
			k := [2]string{string(e.Direction), e.Name}
			seen[k] = append(seen[k], e)
		}
	}
	for k, es := range seen {
		if len(es) < 2 {
			continue
		}
		for _, e := range es {
			if e.Confidence != NameLow || len(e.DuplicateOf) != len(es)-1 {
				t.Errorf("%s %s at %s: duplicate not marked (confidence %s, duplicate_of %v)",
					k[0], k[1], e.Opcode, e.Confidence, e.DuplicateOf)
			}
		}
	}
	// A displaced entry is unnamed and points at where its name went.
	if e, ok := n.Lookup(ServerToClient, 0x4C0003); ok {
		t.Errorf("0x4C0003 still named %s; SMSG_MOTD belongs to 0x4C0005", e.Name)
	}
}
