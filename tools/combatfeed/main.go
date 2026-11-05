// Command combatfeed turns a world session into a live combat feed: one JSON
// object per line on stdout, as it happens. It is what an overlay
// (tools/auraoverlay) or an addon bridge reads in place of the
// COMBAT_LOG_EVENT_UNFILTERED the 12.x client no longer gives addons.
//
//	# the stock client on live, through tools/capture-live's relay and tcpdump
//	go run ./tools/combatfeed -run captures/<stamp> -follow
//	go run ./tools/combatfeed -pcap world.pcap -secrets secrets.jsonl -follow     # the same, by file
//	# the stock client on the local fork: the server's own packet log, no decryption
//	go run ./tools/combatfeed -pkt <fork>/logs/World.pkt -follow -from-end
//	# a recorded fight played back in real time (try it with the overlay)
//	go run ./tools/combatfeed -pkt <fork>/logs/World.pkt -start 610 -replay 1
//
// Lines:
//
//	{"type":"player","guid":"..."}                        CMSG_PLAYER_LOGIN seen
//	{"type":"target","guid":"..."}                        CMSG_SET_SELECTION
//	{"type":"event","t":...,"sub":"SPELL_DAMAGE",...}     every combat event
//	{"type":"auras","unit":"player","auras":[...]}        the unit's auras, on every change
//	{"type":"cooldowns","gcd_ms":...,"cooldowns":[...]}   what is still recovering (absent = ready)
//	{"type":"power","powers":[{"type":17,"name":"fury","amount":42}]}
//
// The player and target come from the client's own messages, so the feed
// knows whose buffs to show and what you have selected.
package main

import (
	"bufio"
	"encoding/csv"
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
	"time"

	"github.com/Ltz-lab/wow-live/protocol/codec"
	"github.com/Ltz-lab/wow-live/protocol/combatlog"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/protocol/pktlog"
	"github.com/Ltz-lab/wow-live/protocol/spellstate"
	"github.com/Ltz-lab/wow-live/protocol/worldstream"
)

type msg struct {
	name string
	c2s  bool
	t    uint32 // ms
	body []byte
}

func main() {
	home, _ := os.UserHomeDir()
	pkt := flag.String("pkt", "", "the fork's packet log (logs/World.pkt)")
	pcap := flag.String("pcap", "", "a tcpdump pcapng of the world port (live, with -secrets)")
	secrets := flag.String("secrets", "", "bnetrelay's secrets.jsonl for -pcap")
	build := flag.Int("build", 0, "with -pcap/-run: 70755 Forever, 70783 retail, 0 to tell from the capture")
	run := flag.String("run", "", "a tools/capture-live run directory: its world.pcap and bnet/*/secrets.jsonl (waits for both)")
	follow := flag.Bool("follow", false, "keep reading as the file grows")
	fromEnd := flag.Bool("from-end", false, "with -follow: skip what is already in the file (the player and target are still picked up from it)")
	start := flag.Float64("start", 0, "skip messages before this many seconds into the log")
	replay := flag.Float64("replay", 0, "play a finished log back at this speed (1 = real time)")
	poll := flag.Duration("poll", 50*time.Millisecond, "with -follow: the longest wait for new data (on macOS the feed wakes the moment the file grows)")
	opcodesH := flag.String("opcodes-h", filepath.Join(envOr("WOW_TC", filepath.Join(home, "Dev/wow/server/wow-forever-tc")), "TrinityCore/src/server/game/Server/Protocol/Opcodes.h"), "the fork's Opcodes.h (names a World.pkt's numbers)")
	spellsCSV := flag.String("spells", "", "a wago.tools SpellName.csv (ID,Name_lang) for spell names, e.g. retail's")
	spellData := flag.String("spelldata", "", "a directory of wago.tools CSVs (SpellCooldowns, SpellCategories, SpellCategory, SpellName) for cooldowns, charges and the GCD")
	db := flag.String("db", envOr("WOW_FOREVER_DB", filepath.Join(home, "Dev/wow/host/data/forever.db")), "forever.db for spell names")
	flag.Parse()

	if *run != "" {
		*pcap = filepath.Join(*run, "world.pcap")
		for said := false; ; said = true {
			m, _ := filepath.Glob(filepath.Join(*run, "bnet", "*", "secrets.jsonl"))
			_, perr := os.Stat(*pcap)
			if len(m) > 0 && perr == nil {
				*secrets = m[len(m)-1]
				break
			}
			if !said {
				fmt.Fprintln(os.Stderr, "combatfeed: waiting for", *pcap, "and the relay's secrets.jsonl (log in now)")
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	var next func() ([]msg, error)
	switch {
	case *pkt != "":
		names, err := forkNames(*opcodesH)
		check(err)
		t, err := pktlog.OpenTail(*pkt)
		check(err)
		next = func() ([]msg, error) {
			ps, err := t.Next()
			var out []msg
			for _, p := range ps {
				c2s := p.Dir == pktlog.C2S
				out = append(out, msg{names[key(c2s, p.Opcode)], c2s, p.TicksMs, p.Body})
			}
			return out, err
		}
	case *pcap != "" && *secrets != "":
		t, err := worldstream.OpenTail(*pcap)
		check(err)
		d := worldstream.New(*build, *secrets)
		var t0 time.Time
		next = func() ([]msg, error) {
			segs, err := t.Next()
			var out []msg
			for _, s := range segs {
				for _, m := range d.Feed(s) {
					if t0.IsZero() {
						t0 = m.T
					}
					out = append(out, msg{m.Short(), m.Dir == opcodes.ClientToServer, uint32(m.T.Sub(t0).Milliseconds()), m.Body})
				}
			}
			return out, err
		}
	default:
		fmt.Fprintln(os.Stderr, "combatfeed: need -pkt, or -pcap with -secrets")
		os.Exit(2)
	}

	spells := spellNames(*db)
	var data *spellstate.Data
	if *spellData != "" {
		d, err := spellstate.LoadWago(*spellData)
		check(err)
		data = d
		for id, n := range d.Names {
			spells[id] = n
		}
	}
	if *spellsCSV != "" {
		check(readSpellCSV(*spellsCSV, spells))
	}
	followed := *pkt
	if followed == "" {
		followed = *pcap
	}
	var wake *waker
	if *follow {
		wake = newWaker(followed)
	}
	f := &feed{log: combatlog.New(), spells: spells, out: json.NewEncoder(os.Stdout), auraSig: map[string]string{}, cd: spellstate.New(data)}
	live := false // false while catching up on what was in the file
	var last uint32
	wall := time.Now()
	for first := true; ; first = false {
		ms, err := next()
		check(err)
		if first && *follow && *fromEnd {
			for _, m := range ms {
				f.quiet(m) // learn the player, target and auras; emit nothing
			}
			ms = nil
		}
		for _, m := range ms {
			if float64(m.t)/1000 < *start {
				f.quiet(m)
				continue
			}
			if *replay > 0 && !live {
				if last != 0 && m.t > last {
					target := time.Duration(float64(m.t-last)/(*replay)) * time.Millisecond
					if gap := target - time.Since(wall); gap > 0 {
						time.Sleep(min(gap, 5*time.Second))
					}
				}
				last, wall = m.t, time.Now()
			}
			if !f.primed {
				f.prime(m.t)
			}
			f.handle(m)
		}
		if !*follow {
			if len(ms) == 0 {
				return
			}
			continue
		}
		live = true
		if len(ms) == 0 {
			wake.wait(*poll) // returns as soon as the capture grows
		}
	}
}

type feed struct {
	log     *combatlog.Log
	spells  map[int32]string
	out     *json.Encoder
	player  codec.GUID
	target  codec.GUID
	auraSig map[string]string
	primed  bool
	cd      *spellstate.Tracker
	cdSig   string
	pwSig   string
}

// prime announces what was picked up quietly: the player, target and auras.
func (f *feed) prime(t uint32) {
	f.primed = true
	if !f.player.IsEmpty() {
		f.emit(map[string]any{"type": "player", "guid": f.player.String(), "t": t})
	}
	f.emit(map[string]any{"type": "target", "guid": f.target.String(), "unit": unitName(f.target), "t": t})
	f.auras(t, true)
}

// quiet takes a message in without emitting anything.
func (f *feed) quiet(m msg) {
	if !f.track(m) && !m.c2s {
		f.cd.Message(m.name, m.t, m.body)
		evs, _ := f.log.Feed(m.name, m.t, m.body)
		for _, e := range evs {
			f.cd.Event(e)
		}
	}
}

// track follows the player and target from the client's own messages.
func (f *feed) track(m msg) bool {
	if !m.c2s {
		return false
	}
	switch m.name {
	case "CMSG_PLAYER_LOGIN":
		if g, err := codec.NewReader(m.body).GUID(); err == nil {
			f.player, f.target = g, codec.GUID{}
			f.cd.Player = g
			return true
		}
	case "CMSG_SET_SELECTION":
		if g, err := codec.NewReader(m.body).GUID(); err == nil && g != f.target {
			f.target = g
			return true
		}
	}
	return false
}

func (f *feed) handle(m msg) {
	if m.c2s {
		if f.track(m) {
			if m.name == "CMSG_PLAYER_LOGIN" {
				f.emit(map[string]any{"type": "player", "guid": f.player.String(), "t": m.t})
			}
			f.emit(map[string]any{"type": "target", "guid": f.target.String(), "unit": unitName(f.target), "t": m.t})
			f.auras(m.t, true)
		}
		return
	}
	changed := f.cd.Message(m.name, m.t, m.body)
	evs, err := f.log.Feed(m.name, m.t, m.body)
	if err != nil {
		if err != combatlog.ErrUnknown {
			fmt.Fprintln(os.Stderr, err)
		}
		if changed {
			f.cooldowns(m.t)
		}
		return
	}
	for _, ev := range evs {
		if f.cd.Event(ev) {
			changed = true
		}
	}
	for _, ev := range evs {
		o := map[string]any{
			"type": "event", "t": ev.TimeMs, "sub": ev.Sub,
			"src": unitName(ev.Source), "dst": unitName(ev.Dest),
			"mine": !f.player.IsEmpty() && ev.Source == f.player,
			"me":   !f.player.IsEmpty() && ev.Dest == f.player,
		}
		if ev.SpellID != 0 {
			o["spell"], o["name"] = ev.SpellID, f.spells[ev.SpellID]
		}
		for k, v := range map[string]int64{"amount": ev.Amount, "over": int64(max(ev.Over, 0)), "absorbed": int64(ev.Absorbed), "school": int64(ev.School), "extra": int64(ev.Extra), "hp": ev.Health} {
			if v != 0 {
				o[k] = v
			}
		}
		if ev.Crit {
			o["crit"] = true
		}
		f.emit(o)
	}
	f.auras(m.t, false)
	if changed {
		f.cooldowns(m.t)
		f.power(m.t)
	}
}

// cooldowns emits the player's GCD and everything still recovering, when it
// changed. Anything not listed is ready.
func (f *feed) cooldowns(t uint32) {
	cds := f.cd.Cooldowns(t)
	for i := range cds {
		if cds[i].Name == "" && cds[i].Spell != 0 {
			cds[i].Name = f.spells[cds[i].Spell]
		}
	}
	gcd := f.cd.GCD(t)
	b, _ := json.Marshal(cds)
	sig := fmt.Sprintf("%d|%s", gcd/50, b) // the same state 50 ms later is not news
	if sig == f.cdSig {
		return
	}
	f.cdSig = sig
	if cds == nil {
		cds = []spellstate.Cooldown{}
	}
	f.emit(map[string]any{"type": "cooldowns", "t": t, "gcd_ms": gcd, "cooldowns": cds})
}

// power emits the player's known power amounts when they changed.
func (f *feed) power(t uint32) {
	pw, ok := f.cd.Power()
	if !ok {
		return
	}
	var list []map[string]any
	var keys []int
	for k := range pw {
		keys = append(keys, int(k))
	}
	sort.Ints(keys)
	for _, k := range keys {
		list = append(list, map[string]any{"type": k, "name": spellstate.PowerNames[int8(k)], "amount": pw[int8(k)]})
	}
	b, _ := json.Marshal(list)
	if string(b) == f.pwSig {
		return
	}
	f.pwSig = string(b)
	f.emit(map[string]any{"type": "power", "t": t, "powers": list})
}

// auras emits the player's and the target's auras when they changed.
func (f *feed) auras(t uint32, force bool) {
	for _, u := range []struct {
		name string
		g    codec.GUID
	}{{"player", f.player}, {"target", f.target}} {
		list := []map[string]any{}
		var sig strings.Builder
		if !u.g.IsEmpty() {
			for _, a := range f.log.Auras(u.g) {
				rem := int64(0)
				if a.ExpiresMs > t {
					rem = int64(a.ExpiresMs - t)
				}
				list = append(list, map[string]any{
					"spell": a.SpellID, "name": f.spells[a.SpellID], "stacks": a.Stacks,
					"duration_ms": a.DurationMs, "remaining_ms": rem,
					"mine": a.Caster == f.player || a.Caster.IsEmpty() && u.g == f.player,
				})
				fmt.Fprintf(&sig, "%d:%d:%d:%d;", a.SpellID, a.Stacks, a.ExpiresMs, a.Slot)
			}
		}
		s := u.g.String() + "|" + sig.String()
		if !force && f.auraSig[u.name] == s {
			continue
		}
		f.auraSig[u.name] = s
		f.emit(map[string]any{"type": "auras", "t": t, "unit": u.name, "guid": u.g.String(), "auras": list})
	}
}

func (f *feed) emit(o map[string]any) {
	_ = f.out.Encode(o)
}

func unitName(g codec.GUID) string {
	if g.IsEmpty() {
		return ""
	}
	switch g.Hi >> 58 {
	case 2:
		return fmt.Sprintf("Player-%d", g.Lo&0xFFFFFFFFFF)
	case 8, 9, 12:
		return fmt.Sprintf("Creature-%d", (g.Hi>>6)&0x7FFFFF)
	}
	return g.String()
}

func key(c2s bool, op uint32) uint64 {
	if c2s {
		return 1<<32 | uint64(op)
	}
	return uint64(op)
}

var opLine = regexp.MustCompile(`\b([CS])(MSG_\w+)\s*=\s*(0x[0-9A-Fa-f]+)`)

func forkNames(path string) (map[uint64]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[uint64]string{}
	for _, m := range opLine.FindAllStringSubmatch(string(b), -1) {
		v, _ := strconv.ParseUint(m[3], 0, 32)
		out[key(m[1] == "C", uint32(v))] = m[1] + m[2]
	}
	return out, nil
}

func spellNames(db string) map[int32]string {
	out := map[int32]string{}
	b, err := exec.Command("sqlite3", "-separator", "\t", db, "select id, name from spells").Output()
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		id, name, ok := strings.Cut(sc.Text(), "\t")
		if v, err := strconv.Atoi(id); ok && err == nil {
			out[int32(v)] = name
		}
	}
	return out
}

// readSpellCSV adds a wago.tools DB2 export (ID,Name_lang) to names (from
// tools/combatlog on claude/cool-swanson-47f3cf, 08f31ca).
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

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "combatfeed:", err)
		os.Exit(1)
	}
}

// envOr is the environment variable k, or def when it is unset.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
