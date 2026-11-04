# wow-radar

The world around your character in WoW, from the server's own positions: every player, creature
and game object the server tells the client about, where moving mobs are heading, and you in the
middle. Nothing touches the game process: the input is the world connection's traffic, recorded and
decrypted by `tools/capture-live` (repo root), or a local TrinityCore fork's `World.pkt`.

```bash
go run . -run ../captures/<stamp> -follow               # retail, live
go run . -run <capture-live --game forever run> -follow -build 70755           # Forever, live
go run . -pkt <fork>/logs/World.pkt -follow                      # the local fork
go run . -jsonl conn-1.jsonl,conn-2.jsonl -speed 1                             # replay worlddecrypt output
python3 overlay.py                                                             # over the game window
python3 tools/nodes-export.py                                                  # once: herb and ore spawns -> nodes.json
```

Then http://127.0.0.1:8791 for the 3D view (three.js), or the overlay over the game.

## MCP

The running radar is also an MCP server at `http://127.0.0.1:8791/mcp` (JSON-RPC over HTTP,
read-only), so Claude Code can ask what's around the character while you play:

```bash
claude mcp add --scope user --transport http wow-radar http://127.0.0.1:8791/mcp
```

| tool | what |
|---|---|
| `radar_self` | map, position, level, health, the quest log (names, objectives, progress), enemy players closing in |
| `radar_around` | everything within `radius` yards, nearest first: name, level, health %, dead, moving with destination and ETA, quest-giver icon |
| `radar_find` | tracked objects by name, nearest first |
| `radar_quest_givers` | NPCs nearby with a ! or ? |
| `radar_nodes` | herb and ore spots with up/empty/gone and respawn estimates |
| `radar_status` | is the stream live: clock, object count, message counters |

On Forever (`-pkt`, or `-build` other than retail's 70783) names come from the Forever names db (`WOW_FOREVER_DB`)
(`-db`), read once with the `sqlite3` command line, since Forever's WDB caches start empty.

## What it reads

| Message | What for |
|---|---|
| `SMSG_UPDATE_OBJECT` | every object's position and kind (create blocks); on Forever also level, health, NPC flags and our quest log |
| `SMSG_ON_MONSTER_MOVE` | mob splines: where a unit goes and when it gets there |
| `SMSG_MOVE_UPDATE*` | other players; they coast at their measured speed between updates |
| client `CMSG_MOVE_*` | us |
| `SMSG_QUEST_GIVER_STATUS(_MULTIPLE)` | the ! and ? over each NPC |
| `creaturecache.wdb`, query responses, `forever.db` | names |

## The overlay

A click-through window over the game (shown while WoW is in front), heading-up:

- **Forever: levelling.** Creatures in con colours, gold rings on your quest targets with their
  counts ("Tough Wolf Meat 3/8"): mobs to kill, mobs that drop a quest item, objects to loot or use,
  from the Forever names db (`WOW_FOREVER_DB`). Quest givers' ! and ?, and a list of the nearest objectives.
- **Retail: players.** Everyone within ~200 yd, which way they face, distance and height.
- **Herbs and ore (both modes).** Every node spot, whatever your tracking: green diamonds for
  herbs, copper squares for ore. Filled when the server shows the node up; hollow with a timer once
  it is gathered (a countdown when the respawn has been measured at that spot, else time since);
  faint when you have been close enough to see it was empty, or saw it up from afar. The nearest
  up ones are listed under the radar and pinned on its edge when beyond it.
- **Enemy players (Forever).** The other faction in red with level and class. Anyone inside 60 yd,
  or inside 120 yd and closing faster than 3 yd/s, gets a warning line above the radar and a chime
  the first time (`--no-chime` to mute).
- `--heads` (experimental): arrows over players' heads with a fixed chase camera. The network
  carries your facing, not the camera, so it only lines up while the camera sits where the flags say.

`python3 overlay.py --help` for the flags.

## Node spots

`tools/nodes-export.py` reads every herb and ore spawn in the Forever names db (`WOW_FOREVER_DB`)
(Questie and Wowhead, as zone-map fractions), fits each zone's map to world coordinates from the
NPC spawns that have both (exact: 49/49 zones at 0.0 yd), and writes `nodes.json`: about 15,000
spots. A live Peacebloom in Elwynn lands 0.1 yd from its exported spawn. Forever's own starter-zone
nodes (Wilted Peacebloom, Stunted Silverleaf, Poor Copper Vein) are not in the db; the radar learns
those as you see them.

A node that is destroyed, or leaves view while you stand inside 0.6 × `-vis` (90 yd) of it, was
gathered; when it is back at the same spot the time is one respawn measurement. Gathered and empty
times and the measurements are kept in `~/.config/wow-radar/seen.json` (live runs only, `-seen ""`
to disable), so the next session starts knowing which spots were cleared and when.

## Layout

| path | what |
|---|---|
| `main.go` | the radar: inputs, the world model, `/state`, the page |
| `web/index.html` | the 3D view |
| `overlay.py` | the overlay (PyObjC) |
| `nodes.go` | herb and ore spots: up, gone, empty, last seen; respawn timers |
| `pvp.go` | enemy players: faction from race, closing speed, warnings |
| `tools/nodes-export.py` | forever.db's node spawns to world coordinates (`nodes.json`) |

The decoders are the repo's own `protocol/updateobject`, `worldstream`, `pktlog` and `state`.
Herb and ore nodes, and Forever names, need a Forever names db (`WOW_FOREVER_DB`); without one the
radar still runs, with fewer names and no nodes.
