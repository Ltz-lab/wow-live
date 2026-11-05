// Command combatlog prints the combat log of a recorded session, rebuilt from
// the server's messages (protocol/combatlog), in the shape of the old
// COMBAT_LOG_EVENT_UNFILTERED / WoWCombatLog.txt lines.
//
//	go run ./tools/combatlog <fork>/logs/World.pkt         # the fork's packet log
//	go run ./tools/combatlog decrypted/conn-55882.jsonl                  # a worlddecrypt connection
//
// Spell names come from forever.db (-db, via the sqlite3 CLI); creatures show
// as Creature-<entry>-<spawn>, players as Player-<low guid>. -stats prints counts only.
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Ltz-lab/wow-live/protocol/codec"
	"github.com/Ltz-lab/wow-live/protocol/combatlog"
	"github.com/Ltz-lab/wow-live/protocol/pktlog"
)

func main() {
	home, _ := os.UserHomeDir()
	opcodesH := flag.String("opcodes-h", filepath.Join(envOr("WOW_TC", filepath.Join(home, "Dev/wow/server/wow-forever-tc")), "TrinityCore/src/server/game/Server/Protocol/Opcodes.h"), "the fork's Opcodes.h, to name a World.pkt's numbers")
	db := flag.String("db", envOr("WOW_FOREVER_DB", filepath.Join(home, "Dev/wow/host/data/forever.db")), "forever.db for spell names (empty: ids only)")
	spellCSV := flag.String("spells", "", "SpellName.csv (ID,Name_lang), e.g. from wago.tools/db2/SpellName/csv?build=12.1.5.70783 for retail; overrides -db")
	stats := flag.Bool("stats", false, "print event and failure counts only")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: combatlog [flags] <World.pkt | conn-*.jsonl>")
		os.Exit(2)
	}
	msgs, err := load(flag.Arg(0), *opcodesH)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spells := spellNames(*db)
	if *spellCSV != "" {
		if err := readSpellCSV(*spellCSV, spells); err != nil {
			fmt.Fprintln(os.Stderr, "combatlog:", err)
			os.Exit(1)
		}
	}

	l := combatlog.New()
	subs := map[string]int{}
	seen := map[string]int{}
	failed := map[string]int{}
	var firstErr = map[string]string{}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for _, m := range msgs {
		evs, err := l.Feed(m.name, m.t, m.body)
		if err == combatlog.ErrUnknown {
			continue
		}
		seen[m.name]++
		if err != nil {
			failed[m.name]++
			if firstErr[m.name] == "" {
				firstErr[m.name] = err.Error()
			}
			continue
		}
		for _, ev := range evs {
			subs[ev.Sub]++
			if !*stats {
				fmt.Fprintln(w, line(ev, spells))
			}
		}
	}
	{
		fmt.Fprintln(os.Stderr, "messages decoded (failed / seen):")
		for _, n := range sortedKeys(seen) {
			fmt.Fprintf(os.Stderr, "  %-34s %4d / %4d  %s\n", n, failed[n], seen[n], firstErr[n])
		}
		fmt.Fprintln(os.Stderr, "events:")
		for _, n := range sortedKeys(subs) {
			fmt.Fprintf(os.Stderr, "  %-26s %5d\n", n, subs[n])
		}
	}
}

type msg struct {
	name string
	t    uint32
	body []byte
}

func load(path, opcodesH string) ([]msg, error) {
	if strings.HasSuffix(path, ".jsonl") {
		return loadJSONL(path)
	}
	names, err := forkNames(opcodesH)
	if err != nil {
		return nil, err
	}
	lg, err := pktlog.ReadPKTFile(path)
	if err != nil {
		return nil, err
	}
	var out []msg
	for _, p := range lg.Packets {
		if p.Dir != pktlog.S2C {
			continue
		}
		out = append(out, msg{names[p.Opcode], p.TicksMs, p.Body})
	}
	return out, nil
}

var tilde = regexp.MustCompile(`~(SMSG_\w+)`)

func loadJSONL(path string) ([]msg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var out []msg
	for sc.Scan() {
		var d struct {
			T, Dir, Name, Body string
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.Dir != "s2c" {
			continue
		}
		m := tilde.FindStringSubmatch(d.Name)
		if m == nil {
			continue
		}
		b, _ := hex.DecodeString(d.Body)
		out = append(out, msg{m[1], clockMs(d.T), b})
	}
	return out, sc.Err()
}

func clockMs(t string) uint32 {
	var h, m int
	var s float64
	fmt.Sscanf(t, "%d:%d:%f", &h, &m, &s)
	return uint32((h*3600+m*60)*1000 + int(s*1000))
}

var opLine = regexp.MustCompile(`\b(SMSG_\w+)\s*=\s*(0x[0-9A-Fa-f]+)`)

func forkNames(path string) (map[uint32]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[uint32]string{}
	for _, m := range opLine.FindAllStringSubmatch(string(b), -1) {
		v, _ := strconv.ParseUint(m[2], 0, 32)
		out[uint32(v)] = m[1]
	}
	return out, nil
}

func spellNames(db string) map[int32]string {
	out := map[int32]string{}
	if db == "" {
		return out
	}
	b, err := exec.Command("sqlite3", "-separator", "\t", db, "select id, name from spells").Output()
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		id, name, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		if v, err := strconv.Atoi(id); err == nil {
			out[int32(v)] = name
		}
	}
	return out
}

// unit names a GUID: type from the high 6 bits, entry from bits 6..28.
func unit(g codec.GUID) string {
	if g.IsEmpty() {
		return "nil"
	}
	switch g.Hi >> 58 {
	case 2:
		return fmt.Sprintf("Player-%d", g.Lo&0xFFFFFFFFFF)
	case 8, 9, 12:
		return fmt.Sprintf("Creature-%d-%010X", (g.Hi>>6)&0x7FFFFF, g.Lo&0xFFFFFFFFFF) // entry, spawn (as CLEU)
	default:
		return g.String()
	}
}

func line(ev combatlog.Event, spells map[int32]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%8.3f  %-24s %-16s > %-16s", float64(ev.TimeMs)/1000, ev.Sub, unit(ev.Source), unit(ev.Dest))
	if ev.SpellID != 0 {
		name := spells[ev.SpellID]
		if name == "" {
			name = "?"
		}
		fmt.Fprintf(&b, " %d %q", ev.SpellID, name)
	}
	switch {
	case strings.HasSuffix(ev.Sub, "_DAMAGE") || strings.HasSuffix(ev.Sub, "_HEAL") || strings.HasSuffix(ev.Sub, "ENERGIZE") || strings.HasSuffix(ev.Sub, "LEECH"):
		fmt.Fprintf(&b, " amount=%d", ev.Amount)
		if ev.Over > 0 {
			fmt.Fprintf(&b, " over=%d", ev.Over)
		}
		if ev.Absorbed != 0 {
			fmt.Fprintf(&b, " absorbed=%d", ev.Absorbed)
		}
		if ev.Crit {
			b.WriteString(" crit")
		}
	case strings.HasPrefix(ev.Sub, "SPELL_AURA_APPLIED") || ev.Sub == "SPELL_AURA_REFRESH":
		if strings.HasSuffix(ev.Sub, "DOSE") {
			fmt.Fprintf(&b, " stacks=%d", ev.Amount)
		} else if ev.Amount != 0 {
			fmt.Fprintf(&b, " remaining=%.1fs", float64(ev.Amount)/1000)
		}
	case ev.Sub == "SPELL_CAST_START":
		fmt.Fprintf(&b, " cast=%.2fs", float64(ev.Amount)/1000)
	case ev.Sub == "SPELL_INTERRUPT":
		fmt.Fprintf(&b, " interrupted=%d %q", ev.Extra, spells[ev.Extra])
	case strings.HasSuffix(ev.Sub, "_MISSED"):
		fmt.Fprintf(&b, " reason=%d", ev.Extra)
	}
	if ev.Health != 0 {
		fmt.Fprintf(&b, " hp=%d", ev.Health)
	}
	return b.String()
}

func sortedKeys(m map[string]int) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// readSpellCSV adds a wago.tools DB2 export (ID,Name_lang) to names.
func readSpellCSV(path string, names map[int32]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return err
	}
	for _, r := range rows[1:] {
		if id, err := strconv.Atoi(r[0]); err == nil && len(r) > 1 {
			names[int32(id)] = r[1]
		}
	}
	return nil
}

// envOr is the environment variable k, or def when it is unset.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
