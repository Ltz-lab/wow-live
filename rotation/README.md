# wow-rotation

A Hekili-style "press this next" for retail WoW (Midnight, 12.1.5), driven by the live combat
log instead of an addon. It reads the JSON lines of `tools/combatfeed` (in this repo's root) (the combat
log rebuilt from the decrypted world connection), works out the state a rotation needs, and adds
`{"type":"next",...}` lines with the next presses for `tools/auraoverlay` to draw over the game.

```bash
cargo build --release
combatfeed -run <capture-live run> -follow -spelldata <wago CSVs> \
  | target/release/wow-rotation rotations/havoc_fel_scarred.toml --feed --next 4 --haste 0.18 \
  | python3 tools/auraoverlay/auraoverlay.py --rotation
```

With `tools/feedhub` between the rotation and the overlay (`havoc-helper` puts it
there), the same lines, `next` included, are also served on `ws://127.0.0.1:7373/feed`, with
a live view at `http://127.0.0.1:7373/`.

`tools/havoc-helper` runs that whole chain in one command: it records the game, decodes it
live, runs the rotation, serves it on the feed hub and draws the icon row, and stops when you quit the game.

```bash
tools/havoc-helper            # play; quit the game to stop
tools/havoc-helper --check    # what it will use
```

## What's here

| path | what |
|---|---|
| `src/rotation.rs` | the engine: rotation files, state from combat-log events and combatfeed snapshots, look-ahead |
| `src/expr.rs` | the condition language (from azsim, Ltz-lab/rust-simulation) |
| `rotations/havoc_fel_scarred.toml` | Havoc Demon Hunter: spell and aura ids, talents, cooldown effects, and SimC's APL |
| `tools/simc_apl.py` | writes a rotation's rules straight from a SimC profile's default APL |
| `profiles/` | the SimC profiles the rules come from |
| `tests/havoc.rs` | the Havoc rules and the state tracking, on events and combatfeed lines |
| `HAVOC.md` | how Havoc's rules map to combat-log state, ids, evidence from live play |
| `tools/auraoverlay/auraoverlay.py` | draws the suggestions over the game window (`--rotation`: the icon row only) |
| `tools/havoc-helper` | all of it in one command |

## Modes

```bash
wow-rotation <rotation.toml> --feed   [--next n] [--haste h]   # combatfeed in, same lines + "next" out
wow-rotation <rotation.toml> --audit  [--next n] < feed.jsonl  # each press against what was suggested
wow-rotation <rotation.toml> --replay events.jsonl             # raw combat-log events
wow-rotation <rotation.toml> --demo 40 [--targets n]           # a stand-in fight
```

## Updating the rotation from SimC

```bash
python3 tools/simc_apl.py profiles/MID2_Demon_Hunter_Havoc.simc rotations/havoc_fel_scarred.toml
```

Python 3.11+. Talents are read live from the passive auras in the player's aura list, so SimC's
talent branches follow the character actually being played.

The feed side (capture, decrypt, combatfeed, feedhub) is the rest of this repo: see the
[top-level README](../README.md) for setup. `tools/havoc-helper` finds it at the repo root
(`WOW_FEED` to point elsewhere) and keeps recordings in `captures/`.
