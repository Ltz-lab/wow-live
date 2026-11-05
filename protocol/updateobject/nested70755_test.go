package updateobject

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
	"github.com/Ltz-lab/wow-live/protocol/pktlog"
)

// The nested delta parts (delta_nested.go), checked against the client's
// own readers run in the emulator (tools/ufemu.py) over real captured bytes
// and over synthetic ones that reach the paths the captures do not.
//
// Every PlayerData delta of both 70755 captures is cut from its mask to the
// end of the block and run through 0x1008d4e40; the recorded run keeps the
// bytes the client consumed, its reads, and nothing else
// (testdata/ufemu/nested.json). Regenerate:
//
//	NESTED_CASES=/tmp/nested-cases.json go test ./protocol/updateobject -run TestWriteNestedCases
//	python3 tools/ufemu.py artifacts/WoWForever-arm64.decrypted \
//	    --cases /tmp/nested-cases.json --out /tmp/nested-results.json
//	NESTED_RESULTS=/tmp/nested-results.json go test ./protocol/updateobject -run TestPackNestedResults

// captures are the frozen sessions, outside the repository; tests that
// need them skip when they are absent.
var captures = []struct{ name, path string }{
	{"live", "~/wow/server/wow-forever-tc/captures/2026-11-05-live-goldshire-70755.pkt"},
	{"old", "~/wow/server/wow-forever-tc/captures/2026-11-04-goldshire-70755.pkt"},
}

const forkUpdateObject = 0x5c0000 // the fork's numbering in its logs

func capturePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

func loadCapture(t *testing.T, p string) *pktlog.Log {
	t.Helper()
	l, err := pktlog.ReadPKTFile(capturePath(p))
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			t.Skipf("capture absent: %s", p)
		}
		t.Fatal(err)
	}
	return l
}

type valuesAt struct {
	pkt, blk int
	typ      ObjectType
	data     []byte
}

// captureValues walks a log's SMSG_UPDATE_OBJECTs, tracking create types,
// and yields every values block.
func captureValues(t *testing.T, l *pktlog.Log, f func(valuesAt)) {
	t.Helper()
	types := map[GUID]ObjectType{}
	for _, p := range l.Packets {
		if p.Dir != pktlog.S2C || p.Opcode != forkUpdateObject {
			continue
		}
		u, err := Decode(p.Body)
		if err != nil {
			t.Fatalf("pkt %d: %v", p.Index, err)
		}
		for bi, bl := range u.Blocks {
			switch {
			case bl.Create != nil:
				types[bl.GUID] = bl.Create.ObjectType
			case bl.Values != nil:
				f(valuesAt{p.Index, bi, types[bl.GUID], bl.Values.Data})
			}
		}
	}
}

// structDeltas returns, for each struct delta in a values block, the
// bytes from that struct's mask to the end of the block, by the client's
// delta reader. The client's reader stops where its struct ends.
func structDeltas(typ ObjectType, data []byte) map[string][]byte {
	chain, ok := updateChains[typ]
	if !ok {
		return nil
	}
	d := &rd{r: codec.NewReader(data)}
	d.u8()
	if d.u8() != 0 {
		d.u8()
		for d.err == nil && d.u8() != FragmentEnd {
		}
	}
	if d.u8()&2 == 0 || d.err != nil {
		return nil
	}
	tm := d.u32()
	out := map[string][]byte{}
	var o ObjectDelta
	for _, st := range chain {
		if tm&(1<<st.bit) == 0 || d.err != nil {
			continue
		}
		if r, ok := deltaReaders[st.bit]; ok {
			out[r] = data[d.r.Pos():]
		}
		st.run(d, &o)
	}
	return out
}

// deltaReaders are the client's delta readers by TypeID bit.
var deltaReaders = map[uint]string{0: "0x1008ce4bc", 5: "0x1008e93e0", 6: "0x1008d4e40", 8: "0x1008b6b60", 1: "0x1008c9ec0"}

var deltaLayoutOf = map[string]*deltaLayout{"0x1008ce4bc": &objectDataDelta, "0x1008e93e0": &unitDataDelta,
	"0x1008d4e40": &playerDataDelta, "0x1008b6b60": &gameObjectDataDelta, "0x1008c9ec0": &itemDataDelta}

type nestedCase struct {
	name   string
	b      []byte
	reader string // empty: PlayerData, 0x1008d4e40
}

func nestedCases(t *testing.T) []nestedCase {
	var out []nestedCase
	seen := map[string]bool{}
	for _, c := range captures {
		l := loadCapture(t, c.path)
		captureValues(t, l, func(v valuesAt) {
			for r, b := range structDeltas(v.typ, v.data) {
				if r == "0x1008ce4bc" {
					continue // ObjectData: plain fields only
				}
				// Identical deltas (health ticks) are run once.
				if k := r + string(b); !seen[k] {
					seen[k] = true
					out = append(out, nestedCase{fmt.Sprintf("%s/%s/%d.%d", deltaLayoutOf[r].name, c.name, v.pkt, v.blk), b, r})
				}
			}
		})
	}
	out = append(out, syntheticPlayerDeltas(t)...)
	return append(out, syntheticListDeltas(t)...)
}

// readerVA is the case's delta reader: given, or by the struct name the
// case's name starts with.
func (c nestedCase) readerVA() string {
	if c.reader != "" {
		return c.reader
	}
	for r, l := range deltaLayoutOf {
		if strings.HasPrefix(c.name, l.name+"/") {
			return r
		}
	}
	panic("no reader for " + c.name)
}

func TestWriteNestedCases(t *testing.T) {
	path := os.Getenv("NESTED_CASES")
	if path == "" {
		t.Skip("set NESTED_CASES to write the emulator cases")
	}
	var out []ufemuCase
	for _, c := range nestedCases(t) {
		r := c.readerVA()
		out = append(out, ufemuCase{Name: c.name, Reader: r, Flags: "0", Hex: hex.EncodeToString(c.b), Delta: true,
			Init: readerInit[createReaderOf[r]]})
	}
	b, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d cases", len(out))
}

// nestedRun is one recorded client run, compact: the bytes it consumed and
// its reads as "pos:getter" (D reads as "pos:D/n").
type nestedRun struct {
	Name   string `json:"name"`
	Reader string `json:"reader"`
	Hex    string `json:"hex"`
	Reads  string `json:"reads"`
}

func TestPackNestedResults(t *testing.T) {
	path := os.Getenv("NESTED_RESULTS")
	if path == "" {
		t.Skip("set NESTED_RESULTS to pack the emulator results")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rs []ufemuResult
	if err := json.Unmarshal(b, &rs); err != nil {
		t.Fatal(err)
	}
	var out []nestedRun
	for _, r := range rs {
		if r.Error != nil {
			t.Fatalf("%s: client run failed: %s", r.Name, *r.Error)
		}
		var rd []string
		for _, e := range normClient(r.Reads) {
			rd = append(rd, e.String())
		}
		out = append(out, nestedRun{r.Name, r.Reader, r.Hex[:2*r.Consumed], strings.Join(rd, " ")})
	}
	j, _ := json.MarshalIndent(out, "", "")
	if err := os.WriteFile("testdata/ufemu/nested.json", append(j, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e traceEnt) String() string {
	s := strconv.Itoa(e.Pos) + ":" + e.W
	if e.N != 0 {
		s += "/" + strconv.Itoa(e.N)
	}
	return s
}

// TestNestedAgainstClient: for every recorded run, our PlayerData delta
// decoder makes the client's reads, at the same positions, and stops where
// the client stopped.
func TestNestedAgainstClient(t *testing.T) {
	b, err := os.ReadFile("testdata/ufemu/nested.json")
	if err != nil {
		t.Fatal(err)
	}
	var runs []nestedRun
	if err := json.Unmarshal(b, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) < 100 {
		t.Fatalf("%d recorded runs", len(runs))
	}
	for _, r := range runs {
		data, _ := hex.DecodeString(r.Hex)
		var tr []traceEnt
		d := &rd{r: codec.NewReader(data), trace: &tr}
		l := deltaLayoutOf[r.Reader]
		if l == nil {
			t.Fatalf("%s: reader %s", r.Name, r.Reader)
		}
		d.readDelta(l, nil)
		if d.err != nil {
			t.Errorf("%s: %v", r.Name, d.err)
			continue
		}
		var got []string
		for _, e := range tr {
			got = append(got, e.String())
		}
		if g := strings.Join(got, " "); g != r.Reads {
			t.Errorf("%s: reads differ\n client %s\n ours   %s", r.Name, r.Reads, g)
		}
		if d.r.Remaining() != 0 {
			t.Errorf("%s: the client consumed %d bytes, we %d", r.Name, len(data), d.r.Pos())
		}
	}
}

// pdDelta builds a PlayerData delta: the mask for bits (plus their group
// bits), the full-values flag, group 0's bits 1 and 2, then body.
func pdDelta(bits []int, full bool, b1, b2 bool, body func(w *codec.Writer)) []byte {
	l := &playerDataDelta
	m := make(mask, l.blocks)
	set := func(b int) { m[b/32] |= 1 << uint(b%32) }
	for _, b := range bits {
		set(b)
		if g, ok := l.groupOf(b); ok {
			set(g)
		}
	}
	w := codec.NewWriter()
	var present uint64
	for k := range m {
		if m[k] != 0 {
			present |= 1 << uint(k)
		}
	}
	_ = w.Bits(present, uint(l.blocks))
	for k := range m {
		if m[k] != 0 {
			_ = w.Bits(uint64(m[k]), 32)
		}
	}
	w.Bit(full)
	if l.effective(m, 1) {
		w.Bit(b1)
	}
	if l.effective(m, 2) {
		w.Bit(b2)
	}
	w.FlushBits()
	body(w)
	return w.Bytes()
}

// counter hands out distinct values.
type counter uint32

func (c *counter) next() uint32 { *c++; return uint32(*c)*0x01010101 ^ 0x00804020 }

func writeQuestLogFull(w *codec.Writer, c *counter) {
	w.Uint32(c.next())
	w.Uint16(uint16(c.next()))
	for j := 0; j < 24; j++ {
		w.Uint16(uint16(c.next()))
	}
	w.Uint64(uint64(c.next())<<32 | uint64(c.next()))
	w.Uint32(c.next())
	w.Uint32(c.next())
}

// writeQuestLogDelta writes a QuestLog nested delta with mask m (block 0).
func writeQuestLogDelta(w *codec.Writer, c *counter, m uint32) {
	w.FlushBits()
	w.Bit(m != 0)
	if m != 0 {
		_ = w.Bits(uint64(m), 32)
	}
	w.FlushBits()
	if m&1 != 0 {
		for b, n := range []int{4, 2, 8, 4, 4} {
			if m&(1<<uint(b+1)) == 0 {
				continue
			}
			switch n {
			case 2:
				w.Uint16(uint16(c.next()))
			case 4:
				w.Uint32(c.next())
			case 8:
				w.Uint64(uint64(c.next())<<32 | uint64(c.next()))
			}
		}
	}
	if m&(1<<6) != 0 {
		for j := 0; j < 24; j++ {
			if m&(1<<uint(7+j)) != 0 {
				w.Uint16(uint16(c.next()))
			}
		}
	}
}

func writeVisibleItemDelta(w *codec.Writer, c *counter, m uint32) {
	w.FlushBits()
	_ = w.Bits(uint64(m), 12)
	if m&1 != 0 {
		if m&2 != 0 {
			w.Bit(true)
		}
		if m&4 != 0 {
			w.Bit(false)
		}
	}
	w.FlushBits()
	if m&1 == 0 {
		return
	}
	for b, n := range []int{4, 4, 4, 4, 2, 2, 4, 1, 1} {
		if m&(1<<uint(b+3)) == 0 {
			continue
		}
		switch n {
		case 1:
			w.Uint8(uint8(c.next()))
		case 2:
			w.Uint16(uint16(c.next()))
		case 4:
			w.Uint32(c.next())
		}
	}
}

func writeForcedReactionDelta(w *codec.Writer, c *counter, m uint32) {
	w.FlushBits()
	_ = w.Bits(uint64(m), 3)
	w.FlushBits()
	if m&1 != 0 {
		if m&2 != 0 {
			w.Uint32(c.next())
		}
		if m&4 != 0 {
			w.Uint32(c.next())
		}
	}
}

// syntheticPlayerDeltas reach the paths the captures do not: the
// full-values flag, the map's own full byte, deleted map entries, every
// QuestLog, VisibleItem and ForcedReaction bit, masks with group 0 clear,
// and bits 1 and 2. Each also carries a plain field on either side.
func syntheticPlayerDeltas(t *testing.T) []nestedCase {
	var c counter
	all31 := uint32(0x7fffffff)
	return []nestedCase{
		{name: "PlayerData/synthetic/full-flag", b: pdDelta([]int{1, 2, 25, 26, 27, 58, 59, 232, 234}, true, true, false, func(w *codec.Writer) {
			w.Uint32(c.next()) // GuildTimeStamp (25)
			w.Uint32(3)        // QuestLogQuestIdToIndex: count, no full byte
			for i := 0; i < 3; i++ {
				w.Uint32(c.next())
				w.Uint32(uint32(i))
			}
			w.Uint32(c.next()) // PlayerTitle (27)
			writeQuestLogFull(w, &c)
			writeQuestLogFull(w, &c)
			writeQuestLogFull(w, &c)
			writeVisibleItemDelta(w, &c, 0xfff) // VisibleItems ignore the flag
		})},
		{name: "PlayerData/synthetic/map-full-byte", b: pdDelta([]int{26, 60, 234, 252, 261, 292}, false, false, false, func(w *codec.Writer) {
			w.Uint8(1)
			w.Uint32(2)
			for i := 0; i < 2; i++ {
				w.Uint32(c.next())
				w.Uint32(c.next())
			}
			writeQuestLogDelta(w, &c, all31)
			writeVisibleItemDelta(w, &c, 0xfff)
			writeVisibleItemDelta(w, &c, 0x0f9) // bits 0, 3..7
			writeForcedReactionDelta(w, &c, 7)
			writeForcedReactionDelta(w, &c, 5)
		})},
		{name: "PlayerData/synthetic/map-changes", b: pdDelta([]int{2, 26, 61, 62, 63, 64, 240, 241, 254, 270}, false, false, true, func(w *codec.Writer) {
			w.Uint8(0)
			w.Uint16(4)
			for _, st := range []uint8{1, 2, 0, 2} {
				w.Uint32(c.next())
				w.Uint8(st)
				if st != MapDeleted {
					w.Uint32(c.next())
				}
			}
			writeQuestLogDelta(w, &c, 0)               // block-present bit clear
			writeQuestLogDelta(w, &c, 0x3e)            // group 0 clear: nothing
			writeQuestLogDelta(w, &c, 1<<6|1<<7|1<<30) // progress only
			writeQuestLogDelta(w, &c, 1|1<<5|1<<3)     // EndTime, EnabledObjectivesMask
			writeVisibleItemDelta(w, &c, 0xffe)        // group 0 clear: nothing
			writeVisibleItemDelta(w, &c, 1|1<<6|1<<11) // Unk_0xC, SheatheCategory
			w.Uint32(c.next())                         // AvgItemLevel[0] (254)
			writeForcedReactionDelta(w, &c, 6)         // group 0 clear
		})},
	}
}

// TestCapturesDecodeEverything: every create and every values block of both
// frozen sessions decodes (the 29-minute live one: combat, loot, quests,
// spawns, despawns), each values block to its last byte.
func TestCapturesDecodeEverything(t *testing.T) {
	for _, c := range captures {
		t.Run(c.name, func(t *testing.T) {
			l := loadCapture(t, c.path)
			types := map[GUID]ObjectType{}
			var creates, values int
			for _, p := range l.Packets {
				if p.Dir != pktlog.S2C || p.Opcode != forkUpdateObject {
					continue
				}
				u, err := Decode(p.Body)
				if err != nil {
					t.Fatalf("pkt %d: %v", p.Index, err)
				}
				for _, bl := range u.Blocks {
					switch {
					case bl.Create != nil:
						creates++
						types[bl.GUID] = bl.Create.ObjectType
						if _, err := bl.Create.DecodeFields(); err != nil {
							t.Errorf("pkt %d create %v: %v", p.Index, bl.GUID, err)
						}
					case bl.Values != nil:
						values++
						if _, err := DecodeValues(types[bl.GUID], bl.Values); err != nil {
							t.Errorf("pkt %d values %v (type %d): %v", p.Index, bl.GUID, types[bl.GUID], err)
						}
					}
				}
			}
			t.Logf("%d creates, %d values blocks", creates, values)
			if values == 0 {
				t.Fatal("no values blocks")
			}
		})
	}
}

// TestLive70755QuestProgress: the two packets the live session used to skip
// (testdata/live70755/README.md). 9968 accepts quest 179 into quest log
// slot 1; 18314 moves its objective 4 to 2.
func TestLive70755QuestProgress(t *testing.T) {
	block := func(pkt string, i int) *ValuesBlock {
		b, err := os.ReadFile("testdata/live70755/" + pkt + "_SMSG_UPDATE_OBJECT.bin")
		if err != nil {
			t.Fatal(err)
		}
		u, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		self := GUID{Hi: 0x0801180000000000, Lo: 3}
		if u.Blocks[i].GUID != self || u.Blocks[i].Values == nil {
			t.Fatalf("pkt %s block %d: %+v", pkt, i, u.Blocks[i])
		}
		return u.Blocks[i].Values
	}
	p := &PlayerData{}
	d, err := DecodeValues(TypeActivePlayer, block("09968", 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ActivePlayerRaw) != 23 {
		t.Errorf("%d ActivePlayerData bytes, want 23 (the client's PlayerData reader stops 23 bytes short)", len(d.ActivePlayerRaw))
	}
	p.ApplyTo(d.Player)
	if len(p.QuestLogQuestIDToIndex) != 1 || p.QuestLogQuestIDToIndex[179] != 1 {
		t.Errorf("QuestLogQuestIdToIndex %v", p.QuestLogQuestIDToIndex)
	}
	if q := p.QuestLog[1]; q.QuestID != 179 || q.ObjectiveProgress != [24]int16{} {
		t.Errorf("QuestLog[1] %+v", q)
	}
	d, err = DecodeValues(TypeActivePlayer, block("18314", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Player) != 1 || d.Player[0].Name != "QuestLog.ObjectiveProgress[1][4]" {
		t.Fatalf("player %v", d.Player)
	}
	p.ApplyTo(d.Player)
	if q := p.QuestLog[1]; q.QuestID != 179 || q.ObjectiveProgress[4] != 2 {
		t.Errorf("QuestLog[1] %+v", q)
	}
}

// TestQuestIDToIndexChanges: the map's change states, from the synthetic
// client-checked case: 1 and 0 set, 2 deletes (and reads no value).
func TestQuestIDToIndexChanges(t *testing.T) {
	p := &PlayerData{QuestLogQuestIDToIndex: map[int32]int32{9: 4}}
	p.ApplyTo([]Field{
		{"QuestLogQuestIdToIndex.Full", WU8, uint8(0)},
		{"QuestLogQuestIdToIndex.changes", WU16, uint16(3)},
		{"QuestLogQuestIdToIndex.Key", WI32, int32(5)}, {"QuestLogQuestIdToIndex.State", WU8, uint8(1)}, {"QuestLogQuestIdToIndex.Value", WI32, int32(2)},
		{"QuestLogQuestIdToIndex.Key", WI32, int32(9)}, {"QuestLogQuestIdToIndex.State", WU8, uint8(2)},
		{"QuestLogQuestIdToIndex.Key", WI32, int32(7)}, {"QuestLogQuestIdToIndex.State", WU8, uint8(0)}, {"QuestLogQuestIdToIndex.Value", WI32, int32(3)},
	})
	if !reflect.DeepEqual(p.QuestLogQuestIDToIndex, map[int32]int32{5: 2, 7: 3}) {
		t.Errorf("%v", p.QuestLogQuestIDToIndex)
	}
	p.ApplyTo([]Field{{"QuestLogQuestIdToIndex.size", WU32, uint32(1)},
		{"QuestLogQuestIdToIndex.Key", WI32, int32(1)}, {"QuestLogQuestIdToIndex.Value", WI32, int32(0)}})
	if !reflect.DeepEqual(p.QuestLogQuestIDToIndex, map[int32]int32{1: 0}) {
		t.Errorf("full: %v", p.QuestLogQuestIDToIndex)
	}
}
