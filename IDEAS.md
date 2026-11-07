# Ideas

Things that could be built on wow-live's method: the world connection's own traffic, decrypted
live with keys recorded at login, without touching the game process. Anything the server sends
the client is data.

Rough order within each section: most distinctive first.

## What's new here

Areas where the method sees things that addons (limited to the game's API) and file data-miners
(limited to what the client ships) can't.

- **NPC movement and spawn mapping.** Real patrol routes, leash ranges, respawn timers and how
  mobs pick targets, from the server's own positions and movement destinations. Little public
  data exists on any of it. Probably the most distinctive idea here, and it's about the world,
  not other players.
- **Measuring the server.** Update rate, spell batching, the real spell queue window, how latency
  changes what you can do, the order the server resolves events in. Measured with millisecond
  timestamps instead of argued about on forums.
- **Encounter recordings beyond the combat log.** Boss mod authors record fights with
  Transcriptor, which only sees what the API exposes. A capture also has positions, hidden NPC
  auras, invisible helper units and movement for the whole fight: how a mechanic is actually
  built.
- **Hotfixes as they arrive.** Log every hotfix the server pushes, timestamped, for a timeline of
  what changes and when. File data-miners mostly see them afterwards, in `DBCache.bin`.
- **Claude as a research assistant on captures.** Extend the radar's MCP server so Claude can
  query a whole recorded session: "how long did that rare take to respawn?", "where did this NPC
  patrol?".
- **Measuring randomness.** Real proc rates, crit streaks and bad-luck protection on drops,
  counted over thousands of events.
- **Reverse-engineering formulas.** Damage, armor, healing and stat scaling from server-reported
  numbers, automated where SimulationCraft does it by hand.
- **Sharding and population.** Which shard you're on, when the server moves you, how many players
  are around over time.
- **New features in the protocol first.** New message types show up in client builds before the
  features ship. Diffing opcodes between patches is a different angle from file data-mining.
- **A public retail protocol spec.** Clean documentation of the current retail protocol barely
  exists in public; it's scattered across private-server code.
- **3D fight replays.** Rebuild a session in the radar's three.js view, with players, mobs,
  spells and movement, and scrub through it like a video.
- **Accessibility.** Audio cues for blind or low-vision players: an enemy casting behind you, a
  proc ready, a mob approaching from the left. Addons can't do this well, and it would help real
  people.
- **World event timers.** When world quests, rares and events actually spawn, logged across
  sessions, for accurate schedules and predictions.

## Tools on the feed

- **More rotation specs.** The engine is general and `rotation/tools/simc_apl.py` already writes
  rules from SimC profiles, so specs beyond Havoc are mostly configuration.
- **Personal performance coach.** Replay a fight against what the rotation suggested: missed
  cooldowns, wasted resources, idle GCDs. A private Warcraft Logs for your own play.
- **Live meters and timers.** DPS, healing and threat meters and boss ability timers from the
  combat events, on the overlay or a second screen.
- **Stream overlays.** `feedhub` already serves WebSockets, so OBS overlays can show cooldowns,
  procs or the rotation queue.
- **Hardware displays.** A Stream Deck or LEDs that light up when a big cooldown is ready or a
  proc fires.
- **Claude as a co-pilot.** Combat, quest and inventory tools next to the radar's, so Claude can
  answer "where's the next quest objective?" or review the last pull.
- **Economy tracking.** Decode auction house replies to log prices over time.
- **Loot and farming stats.** Drops, gold per hour and node respawns over many sessions.

## What the traffic already shows

- **Players behind walls.** The server sends every unit within range whether or not there's line
  of sight; hiding them is the client's job. The radar already draws them. Players beyond range
  aren't sent, and stealthed players are usually withheld until detectable.
- **Enemy cooldowns.** Every spell an enemy casts is in the combat events.
- **Rare spawns and mob paths.** Creatures in range appear with their movement destinations,
  before you can see them.
- **Details the UI hides.** Exact values behind health bars, hidden auras and flags, update fields
  the interface never shows.
- **Hotfixes and unreleased content.** Database hotfixes pushed to the client.
- **Phasing and hidden objects.** Objects or NPCs your phase isn't meant to show yet.

## A note on risk

Recording and decoding your own client's traffic may already be against the game's Terms of Use
(see the README). Analysing your own play, or showing what the game already shows, is low-risk.
Revealing hidden information about other players, or pressing keys for you, is much more clearly
cheating: more likely to get an account banned, and harder to share publicly.
