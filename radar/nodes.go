package main

// Herb and ore nodes. Every node spawn point in forever.db (tools/nodes-export.py
// writes nodes.json with world coordinates) plus every node the server shows
// us becomes a spot, and each spot carries what we know about it:
//
//	up       the server is showing a node there now
//	empty    we are close enough that the server would show one, and it does not
//	gone     we saw it gathered (destroyed, or it vanished well inside view range)
//	seen     up when we last saw it, now out of view
//	unknown  never been near it
//
// A spot remembers when it was gathered and when we last saw it empty, and
// the time from gathered to up again at the same spot is measured, so the
// radar can count down to the respawn. With -seen (live only) that survives
// restarts.

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

var herbs = []string{
	"Peacebloom", "Silverleaf", "Earthroot", "Mageroyal", "Briarthorn", "Stranglekelp", "Bruiseweed",
	"Wild Steelbloom", "Grave Moss", "Kingsblood", "Liferoot", "Fadeleaf", "Goldthorn", "Khadgar's Whisker",
	"Wintersbite", "Firebloom", "Purple Lotus", "Arthas' Tears", "Sungrass", "Blindweed", "Ghost Mushroom",
	"Gromsblood", "Golden Sansam", "Dreamfoil", "Mountain Silversage", "Plaguebloom", "Icecap", "Black Lotus",
	"Swiftthistle", "Wildvine",
}

// nodeKind is "herb", "ore" or "" for a game object's name. Forever's own
// entries ("Wilted Peacebloom", "Poor Copper Vein") count.
func nodeKind(name string) string {
	switch {
	case name == "":
		return ""
	case strings.HasSuffix(name, " Vein"), strings.HasSuffix(name, " Deposit"):
		return "ore"
	}
	for _, h := range herbs {
		if strings.Contains(name, h) {
			return "herb"
		}
	}
	return ""
}

const (
	spotMatch = 8.0  // yards: a live node this close to a known spawn is that spawn
	cellSize  = 64.0 // the spot index's grid
)

type spot struct {
	Key     string    `json:"-"`
	Map     uint16    `json:"m"`
	X       float32   `json:"x"`
	Y       float32   `json:"y"`
	Z       float32   `json:"z,omitempty"`
	HasZ    bool      `json:"hz,omitempty"`
	Name    string    `json:"n"`
	Kind    string    `json:"k"`
	Gone    time.Time `json:"gone,omitempty"`    // gathered, as we watched
	Checked time.Time `json:"checked,omitempty"` // last seen empty
	Seen    time.Time `json:"seen,omitempty"`    // last seen up
	Respawn []float64 `json:"respawn,omitempty"` // measured gathered -> up again, seconds
	live    string    // the GUID of the node showing here now
	known   bool      // from nodes.json
}

type nodes struct {
	entries map[uint32]string
	spots   map[string]*spot
	cells   map[[3]int32][]*spot // map, cell x, cell y
	dirty   bool
}

func newNodes() *nodes {
	return &nodes{entries: map[uint32]string{}, spots: map[string]*spot{}, cells: map[[3]int32][]*spot{}}
}

func cellOf(m uint16, x, y float32) [3]int32 {
	return [3]int32{int32(m), int32(math.Floor(float64(x) / cellSize)), int32(math.Floor(float64(y) / cellSize))}
}

func spotKey(m uint16, x, y float32) string {
	return fmt.Sprintf("%d:%.0f:%.0f", m, math.Round(float64(x)/2)*2, math.Round(float64(y)/2)*2)
}

func (n *nodes) add(s *spot) *spot {
	if s.Key == "" {
		s.Key = spotKey(s.Map, s.X, s.Y)
	}
	if old := n.spots[s.Key]; old != nil {
		return old
	}
	n.spots[s.Key] = s
	c := cellOf(s.Map, s.X, s.Y)
	n.cells[c] = append(n.cells[c], s)
	return s
}

// near calls fn for every spot within r yards of (x, y) on map m.
func (n *nodes) near(m uint16, x, y float32, r float64, fn func(s *spot, d float64)) {
	lo, hi := cellOf(m, x-float32(r), y-float32(r)), cellOf(m, x+float32(r), y+float32(r))
	for cx := lo[1]; cx <= hi[1]; cx++ {
		for cy := lo[2]; cy <= hi[2]; cy++ {
			for _, s := range n.cells[[3]int32{int32(m), cx, cy}] {
				if d := math.Hypot(float64(s.X-x), float64(s.Y-y)); d <= r {
					fn(s, d)
				}
			}
		}
	}
}

// load reads nodes.json (tools/nodes-export.py).
func (n *nodes) load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var f struct {
		Entries map[string]string `json:"entries"`
		Spawns  []struct {
			M uint16  `json:"m"`
			N string  `json:"n"`
			X float32 `json:"x"`
			Y float32 `json:"y"`
		} `json:"spawns"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	for k, v := range f.Entries {
		var id uint32
		fmt.Sscan(k, &id)
		n.entries[id] = v
	}
	for _, s := range f.Spawns {
		n.add(&spot{Map: s.M, X: s.X, Y: s.Y, Name: s.N, Kind: nodeKind(s.N), known: true})
	}
	return nil
}

// loadSeen merges what earlier sessions learned (-seen).
func (n *nodes) loadSeen(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved []spot
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for i := range saved {
		s := saved[i]
		t := n.add(&s)
		if t != &s { // a known spawn: take the history
			t.Gone, t.Checked, t.Seen, t.Respawn = s.Gone, s.Checked, s.Seen, s.Respawn
			if s.HasZ {
				t.Z, t.HasZ = s.Z, true
			}
		}
	}
}

func (n *nodes) save(path string) {
	if !n.dirty || path == "" {
		return
	}
	var out []spot
	for _, s := range n.spots {
		if !s.Gone.IsZero() || !s.Checked.IsZero() || !s.Seen.IsZero() || !s.known {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	b, _ := json.Marshal(out)
	if err := os.WriteFile(path+".tmp", b, 0o644); err == nil {
		os.Rename(path+".tmp", path)
		n.dirty = false
	} else {
		log.Printf("nodes: %v", err)
	}
}

// up: the server shows a node: match it to a spot and mark it up. A spot that
// was gathered and is up again gives one respawn measurement.
func (n *nodes) up(guid string, m uint16, name string, x, y, z float32, t time.Time) *spot {
	var best *spot
	bd := spotMatch
	n.near(m, x, y, spotMatch, func(s *spot, d float64) {
		if d < bd && (s.live == "" || s.live == guid) {
			best, bd = s, d
		}
	})
	if best == nil {
		best = n.add(&spot{Map: m, X: x, Y: y, Name: name, Kind: nodeKind(name)})
	}
	best.Name, best.Kind = name, nodeKind(name)
	best.Z, best.HasZ = z, true
	if !best.Gone.IsZero() {
		if d := t.Sub(best.Gone).Seconds(); d > 10 && d < 6*3600 {
			best.Respawn = append(best.Respawn, d)
			if len(best.Respawn) > 8 {
				best.Respawn = best.Respawn[1:]
			}
		}
		best.Gone = time.Time{}
	}
	best.live, best.Seen = guid, t
	n.dirty = true
	return best
}

// down: the node at s is no longer shown. gathered says it went while we were
// close enough that it can only have been gathered (or despawned).
func (n *nodes) down(s *spot, guid string, gathered bool, t time.Time) {
	if s == nil || s.live != guid {
		return
	}
	s.live = ""
	if gathered {
		s.Gone = t
	} else {
		s.Seen = t // still up as it left view
	}
	n.dirty = true
}

// respawnFor is the typical respawn of nodes of this name: the median of every
// measurement at any spot of that name, or 0 if none yet.
func (n *nodes) respawnFor(name string) float64 {
	var all []float64
	for _, s := range n.spots {
		if s.Name == name {
			all = append(all, s.Respawn...)
		}
	}
	if len(all) == 0 {
		return 0
	}
	sort.Float64s(all)
	return all[len(all)/2]
}

type nodeOut struct {
	Name  string  `json:"n"`
	Kind  string  `json:"k"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Z     float32 `json:"z"`
	HasZ  bool    `json:"hz,omitempty"`
	State string  `json:"st"`            // up, empty, gone, seen, unknown
	Ago   float64 `json:"ago,omitempty"` // seconds since gathered (gone), last checked (empty) or last seen up (seen)
	ETA   float64 `json:"eta,omitempty"` // seconds to the expected respawn, if measured
	D     float64 `json:"d"`
}

// view lists the spots within r of us. Spots inside vis yards with no node
// showing are marked checked: the server would be showing one.
func (n *nodes) view(m uint16, x, y float32, r, vis float64, now time.Time) []nodeOut {
	var out []nodeOut
	resp := map[string]float64{}
	n.near(m, x, y, r, func(s *spot, d float64) {
		o := nodeOut{Name: s.Name, Kind: s.Kind, X: s.X, Y: s.Y, Z: s.Z, HasZ: s.HasZ, D: math.Round(d*10) / 10}
		switch {
		case s.live != "":
			o.State = "up"
		case d < vis && !now.IsZero():
			if s.Checked.Before(now.Add(-5 * time.Second)) {
				n.dirty = true
			}
			s.Checked = now
			o.State = "empty"
		case !s.Gone.IsZero():
			o.State = "gone"
		case s.Seen.After(s.Checked):
			o.State = "seen"
		case !s.Checked.IsZero():
			o.State = "empty"
		default:
			o.State = "unknown"
		}
		if o.State != "up" {
			from := s.Checked
			if o.State == "seen" {
				from = s.Seen
			}
			if !s.Gone.IsZero() {
				from = s.Gone
				if o.State == "empty" {
					o.State = "gone" // gathered and not back yet
				}
			}
			if !from.IsZero() {
				o.Ago = math.Round(now.Sub(from).Seconds())
			}
			if !s.Gone.IsZero() {
				rs, ok := resp[s.Name]
				if !ok {
					rs = n.respawnFor(s.Name)
					resp[s.Name] = rs
				}
				if rs > 0 {
					o.ETA = math.Max(0, math.Round(rs-now.Sub(s.Gone).Seconds()))
				}
			}
		}
		out = append(out, o)
	})
	sort.Slice(out, func(i, j int) bool { return out[i].D < out[j].D })
	return out
}
