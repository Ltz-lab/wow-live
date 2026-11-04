# wow-live

Live WoW game state from the stock client's own network traffic, and the tools built on it.
The traffic is recorded and decrypted as it is written, then turned into JSON lines that any
tool can read. Nothing is injected into the game process or reads its memory. Works with retail (Midnight 12.1.5) and
WoW Forever (1.60.1).

| part | what |
|---|---|
| **the feed** (`tools/`, `protocol/`) | capture, decrypt, and the combat feed: auras, cooldowns, power and every combat event as JSON lines, served over WebSocket by `feedhub` |
| **the rotation helper** (`rotation/`, Rust) | a Hekili-style "press this next" that reads the feed, with SimC's Havoc Demon Hunter APL and an icon overlay |
| **the radar** (`radar/`) | every unit and object the server shows around you, mob paths, quest givers, herb and ore nodes, enemy players; a 3D web view, an overlay, and an MCP server so Claude can see around your character |

```
bnetrelay (login, records the world-key secrets)  ─┐
tcpdump of the world port (world.pcap)            ─┴→ worldstream (decrypt as it grows)
                                                       ├→ combatfeed → rotation → feedhub → overlay
                                                       └→ radar (web view, overlay, MCP)
```

## Setup (macOS)

Needs Go 1.23+, Rust (for the rotation helper), python3 with PyObjC (overlays), jq, and sudo for tcpdump.

```bash
sudo setup/connect-client.sh      # once: local certs, trusted CA, hosts entry, portal file (undo: ... undo)
sudo -v                           # tcpdump runs under sudo
tools/capture-live --dry-run      # check everything without starting anything
```

The client logs in through `tools/bnetrelay` on this Mac, which keeps the session's world-key
secrets so the world connection can be decrypted. Recordings go to `captures/<timestamp>/`
(git-ignored, mode 0700). **They hold your session's keys: never share or commit them.**

| environment | default |
|---|---|
| `WOW_RETAIL_DIR` | `/Applications/World of Warcraft/_retail_` |
| `WOW_FOREVER_DIR` | `/Applications/World of Warcraft/_forever_` |
| `WOW_LIVE_CAPTURES` | `captures/` in this repo |
| `WOW_LIVE_CERTS` | `certs/` in this repo |
| `WOW_FOREVER_DB` | an optional SQLite db of Forever names (`npcs`, `objects`, `quests`, `spells` tables) |

## Use

```bash
# the Havoc rotation helper: records, decodes, suggests and draws, until you quit the game
(cd rotation && cargo build --release) && rotation/tools/havoc-helper

# the feed by itself
tools/capture-live --game retail                       # relay + tcpdump + the game; quit the game to stop
go run ./tools/combatfeed -run captures/<run> -follow -spelldata <wago CSVs>

# the radar on a run that is recording, then http://127.0.0.1:8791
go run ./radar -run captures/<run> -follow             # retail; Forever: add -build 70755
claude mcp add --transport http wow-radar http://127.0.0.1:8791/mcp   # let Claude Code see around you
```

See [rotation/README.md](rotation/README.md), [rotation/HAVOC.md](rotation/HAVOC.md) and
[radar/README.md](radar/README.md) for each part.

## Feed lines

```
{"type":"player","guid":"..."}                        CMSG_PLAYER_LOGIN seen
{"type":"target","guid":"..."}                        CMSG_SET_SELECTION
{"type":"event","t":...,"sub":"SPELL_DAMAGE",...}     every combat event
{"type":"auras","unit":"player","auras":[...]}        the unit's auras, on every change
{"type":"cooldowns","gcd_ms":...,"cooldowns":[...]}   what is still recovering (absent = ready)
{"type":"power","powers":[{"type":17,"name":"fury","amount":42}]}
```

## The feed as a WebSocket server

`tools/feedhub` goes at the end of any feed pipe. It passes every line through to stdout
unchanged and serves the same lines to any number of local WebSocket clients: overlays,
dashboards, Discord bots, hardware displays. Lines that filters earlier in the pipe add,
such as the rotation's `next`, are served too.

```bash
combatfeed ... | wow-rotation <rotation.toml> --feed | go run ./tools/feedhub | auraoverlay.py --rotation
```

| endpoint (default `127.0.0.1:7373`) | what |
|---|---|
| `ws://…/feed` | each line as one message, `{"v":1,"seq":N,...}` |
| `ws://…/feed?types=auras,next` | only those types |
| `http://…/state` | the current state as JSON lines |
| `http://…/` | a live view in the browser |

A new client first gets the current state (the latest line of each type, per `unit` where
the line has one; never old `event`s; a `player` line, a login or relog, starts the state
afresh), then `{"type":"hub","live":true}` with the latest
`seq`, then the stream. From then on `seq` rises by one per line, so a gap means a missed
line. A client that can't keep up is closed with 1008 "too slow" and never holds up the
feed. Browser pages can connect only from the hub's own page or a `localhost`/`127.0.0.1`
origin (`-origin` adds more); `-token` requires `?token=`. When the feed ends, clients are
closed with 1000 "feed ended".

```js
const ws = new WebSocket('ws://127.0.0.1:7373/feed?types=next');
ws.onmessage = e => console.log(JSON.parse(e.data).queue?.[0]?.name);
```

On macOS `combatfeed -follow` wakes the moment tcpdump appends to the capture (kqueue), so a
packet reaches the feed about a millisecond after it lands.

## What's here

| path | what |
|---|---|
| `tools/capture-live` | records the stock client on live: bnet relay, tcpdump of the world port, the game, decrypt at the end |
| `tools/bnetrelay` | the Battle.net relay that records the session's world-key secrets |
| `tools/worlddecrypt` | a finished capture to one JSON line per message |
| `tools/combatfeed` | the live feed |
| `tools/combatlog` | a capture's combat log, printed |
| `tools/feedhub` | the feed as a local WebSocket server, with a live view |
| `protocol/worldstream` | incremental decryption of a growing pcap |
| `protocol/combatlog` | CLEU-style events rebuilt from the server's combat messages |
| `protocol/spellstate` | cooldowns, charges, the GCD and power from the stream |
| `protocol/opcodes`, `codec`, `framing`, `pktlog`, `version` | the wire format |
| `auth`, `transport`, `world` | Battle.net, the connection and the world-key derivation |

| `tools/agent_relay.py` | sits between the game and the Battle.net Agent while capture-live starts it |
| `setup/connect-client.sh` | the one-time machine setup |
| `rotation/` | the rotation helper (its own README) |
| `radar/` | the radar (its own README) |
| `protocol/updateobject`, `state` | the world's objects and their update fields |

## Legal

An independent fan project, not affiliated with or endorsed by Blizzard Entertainment. World of
Warcraft and Battle.net are trademarks of Blizzard. Recording and decoding your own client's traffic
may be against the game's Terms of Use; use it at your own risk. GPL-3.0 (see LICENSE).
