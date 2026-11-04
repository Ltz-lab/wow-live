package main

import (
	"testing"
	"time"
)

func TestNodeKind(t *testing.T) {
	for name, want := range map[string]string{
		"Peacebloom": "herb", "Wilted Peacebloom": "herb", "Khadgar's Whisker": "herb",
		"Copper Vein": "ore", "Poor Copper Vein": "ore", "Ooze Covered Mithril Deposit": "ore",
		"Mailbox": "", "": "", "Old Lion Statue": "",
	} {
		if got := nodeKind(name); got != want {
			t.Errorf("nodeKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func stateAt(n *nodes, x, y float32, now time.Time) nodeOut {
	for _, o := range n.view(0, x, y, 500, 90, now) {
		if o.D < 1 {
			return o
		}
	}
	return nodeOut{State: "missing"}
}

// A known spawn goes unknown -> up -> gone -> (respawn measured) -> up,
// and a second gather counts down from the measured respawn.
func TestSpotLifecycle(t *testing.T) {
	n := newNodes()
	n.add(&spot{Map: 0, X: 100, Y: 100, Name: "Peacebloom", Kind: "herb", known: true})
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	far := func(now time.Time) nodeOut { // looked at from 300 yd: outside view range
		o := n.view(0, 400, 100, 500, 90, now)
		return o[0]
	}
	if o := far(t0); o.State != "unknown" {
		t.Fatalf("before = %s, want unknown", o.State)
	}
	s := n.up("g1", 0, "Wilted Peacebloom", 103, 101, 50, t0) // 3 yd off: the same spot
	if !s.known || s.Name != "Wilted Peacebloom" {
		t.Fatalf("live node not matched to the known spawn: %+v", s)
	}
	if o := stateAt(n, 100, 100, t0); o.State != "up" {
		t.Fatalf("up = %s", o.State)
	}
	n.down(s, "g1", true, t0.Add(time.Minute))
	if o := far(t0.Add(2 * time.Minute)); o.State != "gone" || o.Ago != 60 || o.ETA != 0 {
		t.Fatalf("gone = %+v", o)
	}
	n.up("g2", 0, "Wilted Peacebloom", 103, 101, 50, t0.Add(6*time.Minute)) // back after 5 min
	if got := n.respawnFor("Wilted Peacebloom"); got != 300 {
		t.Fatalf("respawn = %v, want 300", got)
	}
	n.down(s, "g2", true, t0.Add(10*time.Minute))
	if o := far(t0.Add(12 * time.Minute)); o.State != "gone" || o.ETA != 180 {
		t.Fatalf("second gather = %+v, want gone with 180 s to go", o)
	}
}

// Out of range from far away is not a gather: the spot is "seen" (last up).
// Standing inside view range with nothing showing marks it empty.
func TestSeenAndEmpty(t *testing.T) {
	n := newNodes()
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	s := n.up("g1", 0, "Copper Vein", 0, 0, 0, t0)
	n.down(s, "g1", false, t0.Add(time.Second))
	o := n.view(0, 200, 0, 500, 90, t0.Add(31*time.Second))[0]
	if o.State != "seen" || o.Ago != 30 {
		t.Fatalf("seen = %+v", o)
	}
	if o := n.view(0, 50, 0, 500, 90, t0.Add(40*time.Second))[0]; o.State != "empty" {
		t.Fatalf("inside view range = %+v, want empty", o)
	}
	if o := n.view(0, 200, 0, 500, 90, t0.Add(100*time.Second))[0]; o.State != "empty" || o.Ago != 60 {
		t.Fatalf("after = %+v, want empty checked 60 s ago", o)
	}
}

// What earlier sessions learned comes back onto the known spawns.
func TestSeenRoundTrip(t *testing.T) {
	path := t.TempDir() + "/seen.json"
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	a := newNodes()
	a.add(&spot{Map: 0, X: 10, Y: 10, Name: "Tin Vein", Kind: "ore", known: true})
	s := a.up("g", 0, "Tin Vein", 10, 10, 5, t0)
	a.down(s, "g", true, t0.Add(time.Minute))
	a.save(path)
	b := newNodes()
	b.add(&spot{Map: 0, X: 10, Y: 10, Name: "Tin Vein", Kind: "ore", known: true})
	b.loadSeen(path)
	if len(b.spots) != 1 {
		t.Fatalf("%d spots, want the one known spawn", len(b.spots))
	}
	for _, s := range b.spots {
		if !s.Gone.Equal(t0.Add(time.Minute)) || !s.HasZ {
			t.Fatalf("history lost: %+v", s)
		}
	}
}
