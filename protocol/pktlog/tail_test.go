package pktlog

import (
	"os"
	"path/filepath"
	"testing"
)

// Tail over a file written in small pieces gives what ReadPKT gives at once.
func TestTailMatchesReadPKT(t *testing.T) {
	src := filepath.Join(os.Getenv("HOME"), "Dev/wow/server/wow-forever-tc/captures/2026-11-04-goldshire-70755.pkt")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skip("no fork capture here:", err)
	}
	want, err := ReadPKT(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "grow.pkt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tail, err := OpenTail(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []Packet
	for p := 0; p < len(raw); p += 777 {
		e := min(p+777, len(raw))
		f.Write(raw[p:e])
		pk, err := tail.Next()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, pk...)
	}
	if len(got) != len(want.Packets) {
		t.Fatalf("tail %d packets, ReadPKT %d", len(got), len(want.Packets))
	}
	for i := range got {
		if got[i].Opcode != want.Packets[i].Opcode || got[i].TicksMs != want.Packets[i].TicksMs || string(got[i].Body) != string(want.Packets[i].Body) {
			t.Fatalf("packet %d differs", i)
		}
	}
}
