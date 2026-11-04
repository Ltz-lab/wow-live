package main

// The radar's world model as an MCP server: POST /mcp, JSON-RPC 2.0 over
// Claude Code's "http" transport (one request per POST, a JSON answer back).
// Everything is read-only and comes from the same snapshot the page draws.
//
//	claude mcp add --transport http wow-radar http://127.0.0.1:8791/mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
)

const mcpProtocol = "2025-06-18"

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	pRadius = map[string]any{"type": "number", "description": "yards around the player (default 100)"}
	pKind   = map[string]any{"type": "string", "enum": []string{"unit", "player", "gameobject", "corpse"}, "description": "only this kind"}
	pLimit  = map[string]any{"type": "integer", "description": "most objects to return, nearest first (default 40)"}
)

var mcpTools = []mcpTool{
	{"radar_self", "The player right now: map, position, facing, level, health, the quest log (quest ids with objective progress), and any enemy players closing in.", schema(map[string]any{})},
	{"radar_around", "Everything the server shows around the player, nearest first: creatures, players, game objects with name, level, health %, dead, moving (with destination and ETA), quest-giver status and distance in yards.", schema(map[string]any{"radius": pRadius, "kind": pKind, "limit": pLimit, "alive": map[string]any{"type": "boolean", "description": "skip dead units"}})},
	{"radar_find", "Find tracked objects by name (case-insensitive substring), nearest first, anywhere the server has shown them.", schema(map[string]any{"name": map[string]any{"type": "string"}, "kind": pKind, "limit": pLimit}, "name")},
	{"radar_quest_givers", "NPCs nearby with a quest-giver icon: available (!), turnin (?), trivial or future.", schema(map[string]any{"radius": pRadius})},
	{"radar_nodes", "Herb and ore spots near the player with their state (up, empty, gone, seen, unknown) and respawn estimates.", schema(map[string]any{"radius": pRadius})},
	{"radar_status", "Is the stream live: the stream clock, how many objects are tracked, and message counters.", schema(map[string]any{})},
}

const mcpInstructions = "Live WoW world around the player's character, decoded from the game's own network traffic by wow-radar " +
	"(no addon, read-only). Start with radar_self, then radar_around. Distances are yards; positions are world x,y,z. " +
	"If radar_status shows no self, the capture hasn't seen world entry yet."

type around struct {
	GUID    string    `json:"guid"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name,omitempty"`
	Entry   uint32    `json:"entry,omitempty"`
	Level   uint8     `json:"level,omitempty"`
	HPPct   *int      `json:"hp_pct,omitempty"`
	HP      int64     `json:"hp,omitempty"`
	Dead    bool      `json:"dead,omitempty"`
	Dist    float64   `json:"dist"`
	X       float32   `json:"x"`
	Y       float32   `json:"y"`
	Z       float32   `json:"z"`
	Moving  bool      `json:"moving,omitempty"`
	To      []float32 `json:"to,omitempty"`
	ETA     float64   `json:"eta_s,omitempty"`
	QGiver  string    `json:"quest_giver,omitempty"`
	Hostile bool      `json:"enemy_player,omitempty"`
	Race    uint8     `json:"race,omitempty"`
	Class   uint8     `json:"class,omitempty"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func toAround(e ent, self *ent) around {
	a := around{GUID: e.GUID, Kind: e.Kind, Name: e.Name, Entry: e.Entry, Level: e.Level, HP: e.HP, Dead: e.Dead,
		X: e.X, Y: e.Y, Z: e.Z, Moving: e.Moving, QGiver: e.QGiver, Hostile: e.Hostile, Race: e.Race, Class: e.Class}
	if e.MaxHP > 0 {
		p := int(math.Round(100 * float64(e.HP) / float64(e.MaxHP)))
		a.HPPct = &p
	}
	if e.Moving {
		a.To = []float32{e.ToX, e.ToY, e.ToZ}
		a.ETA = round1(e.ETA)
	}
	if self != nil {
		a.Dist = round1(math.Sqrt(sq(e.X-self.X) + sq(e.Y-self.Y) + sq(e.Z-self.Z)))
	}
	return a
}

func sq(v float32) float64 { return float64(v) * float64(v) }

// pick filters a snapshot's objects, nearest first.
func pick(s snapshot, kind, name string, alive bool, limit int, keep func(ent) bool) []around {
	out := []around{}
	name = strings.ToLower(name)
	for _, e := range s.Objects {
		if kind != "" && e.Kind != kind || alive && e.Dead ||
			name != "" && !strings.Contains(strings.ToLower(e.Name), name) || keep != nil && !keep(e) {
			continue
		}
		out = append(out, toAround(e, s.Self))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Dist < out[j].Dist })
	if limit <= 0 {
		limit = 40
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

type mcpArgs struct {
	Radius float64 `json:"radius"`
	Kind   string  `json:"kind"`
	Limit  int     `json:"limit"`
	Alive  bool    `json:"alive"`
	Name   string  `json:"name"`
}

func (w *world) callTool(name string, a mcpArgs, nodeRadius float64) (any, error) {
	r := a.Radius
	if r <= 0 {
		r = 100
	}
	switch name {
	case "radar_self":
		s := w.snapshot(0, 0)
		if s.Self == nil {
			return map[string]any{"self": nil, "note": "no world entry seen yet"}, nil
		}
		me := toAround(*s.Self, nil)
		type questOut struct {
			ID       int32   `json:"id"`
			Progress []int32 `json:"progress,omitempty"`
			*questInfo
		}
		qs := []questOut{}
		for _, q := range s.Quests {
			qs = append(qs, questOut{q.ID, q.P, questNames[q.ID]})
		}
		return map[string]any{"map": s.Map, "clock": s.Clock, "self": me, "facing": s.Self.O, "level": s.Level,
			"race": s.Race, "quests": qs, "threats": s.Threats}, nil
	case "radar_around":
		s := w.snapshot(r, 0)
		return map[string]any{"map": s.Map, "clock": s.Clock, "radius": r, "objects": pick(s, a.Kind, "", a.Alive, a.Limit, nil)}, nil
	case "radar_find":
		if a.Name == "" {
			return nil, fmt.Errorf("radar_find needs a name")
		}
		s := w.snapshot(math.Inf(1), 0)
		return map[string]any{"clock": s.Clock, "matches": pick(s, a.Kind, a.Name, false, a.Limit, nil)}, nil
	case "radar_quest_givers":
		s := w.snapshot(r, 0)
		return map[string]any{"clock": s.Clock, "givers": pick(s, "unit", "", true, 100, func(e ent) bool { return e.QGiver != "" })}, nil
	case "radar_nodes":
		if a.Radius <= 0 {
			r = nodeRadius
		}
		s := w.snapshot(0, r)
		return map[string]any{"clock": s.Clock, "nodes": s.Nodes}, nil
	case "radar_status":
		s := w.snapshot(math.Inf(1), 0)
		return map[string]any{"clock": s.Clock, "map": s.Map, "has_self": s.Self != nil, "objects": len(s.Objects), "stats": s.Stats}, nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func mcpHandler(w *world, nodeRadius float64) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			rw.Header().Set("Allow", "POST")
			http.Error(rw, "POST JSON-RPC here", http.StatusMethodNotAllowed)
			return
		}
		var req rpcReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPC(rw, nil, nil, &rpcErr{-32700, "parse error: " + err.Error()})
			return
		}
		if len(req.ID) == 0 { // a notification
			rw.WriteHeader(http.StatusAccepted)
			return
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			v := p.ProtocolVersion
			if v == "" {
				v = mcpProtocol
			}
			writeRPC(rw, req.ID, map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "wow-radar", "version": "0.1"}, "instructions": mcpInstructions}, nil)
		case "ping":
			writeRPC(rw, req.ID, map[string]any{}, nil)
		case "tools/list":
			writeRPC(rw, req.ID, map[string]any{"tools": mcpTools}, nil)
		case "tools/call":
			var p struct {
				Name      string  `json:"name"`
				Arguments mcpArgs `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				writeRPC(rw, req.ID, nil, &rpcErr{-32602, err.Error()})
				return
			}
			out, err := w.callTool(p.Name, p.Arguments, nodeRadius)
			if err != nil {
				writeRPC(rw, req.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}, nil)
				return
			}
			b, _ := json.MarshalIndent(out, "", " ")
			writeRPC(rw, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}}, nil)
		default:
			writeRPC(rw, req.ID, nil, &rpcErr{-32601, "method not found: " + req.Method})
		}
	}
}

func writeRPC(rw http.ResponseWriter, id json.RawMessage, result any, e *rpcErr) {
	rw.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if id == nil {
		resp["id"] = nil
	}
	if e != nil {
		resp["error"] = e
	} else {
		resp["result"] = result
	}
	json.NewEncoder(rw).Encode(resp)
}
