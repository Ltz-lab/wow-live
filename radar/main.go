// Command radar is a live 3D radar of the world around the player, drawn
// from the positions the server sends: players, creatures and game objects,
// where moving mobs are heading, and ourselves at the centre.
//
//	go run . -run captures/<stamp> -follow   # live, with capture-live
//	go run . -jsonl conn-1.jsonl,conn-2.jsonl -speed 1                   # replay worlddecrypt output
//	go run . -pkt <fork>/logs/World.pkt -follow             # the local fork's packet log
//	go run . -run <capture-live --game forever run> -follow -build 70755   # Forever live
//
// Then open http://localhost:8791. Nothing touches the game process: the
// input is the world connection's own traffic (tcpdump + the relay's keys).
//
// Sources: SMSG_UPDATE_OBJECT creates (movement block: every object's
// position), SMSG_ON_MONSTER_MOVE (splines: where a unit is going and how
// long it takes), SMSG_MOVE_UPDATE (other players), the client's own
// movement messages (us), and the client's creature/game-object name caches
// plus the query responses that arrive while it runs.
//
// On Forever (70755) the update fields decode too: levels, health, NPC flags
// and our quest log (QuestLog.QuestID / ObjectiveProgress), and
// SMSG_QUEST_GIVER_STATUS(_MULTIPLE) gives each NPC's quest icon. The
// overlay's levelling mode joins those with a Forever names db (WOW_FOREVER_DB).
package main

import (
	"bufio"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ltz-lab/wow-live/protocol/codec"
	"github.com/Ltz-lab/wow-live/protocol/opcodes"
	"github.com/Ltz-lab/wow-live/protocol/pktlog"
	"github.com/Ltz-lab/wow-live/protocol/updateobject"
	"github.com/Ltz-lab/wow-live/protocol/updateobject/apply"
	"github.com/Ltz-lab/wow-live/protocol/worldstream"
	"github.com/Ltz-lab/wow-live/state"
)

//go:embed web
var web embed.FS

type msg struct {
	T    time.Time
	C2S  bool
	Op   uint32
	Name string // bare name, SMSG_UPDATE_OBJECT, or ""
	Body []byte
}

type ent struct {
	GUID  string  `json:"g"`
	Kind  string  `json:"k"`
	Entry uint32  `json:"e,omitempty"`
	Name  string  `json:"n,omitempty"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Z     float32 `json:"z"`
	O     float32 `json:"o"`
	// The active spline, if any: from Pos at T0 to Dest at T1.
	DX, DY, DZ float32   `json:"-"`
	T0, T1     time.Time `json:"-"`
	Moving     bool      `json:"m,omitempty"`
	ToX        float32   `json:"tx,omitempty"`
	ToY        float32   `json:"ty,omitempty"`
	ToZ        float32   `json:"tz,omitempty"`
	ETA        float64   `json:"eta,omitempty"` // seconds to the destination
	// From the update fields (Forever: they decode; retail: not yet).
	Level   uint8     `json:"lvl,omitempty"`
	HP      int64     `json:"hp,omitempty"`
	MaxHP   int64     `json:"mhp,omitempty"`
	Dead    bool      `json:"dead,omitempty"`
	NPC     uint32    `json:"npcf,omitempty"`
	QGiver  string    `json:"qs,omitempty"` // quest giver status: turnin, available, trivial, future
	Race    uint8     `json:"race,omitempty"`
	Class   uint8     `json:"cls,omitempty"`
	Hostile bool      `json:"hostile,omitempty"` // a player of the other faction
	spot    *spot     // a herb or ore node: its spot
	Seen    time.Time `json:"-"`
	// Between a player's movement messages: the velocity measured from the
	// last two (yards per second), while its flags say it is moving.
	VX, VY, VZ float32 `json:"-"`
	MT         uint32  `json:"-"` // the mover's clock at the last message (ms)
	Coast      bool    `json:"-"`
}

// moving flags: forward, backward, strafe left/right, falling, ascending, descending.
const movingFlags = 0x0F | 0x800 | 0x200000 | 0x400000

// moveTo applies a MovementInfo and measures the velocity since the last one.
func moveTo(e *ent, mi *updateobject.MovementInfo, t time.Time) {
	p := mi.Position
	if e.MT != 0 && mi.MoveTime > e.MT && mi.MoveTime-e.MT < 3000 {
		dt := float32(mi.MoveTime-e.MT) / 1000
		vx, vy, vz := (p.X-e.X)/dt, (p.Y-e.Y)/dt, (p.Z-e.Z)/dt
		if math.Hypot(float64(vx), float64(vy)) < 120 { // not a teleport
			e.VX, e.VY, e.VZ = vx, vy, vz
		}
	}
	e.Coast = mi.Flags&movingFlags != 0
	if !e.Coast {
		e.VX, e.VY, e.VZ = 0, 0, 0
	}
	e.X, e.Y, e.Z, e.O = p.X, p.Y, p.Z, p.O
	e.MT = mi.MoveTime
	e.Moving = false
	e.Seen = t
}

// coast moves e on by its velocity for the time since its last message (at most 1.5 s).
func coast(e *ent, now time.Time) {
	if !e.Coast {
		return
	}
	dt := float32(math.Min(now.Sub(e.Seen).Seconds(), 1.5))
	if dt <= 0 {
		return
	}
	e.X += e.VX * dt
	e.Y += e.VY * dt
	e.Z += e.VZ * dt
}

type world struct {
	mu       sync.Mutex
	ents     map[codec.GUID]*ent
	self     codec.GUID
	mapID    uint16
	clock    time.Time // stream time of the last message
	wall     time.Time // wall time it arrived
	speed    float64
	names    map[uint32]string // creature entry -> name
	goNames  map[uint32]string
	players  map[codec.GUID]string
	stats    map[string]int
	lastMove time.Time
	// The full update-field model (Forever's fields decode) and our quest log.
	st      *state.State
	quests  map[int]*quest // quest log slot -> quest
	qstatus map[codec.GUID]uint64
	nodes   *nodes
	vis     float64 // yards inside which the server always shows a node that is up
}

type quest struct {
	ID       int32     `json:"id"`
	Progress [24]int32 `json:"-"`
	P        []int32   `json:"p"`
}

func newWorld(speed float64) *world {
	return &world{ents: map[codec.GUID]*ent{}, names: map[uint32]string{}, goNames: map[uint32]string{},
		players: map[codec.GUID]string{}, stats: map[string]int{}, speed: speed,
		st: state.New(), quests: map[int]*quest{}, qstatus: map[codec.GUID]uint64{},
		nodes: newNodes(), vis: 90}
}

// now is the stream's clock: the last message's time plus the wall time
// since (scaled in replay), so splines keep moving between messages.
func (w *world) now() time.Time {
	if w.clock.IsZero() {
		return time.Now()
	}
	d := time.Since(w.wall)
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return w.clock.Add(time.Duration(float64(d) * w.speed))
}

func guidType(g codec.GUID) uint64  { return g.Hi >> 58 }
func guidEntry(g codec.GUID) uint32 { return uint32((g.Hi >> 6) & 0x7FFFFF) }

var kinds = map[updateobject.ObjectType]string{
	updateobject.TypeUnit: "unit", updateobject.TypePlayer: "player", updateobject.TypeActivePlayer: "player",
	updateobject.TypeGameObject: "gameobject", updateobject.TypeCorpse: "corpse",
	updateobject.TypeAreaTrigger: "areatrigger", updateobject.TypeDynamicObject: "dynamic",
}

func (w *world) get(g codec.GUID, kind string) *ent {
	e := w.ents[g]
	if e == nil {
		e = &ent{GUID: fmt.Sprintf("%016x%016x", g.Hi, g.Lo), Kind: kind}
		if kind == "unit" || kind == "gameobject" {
			e.Entry = guidEntry(g)
		}
		w.ents[g] = e
	}
	return e
}

func (w *world) handle(m msg) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clock, w.wall = m.T, time.Now()
	if m.C2S {
		w.c2s(m)
		return
	}
	switch m.Name {
	case "SMSG_LOGIN_VERIFY_WORLD", "SMSG_NEW_WORLD", "SMSG_LOGOUT_COMPLETE":
		// The client drops every object here and the server sends them all again with new creates.
		// Without this a relog (or a World.pkt holding several sessions) leaves a stale copy of each.
		w.ents = map[codec.GUID]*ent{}
		w.qstatus = map[codec.GUID]uint64{}
		if m.Name == "SMSG_LOGOUT_COMPLETE" {
			w.self, w.quests = codec.GUID{}, map[int]*quest{}
		}
		w.stats["world reset"]++
	case "SMSG_UPDATE_OBJECT":
		u, err := updateobject.Decode(m.Body)
		if err != nil {
			w.stats["update_object failed"]++
			return
		}
		w.stats["update_object"]++
		w.mapID = u.MapID
		if r := apply.Apply(w.st, u); len(r.Skipped) == 0 {
			w.stats["fields applied"]++
		}
		for _, b := range u.Blocks {
			if b.Create != nil && b.Create.Movement != nil && b.Create.Movement.ThisIsYou {
				w.self = b.GUID
			}
			if b.GUID != w.self || w.self == (codec.GUID{}) {
				continue
			}
			var fs []updateobject.Field
			switch {
			case b.Create != nil:
				if of, err := b.Create.DecodeFields(); err == nil && of.Player != nil {
					fs = of.Player.Fields
				}
			case b.Values != nil:
				if d, err := updateobject.DecodeValues(updateobject.TypeActivePlayer, b.Values); d != nil && (err == nil || d.Player != nil) {
					fs = d.Player
				}
			}
			w.questFields(fs)
		}
		if rm := u.Removed; rm != nil {
			for i, l := range [][]codec.GUID{rm.Destroyed, rm.OutOfRange} {
				for _, g := range l {
					if g == w.self {
						continue
					}
					if e := w.ents[g]; e != nil && e.spot != nil {
						// out of range while we stand well inside view range: it went
						gathered := i == 0 || w.distSelf(e) < w.vis*0.6
						w.nodes.down(e.spot, e.GUID, gathered, m.T)
					}
					delete(w.ents, g)
				}
			}
		}
		for _, b := range u.Blocks {
			c := b.Create
			if c == nil {
				continue
			}
			kind, ok := kinds[c.ObjectType]
			if !ok || c.Movement == nil {
				continue
			}
			var p *updateobject.Pos4
			switch mv := c.Movement; {
			case mv.Movement != nil:
				p = &mv.Movement.Info.Position
			case mv.Stationary != nil:
				p = mv.Stationary
			}
			if p == nil {
				continue
			}
			e := w.get(b.GUID, kind)
			e.X, e.Y, e.Z, e.O = p.X, p.Y, p.Z, p.O
			e.Moving = false
			e.Seen = m.T
			if c.Movement.ThisIsYou {
				w.self = b.GUID
			}
			if kind == "gameobject" {
				if name := w.goName(e.Entry); nodeKind(name) != "" {
					e.spot = w.nodes.up(e.GUID, u.MapID, name, e.X, e.Y, e.Z, m.T)
				}
			}
		}
	case "SMSG_ON_MONSTER_MOVE":
		mm, err := updateobject.DecodeMonsterMove(m.Body)
		if err != nil {
			w.stats["monster_move failed"]++
			return
		}
		w.stats["monster_move"]++
		e := w.get(mm.Mover, "unit")
		d := mm.Dest()
		e.X, e.Y, e.Z = mm.Start.X, mm.Start.Y, mm.Start.Z
		e.DX, e.DY, e.DZ = d.X, d.Y, d.Z
		left := time.Duration(int64(mm.MoveTime)-int64(mm.Elapsed)) * time.Millisecond
		e.T0, e.T1 = m.T, m.T.Add(left)
		e.Moving = len(mm.Points) > 0 && left > 0
		if e.Moving && (d.X != e.X || d.Y != e.Y) {
			e.O = float32(math.Atan2(float64(d.Y-e.Y), float64(d.X-e.X)))
		}
		e.Seen = m.T
	case "SMSG_MOVE_UPDATE", "SMSG_MOVE_UPDATE_RUN_SPEED", "SMSG_MOVE_UPDATE_FLIGHT_SPEED",
		"SMSG_MOVE_UPDATE_SWIM_SPEED", "SMSG_MOVE_UPDATE_COLLISION_HEIGHT", "SMSG_MOVE_UPDATE_KNOCK_BACK":
		// all start with the mover's MovementInfo (checked: every one decodes, 2026-11-05)
		mi, err := updateobject.DecodeMoveUpdate(m.Body)
		if err != nil || !sane(mi.Position) {
			w.stats["move_update failed"]++
			return
		}
		w.stats["move_update"]++
		kind := "unit"
		if guidType(mi.Mover) == 2 {
			kind = "player"
		}
		moveTo(w.get(mi.Mover, kind), mi, m.T)
	case "SMSG_QUEST_GIVER_STATUS":
		r := codec.NewReader(m.Body)
		if g, err := r.GUID(); err == nil {
			if v, err := r.Uint64(); err == nil {
				w.qstatus[g] = v
			}
		}
	case "SMSG_QUEST_GIVER_STATUS_MULTIPLE":
		r := codec.NewReader(m.Body)
		n, _ := r.Uint32()
		for i := 0; i < int(n) && i < 4096; i++ {
			g, err := r.GUID()
			if err != nil {
				break
			}
			v, err := r.Uint64()
			if err != nil {
				break
			}
			w.qstatus[g] = v
		}
	case "SMSG_QUERY_CREATURE_RESPONSE":
		if id, n := cacheName(m.Body); id != 0 && n != "" {
			w.names[id] = n
		}
	case "SMSG_QUERY_GAME_OBJECT_RESPONSE":
		if id, n := cacheName(m.Body); id != 0 && n != "" {
			w.goNames[id] = n
		}
	case "SMSG_QUERY_PLAYER_NAMES_RESPONSE":
		for g, n := range playerNames(m.Body) {
			w.players[g] = n
		}
	}
}

func (w *world) goName(entry uint32) string {
	if n := w.goNames[entry]; n != "" {
		return n
	}
	return w.nodes.entries[entry]
}

func (w *world) distSelf(e *ent) float64 {
	me := w.ents[w.self]
	if me == nil {
		return math.Inf(1)
	}
	return math.Hypot(float64(e.X-me.X), float64(e.Y-me.Y))
}

// c2s: the client's own movement messages carry our MovementInfo. On
// 70783 they are the 0x41 group (CMSG_MOVE_*, unnamed in our table).
func (w *world) c2s(m msg) {
	if m.Op>>16 != 0x41 && !strings.HasPrefix(m.Name, "CMSG_MOVE") {
		return
	}
	mi, err := updateobject.DecodeMoveUpdate(m.Body)
	if err != nil || guidType(mi.Mover) != 2 || !sane(mi.Position) {
		return
	}
	if w.self != mi.Mover {
		w.self = mi.Mover
	}
	w.stats["self_move"]++
	moveTo(w.get(mi.Mover, "player"), mi, m.T)
	w.lastMove = m.T
}

func sane(p updateobject.Pos4) bool {
	return math.Abs(float64(p.X)) < 20000 && math.Abs(float64(p.Y)) < 20000 && math.Abs(float64(p.Z)) < 10000 && !math.IsNaN(float64(p.X))
}

var questField = regexp.MustCompile(`^QuestLog\.(QuestID|ObjectiveProgress)\[(\d+)\](?:\[(\d+)\])?$`)

// questFields applies our PlayerData quest log fields (QuestLog.QuestID[i],
// QuestLog.ObjectiveProgress[i][j]).
func (w *world) questFields(fs []updateobject.Field) {
	for _, f := range fs {
		m := questField.FindStringSubmatch(f.Name)
		if m == nil {
			continue
		}
		i, _ := strconv.Atoi(m[2])
		q := w.quests[i]
		if q == nil {
			q = &quest{}
			w.quests[i] = q
		}
		v := toI32(f.Value)
		if m[1] == "QuestID" {
			if v != q.ID {
				q.Progress = [24]int32{}
			}
			q.ID = v
		} else if j, _ := strconv.Atoi(m[3]); j < 24 {
			q.Progress[j] = v
		}
	}
}

func toI32(v any) int32 {
	switch x := v.(type) {
	case int32:
		return x
	case uint32:
		return int32(x)
	case int16:
		return int32(x)
	case uint16:
		return int32(x)
	case int8:
		return int32(x)
	case uint8:
		return int32(x)
	case int64:
		return int32(x)
	}
	return 0
}

// giverStatus names the quest-giver icon the client would draw (QuestGiverStatus bits).
func giverStatus(v uint64) string {
	switch {
	case v&0x0000000FE000 != 0 || v&0xFFFE00000000 != 0: // Reward..ImportantReward, *RewardComplete*
		return "turnin"
	case v&(0x400000|0x800000|0x1000000|0x2000000|0x10000000|0x40000000|0x100000000) != 0:
		return "available"
	case v&0x1FE0 != 0:
		return "trivial"
	case v&0x1E != 0:
		return "future"
	}
	return ""
}

type snapshot struct {
	Map     uint16         `json:"map"`
	Self    *ent           `json:"self"`
	Objects []ent          `json:"objects"`
	Clock   string         `json:"clock"`
	Stats   map[string]int `json:"stats"`
	Level   uint8          `json:"level,omitempty"`
	Race    uint8          `json:"race,omitempty"`
	Quests  []quest        `json:"quests,omitempty"`
	Nodes   []nodeOut      `json:"nodes,omitempty"`
	Threats []threat       `json:"threats,omitempty"`
}

// fill copies what the field model knows about e's object.
func (w *world) fill(g codec.GUID, c *ent) {
	if o, ok := w.st.Object(state.GUID{Hi: g.Hi, Lo: g.Lo}); ok {
		c.Level, c.HP, c.MaxHP, c.NPC = o.Level, o.Health, o.MaxHealth, o.NpcFlags
		c.Dead = o.MaxHealth > 0 && o.Health == 0
		c.Race, c.Class = o.Race, o.Class
		if c.Entry == 0 {
			c.Entry = o.Entry
		}
	}
	if v, ok := w.qstatus[g]; ok {
		c.QGiver = giverStatus(v)
	}
}

func (w *world) snapshot(radius, nodeRadius float64) snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	s := snapshot{Map: w.mapID, Clock: now.Format("15:04:05.0"), Stats: map[string]int{}}
	for k, v := range w.stats {
		s.Stats[k] = v
	}
	var sx, sy float32
	if me := w.ents[w.self]; me != nil {
		c := *me
		coast(&c, now)
		w.fill(w.self, &c)
		c.Kind = "self"
		if o, ok := w.st.Object(state.GUID{Hi: w.self.Hi, Lo: w.self.Lo}); ok {
			s.Level, s.Race = o.Level, o.Race
		}
		for i := 0; i < 175; i++ {
			if q := w.quests[i]; q != nil && q.ID > 0 {
				qq := *q
				n := 24
				for n > 0 && q.Progress[n-1] == 0 {
					n--
				}
				qq.P = append([]int32{}, q.Progress[:n]...)
				s.Quests = append(s.Quests, qq)
			}
		}
		c.Name = w.players[w.self]
		s.Self = &c
		sx, sy = c.X, c.Y
	}
	for g, e := range w.ents {
		if g == w.self {
			continue
		}
		c := *e
		coast(&c, now)
		w.fill(g, &c)
		if c.Moving {
			c.ToX, c.ToY, c.ToZ = c.DX, c.DY, c.DZ
			span := c.T1.Sub(c.T0).Seconds()
			f := 1.0
			if span > 0 {
				f = now.Sub(c.T0).Seconds() / span
			}
			if f >= 1 {
				c.X, c.Y, c.Z, c.Moving = c.DX, c.DY, c.DZ, false
				e.X, e.Y, e.Z, e.Moving = c.DX, c.DY, c.DZ, false // arrived
			} else {
				if f < 0 {
					f = 0
				}
				c.X += float32(f) * (c.DX - c.X)
				c.Y += float32(f) * (c.DY - c.Y)
				c.Z += float32(f) * (c.DZ - c.Z)
				c.ETA = c.T1.Sub(now).Seconds()
			}
		}
		if s.Self != nil && math.Hypot(float64(c.X-sx), float64(c.Y-sy)) > radius {
			continue
		}
		switch c.Kind {
		case "unit":
			c.Name = w.names[c.Entry]
		case "gameobject":
			c.Name = w.goName(c.Entry)
		case "player":
			c.Name = w.players[g]
		}
		s.Objects = append(s.Objects, c)
	}
	sort.Slice(s.Objects, func(i, j int) bool { return s.Objects[i].GUID < s.Objects[j].GUID })
	if s.Self != nil {
		s.Threats = threats(s.Self, s.Objects)
		s.Nodes = w.nodes.view(w.mapID, sx, sy, nodeRadius, w.vis, now)
	}
	return s
}

// cacheName pulls (entry, name) out of a creature or game-object record:
// the WDB cache's record body or a query response (u32 entry, then the
// stats with the name as the first printable run).
func cacheName(b []byte) (uint32, string) {
	if len(b) < 8 {
		return 0, ""
	}
	return binary.LittleEndian.Uint32(b), firstText(b[4:])
}

func firstText(b []byte) string {
	start := -1
	for i := 0; i <= len(b); i++ {
		ok := i < len(b) && b[i] >= 0x20 && b[i] < 0x7f
		if ok && start < 0 {
			start = i
		}
		if !ok && start >= 0 {
			if i-start >= 3 && strings.ContainsAny(string(b[start:i]), "abcdefghijklmnopqrstuvwxyz") {
				return string(b[start:i])
			}
			start = -1
		}
	}
	return ""
}

// playerNames: each entry of the response names a player GUID; take the
// player GUIDs in order and the printable runs after each.
func playerNames(b []byte) map[codec.GUID]string {
	out := map[codec.GUID]string{}
	for i := 0; i+18 < len(b); i++ {
		r := codec.NewReader(b[i:])
		g, err := r.GUID()
		if err != nil || guidType(g) != 2 || g.Lo == 0 {
			continue
		}
		if n := firstText(b[i+2 : min(len(b), i+80)]); n != "" && !strings.Contains(n, " ") && len(n) <= 12 {
			out[g] = n
		}
	}
	return out
}

// loadWDB reads a client cache file (creaturecache.wdb / gameobjectcache.wdb):
// a 24-byte header, then records of u32 entry, u32 size, size bytes.
func loadWDB(path string, into map[uint32]string) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 24 {
		return 0
	}
	n := 0
	for p := 24; p+8 <= len(b); {
		id := binary.LittleEndian.Uint32(b[p:])
		sz := int(binary.LittleEndian.Uint32(b[p+4:]))
		if id == 0 && sz == 0 || p+8+sz > len(b) {
			break
		}
		if name := firstText(b[p+8 : p+8+sz]); name != "" {
			into[id] = name
			n++
		}
		p += 8 + sz
	}
	return n
}

func main() {
	run := flag.String("run", "", "a tools/capture-live run directory (its world.pcap and bnet/*/secrets.jsonl)")
	pcap := flag.String("pcap", "", "the world-port capture")
	secrets := flag.String("secrets", "", "bnetrelay's secrets.jsonl")
	follow := flag.Bool("follow", false, "keep reading as the capture grows")
	jsonl := flag.String("jsonl", "", "replay worlddecrypt output instead (comma-separated conn-*.jsonl)")
	pkt := flag.String("pkt", "", "a TrinityCore World.pkt (the local fork), with -follow to tail it")
	opcodesH := flag.String("opcodes-h", filepath.Join(envOr("WOW_TC", filepath.Join(os.Getenv("HOME"), "Dev/wow-forever-tc")), "TrinityCore/src/server/game/Server/Protocol/Opcodes.h"), "the fork's Opcodes.h (names a World.pkt's numbers)")
	speed := flag.Float64("speed", 1, "replay speed (0: as fast as possible)")
	build := flag.Int("build", 70783, "client build (opcode names)")
	addr := flag.String("addr", "127.0.0.1:8791", "where to serve the radar")
	radius := flag.Float64("radius", 250, "yards around us to send to the page")
	nodesPath := flag.String("nodes", "nodes.json", "herb and ore spawn points (tools/nodes-export.py); \"\" for none")
	nodeRadius := flag.Float64("node-radius", 400, "yards around us to list node spots")
	vis := flag.Float64("vis", 90, "yards inside which the server shows every node that is up")
	seen := flag.String("seen", filepath.Join(os.Getenv("HOME"), ".config/wow-radar/seen.json"), "where gathered/empty times and respawns are kept between sessions (live only); \"\" for nowhere")
	fdb := flag.String("db", "", "forever.db for creature, object and quest names (default with -pkt or a Forever -build: $WOW_FOREVER_DB, else ~/Dev/wow/host/data/forever.db; \"-\" for none)")
	wdb := flag.String("wdb", filepath.Join(envOr("WOW_RETAIL_DIR", "/Applications/World of Warcraft/_retail_"), "Cache/WDB/enUS"), "the client's name caches")
	flag.Parse()

	sp := *speed
	if sp <= 0 {
		sp = 1
	}
	w := newWorld(sp)
	w.vis = *vis
	if *nodesPath != "" {
		if err := w.nodes.load(*nodesPath); err != nil {
			log.Printf("nodes: %v (run tools/nodes-export.py)", err)
		} else {
			log.Printf("nodes: %d spots, %d node entries from %s", len(w.nodes.spots), len(w.nodes.entries), *nodesPath)
		}
	}
	if *pkt != "" || *jsonl != "" || !*follow {
		*seen = "" // replays have their own clocks: keep them out of the live history
	}
	if *seen != "" {
		os.MkdirAll(filepath.Dir(*seen), 0o755)
		w.nodes.loadSeen(*seen)
		go func() {
			for range time.Tick(30 * time.Second) {
				w.mu.Lock()
				w.nodes.save(*seen)
				w.mu.Unlock()
			}
		}()
	}
	log.Printf("names: %d creatures, %d game objects from %s",
		loadWDB(filepath.Join(*wdb, "creaturecache.wdb"), w.names),
		loadWDB(filepath.Join(*wdb, "gameobjectcache.wdb"), w.goNames), *wdb)

	if *fdb == "" && (*pkt != "" || *build != 70783) {
		*fdb = envOr("WOW_FOREVER_DB", filepath.Join(os.Getenv("HOME"), "Dev/wow/host/data/forever.db"))
	}
	if *fdb != "" && *fdb != "-" {
		if n, o, q, err := loadForeverDB(*fdb, w); err != nil {
			log.Printf("forever.db: %v", err)
		} else {
			log.Printf("forever.db: %d creatures, %d game objects, %d quests from %s", n, o, q, *fdb)
		}
	}
	sub, _ := fs.Sub(web, "web")
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/state", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(rw).Encode(w.snapshot(*radius, *nodeRadius))
	})
	http.HandleFunc("/mcp", mcpHandler(w, *nodeRadius))
	go func() { log.Fatal(http.ListenAndServe(*addr, nil)) }()
	log.Printf("radar on http://%s", *addr)

	switch {
	case *pkt != "":
		readPKT(w, *pkt, *opcodesH, *follow, *speed)
	case *jsonl != "":
		replay(w, strings.Split(*jsonl, ","), *speed)
		log.Printf("replay done: %v", w.stats)
		select {}
	default:
		if *run != "" {
			*pcap = filepath.Join(*run, "world.pcap")
			for said := false; ; said = true {
				m, _ := filepath.Glob(filepath.Join(*run, "bnet", "*", "secrets.jsonl"))
				if _, err := os.Stat(*pcap); err == nil && len(m) > 0 {
					*secrets = m[len(m)-1]
					break
				}
				if !said {
					log.Printf("waiting for %s and the relay's secrets (log in now)", *pcap)
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
		if *pcap == "" || *secrets == "" {
			log.Fatal("need -run, -pcap with -secrets, or -jsonl")
		}
		live(w, *pcap, *secrets, *build, *follow)
	}
}

func live(w *world, pcap, secrets string, build int, follow bool) {
	t, err := worldstream.OpenTail(pcap)
	if err != nil {
		log.Fatal(err)
	}
	d := worldstream.New(build, secrets)
	last := time.Now()
	for {
		segs, err := t.Next()
		if err != nil {
			log.Fatal(err)
		}
		for _, s := range segs {
			for _, m := range d.Feed(s) {
				w.handle(msg{m.T, m.Dir == opcodes.ClientToServer, m.Opcode, m.Short(), m.Body})
			}
		}
		if len(segs) == 0 {
			if !follow {
				log.Printf("capture read: %v", w.stats)
				select {}
			}
			time.Sleep(30 * time.Millisecond)
		}
		if time.Since(last) > 10*time.Second {
			last = time.Now()
			w.mu.Lock()
			log.Printf("%d objects, %v", len(w.ents), w.stats)
			w.mu.Unlock()
		}
	}
}

var opLine = regexp.MustCompile(`\b([CS])(MSG_\w+)\s*=\s*(0x[0-9A-Fa-f]+)`)

// readPKT feeds a fork World.pkt: replayed at -speed, or tailed with -follow.
func readPKT(w *world, path, opcodesH string, follow bool, speed float64) {
	names := map[uint64]string{}
	// a live capture saved as .pkt names its numbers in <name>-opcodes.tsv
	// (dir, opcode, hex, name); the fork's come from Opcodes.h
	if tsv, err := os.ReadFile(strings.TrimSuffix(path, ".pkt") + "-opcodes.tsv"); err == nil {
		for _, l := range strings.Split(string(tsv), "\n") {
			f := strings.Split(l, "\t")
			if len(f) < 4 {
				continue
			}
			v, err := strconv.ParseUint(f[1], 10, 32)
			if err != nil {
				continue
			}
			if f[0] == "C" {
				v |= 1 << 32
			}
			names[v] = f[3]
		}
		log.Printf("opcode names from %s-opcodes.tsv", strings.TrimSuffix(filepath.Base(path), ".pkt"))
	}
	b, err := os.ReadFile(opcodesH)
	if err != nil && len(names) == 0 {
		log.Fatal(err)
	}
	for _, m := range opLine.FindAllStringSubmatch(string(b), -1) {
		v, _ := strconv.ParseUint(m[3], 0, 32)
		k := v
		if m[1] == "C" {
			k |= 1 << 32
		}
		if names[k] == "" {
			names[k] = m[1] + m[2]
		}
	}
	t, err := pktlog.OpenTail(path)
	if err != nil {
		log.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start := time.Now()
	var t0 time.Time
	for {
		ps, err := t.Next()
		if err != nil {
			log.Fatal(err)
		}
		for _, p := range ps {
			c2s := p.Dir == pktlog.C2S
			k := uint64(p.Opcode)
			if c2s {
				k |= 1 << 32
			}
			m := msg{base.Add(time.Duration(p.TicksMs) * time.Millisecond), c2s, p.Opcode, names[k], p.Body}
			if t0.IsZero() {
				t0 = m.T
			}
			if !follow && speed > 0 {
				if d := time.Until(start.Add(time.Duration(float64(m.T.Sub(t0)) / speed))); d > 0 {
					time.Sleep(d)
				}
			}
			w.handle(m)
		}
		if len(ps) == 0 {
			if !follow {
				log.Printf("log read: %v", w.stats)
				select {}
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
}

func replay(w *world, files []string, speed float64) {
	var all []msg
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			log.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<20), 64<<20)
		for sc.Scan() {
			var j struct{ T, Dir, Opcode, Name, Body string }
			if json.Unmarshal(sc.Bytes(), &j) != nil {
				continue
			}
			t, err := time.Parse("15:04:05.000", j.T)
			if err != nil {
				continue
			}
			var op uint32
			fmt.Sscanf(j.Opcode, "0x%x", &op)
			b, _ := hex.DecodeString(j.Body)
			name := ""
			if s := shortName(j.Name); s != "" {
				name = s
			}
			all = append(all, msg{t, j.Dir == "c2s", op, name, b})
		}
		fh.Close()
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].T.Before(all[j].T) })
	log.Printf("replaying %d messages from %s to %s", len(all), all[0].T.Format("15:04:05"), all[len(all)-1].T.Format("15:04:05"))
	start, t0 := time.Now(), all[0].T
	for _, m := range all {
		if speed > 0 {
			due := start.Add(time.Duration(float64(m.T.Sub(t0)) / speed))
			if d := time.Until(due); d > 0 {
				time.Sleep(d)
			}
		}
		w.handle(m)
	}
}

func shortName(s string) string {
	i := strings.Index(s, "MSG_")
	if i < 1 {
		return ""
	}
	j := i + 4
	for j < len(s) && (s[j] == '_' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	return s[i-1 : j]
}

// envOr is the environment variable k, or def when it is unset.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
