# Havoc live overlay (combat-log driven)

`rotations/havoc_fel_scarred.toml` is a Havoc Demon Hunter (Fel-Scarred) priority list for a
Hekili-style "press this next" overlay on retail 12.1.5, fed only by the combat-log stream that
wow-forever rebuilds from the world connection (`protocol/combatlog`, see its `docs/RETAIL.md`).
`src/rotation.rs` turns those events into state and evaluates the list; nothing in it is
Havoc-specific.

```bash
wow-rotation rotations/havoc_fel_scarred.toml --demo 45 --haste 0.17            # stand-in fight, 1 target
wow-rotation rotations/havoc_fel_scarred.toml --demo 45 --haste 0.17 --targets 4
wow-rotation rotations/havoc_fel_scarred.toml --replay events.jsonl --next 3 [--player <guid>]
```

## In the live pipeline

```bash
combatfeed -run <capture-live run> -spelldata <wago CSV dir> -follow \
  | wow-rotation rotations/havoc_fel_scarred.toml --feed --next 3 \
  | auraoverlay
```

`--feed` reads wow-feed's `tools/combatfeed` lines on stdin, writes every line back unchanged
(flushed per line), and adds one line whenever the suggestion changes:

```json
{"type":"next","t":36032,"spell":198013,"name":"Eye Beam","action":"eye_beam","in_ms":0,"off_gcd":false,
 "queue":[{"spell":198013,"name":"Eye Beam","action":"eye_beam"},{"spell":370965,"name":"The Hunt","action":"the_hunt"},
          {"spell":258860,"name":"Essence Break","action":"essence_break"},{"spell":232893,"name":"Felblade","action":"felblade"}]}
```

`queue[0]` is the press now, the rest the look-ahead (`--next`, 4 for the overlay's icon row).
`spell` is the button's id (the in-Metamorphosis form when that is what to press: Death Sweep
210152, not Blade Dance), `name` its display name (a spell's `label`, else the action in title
case), `key` its keybind when the spell has a `key` (none are set yet), `in_ms` how long until it
can go out. How each combatfeed line is used:

| line | used for | replaces |
|---|---|---|
| `event` | everything in the table below, with `mine` / `me` as the source/dest test | — |
| `auras` `unit: player` | the player's auras with `remaining_ms` and `stacks` (a full snapshot: an aura missing from it is gone) | the event-based aura timers |
| `auras` `unit: target` | the player's (`mine`) debuffs on the selected target | same |
| `target` | the selected unit (`unit`, same names as the events' `dst`) | the swing/cast target |
| `cooldowns` | everything recovering (`spell`, or `category` + `spells`, with `charges`); absent = ready | see below |
| `power` type 17 | Fury | energizes minus costs |

The feed's cooldowns are the base tables plus the server's corrections, without talent effects, and
unhasted. So they are merged with the list's own timers, which know the talents (Metamorphosis'
resets, Eternal Hunt's reset, Cycle of Hatred's 20s Eye Beam) but only from casts they saw:

- a cooldown never cast since the overlay started: the feed's value;
- cast before the latest snapshot: whichever is ready sooner (talents only shorten);
- cast after the latest snapshot (or in the look-ahead): the own timer, the snapshot is stale for it.

`gcd_ms` is used only until the first cast; after that the own hasted GCD is. Between snapshots
the estimates from events carry on, so the rules' inputs are the same either way. Run combatfeed
with `-spelldata` so its list knows Havoc's cooldowns.

Tested on a real retail capture (a Vengeance DH on a dummy, 6,398 lines): every line passed through
byte for byte (16 suggestion changes added, no repeats in a queue), Fury followed the `power` lines, and the picks followed the casts. Vengeance shares
Immolation Aura, Throw Glaive and Felblade with Havoc, so that run checks the plumbing, not Havoc's
rules; `tests/havoc.rs` checks the rules on combatfeed-shaped lines.

## Replaying raw events

`--replay` reads one event a line, as Go's `json.Marshal(combatlog.Event)` writes it
(`TimeMs, Sub, Source, Dest, SpellID, Amount, School`; snake_case also works), and prints
`{"t", "fury", "enemies", "next": [3 presses]}` whenever the suggestion changes. Without
`--player` the player is whoever casts one of the list's spells or energizes themselves with Fury.
Tests: `cargo test --release --test havoc`.

## Format

Same rotation language as the specs (`[[rotation]]` `action` + `if`, `src/expr.rs`). The other
tables say where each name comes from:

| table | what | from the log |
|---|---|---|
| `[[spells]]` | button: cast `ids`, `cooldown` (`hasted`, `charges`, shared `group`), Fury `cost`, `gcd`, form (`requires`/`excludes` an aura), `on_cast` resets | `SPELL_CAST_SUCCESS` by the player |
| `[[auras]]` | buff (`on = "player"`) or debuff the player put on its target (`on = "target"`) | `SPELL_AURA_APPLIED/REFRESH` (Amount = remaining ms), `_APPLIED_DOSE/_REMOVED_DOSE` (Amount = stacks), `_REMOVED` |
| `[[markers]]` | state with no aura: set and cleared by the player's casts, optionally only while an aura is up (`ends_with`) | `SPELL_CAST_SUCCESS` |
| `resource` | Fury, power type 17 | `SPELL_ENERGIZE` / `SPELL_PERIODIC_ENERGIZE` to the player, minus each cast's `cost`; a `POWER_UPDATE` event (not CLEU: an exact SMSG_POWER_UPDATE reading, if the stream adds one) overwrites it |
| `active_enemies` | units the player damaged in the last `aoe_window` (3s) | `SWING_*`, `SPELL_DAMAGE`, `SPELL_PERIODIC_DAMAGE`, `SPELL_MISSED`; `UNIT_DIED` drops a unit |
| target | the unit the player swings at (else last cast target) | `SWING_DAMAGE` dest, `SPELL_CAST_SUCCESS` dest |
| `gcd`, haste | 1.5s / (1 + haste), 0.75s floor; haste = `--haste` (gear, set once) x Metamorphosis 20% x Furious Gaze 8% | the two haste auras |

A rule fires when its spell is off cooldown at the end of the current GCD, affordable, in the right
form, and its `if` holds. `recommend(n)` looks ahead by applying each pick to a copy of the state
(cost, cooldown, markers, plus the spell's `gain` and `applies`, which live play reports as events).

## Spell and aura ids (retail 12.1.5.70783)

| name | cast id(s) | aura id | cooldown in the list |
|---|---|---|---|
| Chaos Strike / Annihilation | 162794 / 201427 | — | — (40 Fury) |
| Blade Dance / Death Sweep | 188499 / 210152 | — | shared: 15s / 9s, hasted (35 Fury) |
| Eye Beam / Abyssal Gaze | 198013 / 452497 | — | shared 20s (30 Fury) |
| Immolation Aura / Consuming Fire | 258920 / 452487 | 258920 | shared 30s |
| Essence Break | 258860 | 320338 (on target) | 40s |
| Metamorphosis | 191427 | 162264 | 120s; resets Eye Beam, Blade Dance, Immolation Aura |
| The Hunt | 370965 | — | 60s |
| Felblade | 232893 | — | 12s |
| Vengeful Retreat | 198793 | Initiative 391215 | 25s, off the GCD |
| Throw Glaive | 185123 | — | 9s (25 Fury) |
| Furious Gaze | — | 343312 | |
| Demonsurge (stacking) | — | 452416 | |
| Eternal Hunt | — | 1271092 | Blade Dance/Death Sweep consume it and reset |
| Empowered Eye Beam | — | 1271144 | |
| Demon Blades (Fury) | energize 203796 | — | |

Ids checked by name against `SpellName` at build 12.1.5.70783; cooldowns, costs and durations from
the Spell tables (70575) via `tools/spelldb.py`. The in-Metamorphosis replacements come from
Metamorphosis 162264 effects #5/#6 and Demonsurge 452489 effects #1/#3.

## The priority: SimC's APL, generated

The rules are SimC's default Havoc action list (the top-DPS rotation), line for line, written into
the overlay by `tools/simc_apl.py` from `profiles/MID2_Demon_Hunter_Havoc.simc` (Python 3.11+):

```bash
python3 tools/simc_apl.py profiles/MID2_Demon_Hunter_Havoc.simc rotations/havoc_fel_scarred.toml
```

Each rule carries its SimC line as a comment. The translation is mechanical:
`action.X.demonsurge_available` is the overlay's Demonsurge marker for that button, `gcd.max` the
GCD, `buff.X.down` `!buff.X.up`, `(a>?b)` SimC's max, `variable.X` its precombat default, and the
auras and dots of talents this overlay doesn't track (Aldrachi Reaver, Burning Wound's dot count)
read 0. SimC's `immolation_aura` is two buttons here (Consuming Fire inside Metamorphosis). Lines
with no button (auto attack, retargeting, items, Reaver's Glaive, Arcane Torrent) are skipped.

**Talents are live.** `talent.X` is up while its passive aura (duration 0) is in the player's
aura list; the `[[talents]]` table maps names to ids and gives the SimC profile's build as the
default until a list arrives. Jack's build differs from the profile: no Eternal Hunt (The Hunt is
90s, SimC's line 44 then fires on cooldown), plus A Fire Inside and Burning Wound (SimC's lines 54
and 62 come in).

**Talent effects on cooldowns** the feed's base-table cooldowns don't have:

| effect | modelled as | evidence |
|---|---|---|
| Cycle of Hatred: Eye Beam / Abyssal Gaze -2.5s a stack | `stack_cooldown` on aura 1214887's stacks | DB2 258887 #0; stacks 1→3 live |
| A Fire Inside: Immolation Aura / Consuming Fire 2 charges, 24s | `talent_charges`, `talent_cooldown` | DB2 427775 #0, #2; a Consuming Fire 19s after the last |
| Eternal Hunt: The Hunt 60s | `talent_cooldown` | DB2 1270898 |
| Chaotic / Violent Transformation, Eternal Hunt resets | `on_cast` resets | the spec |

**Haste** comes from the Eye Beam / Abyssal Gaze channel aura (198013 / 452497): its
`duration_ms` is 2000 / (1 + gear haste). Live: 1,692 ms in and out of Metamorphosis, so 18.2%.
Metamorphosis (+20%) and Furious Gaze (+8%) then hasten the GCD and Blade Dance / Death Sweep on
top, which matches Jack's Death Sweeps 6.2-7s apart. Until the first channel, `--haste` (default 0).

**Off-GCD presses** (Vengeful Retreat) are not in `queue`; they go in the line's `off_gcd` list.

## Against Jack's Havoc recording

`wow-rotation rotations/havoc_fel_scarred.toml --audit --next 4 < feed.jsonl` prints, for every press
of a listed spell, what the overlay said just before it. On 2026-11-05 03:28 (49s of combat, 39
presses): 18 were the overlay's queue[0], 13 elsewhere in its queue, and 2 came while it thought
the spell was on cooldown (8 before the APL, haste and talent work above). Jack isn't ground truth:
SimC's line 50 wants Essence Break as soon as a Demonsurge window opens, he held it 12s.

**Shared categories.** Blade Dance and Death Sweep share cooldown category 1640 with their own
lengths (15s / 9s, both hasted): one start, each member back after its own length, so Death
Sweep is ready 9s after either. combatfeed sends such a category as one entry per length (an
entry with `spell` is that spell's own). With that, the remaining "early" presses on the recording
are a Death Sweep 0.6s before the own timer (at the pull, before the first Eye Beam taught the
overlay Jack's haste; `--haste 0.18` covers it) and one Throw Glaive.

## What the demo and tests show

`--demo` is a closed loop: a stand-in game presses whatever the overlay says and reports it back
only as log events (casts, auras with remaining time, energizes, swings, damage on each target).
At 17% haste the 1-target opener is Eye Beam (Demonic) → The Hunt → Essence Break → empowered
Death Sweep, and Death Sweep again (Eternal Hunt reset it) → Vengeful Retreat + Felblade →
Annihilation → hardcast Metamorphosis once the window is spent → the new empowered Death Sweep and
Annihilation → Abyssal Gaze → its empowered spenders, then Vengeful Retreat just before the next
Eye Beam at 31s; with 4 targets Consuming Fire moves above Annihilation. The stand-in's Fury income is
only 15 a swing plus Felblade and Immolation Aura (no Tactical Retreat, Student of Suffering,
soul fragments or refunds), so it is starved compared with real play.

`tests/havoc.rs` builds state from events only and checks: the opener; The Hunt then
Essence Break then Death Sweep/Annihilation in the Demonic window, read from the target's
320338 debuff; the hardcast Metamorphosis after the window and the Chaotic Transformation reset;
Felblade when starved; Immolation Aura above Chaos Strike at 3 targets (and back once they leave
the 3s window); and a 120s loop with no unaffordable press and no idle gap over 4s.

## Known gaps

- **Without combatfeed's `cooldowns` lines, cooldowns are timers from the cast.** Cycle of
  Hatred's per-stack cuts (and the tier 4pc's Essence Break cuts) are then not followed: Eye Beam
  uses the spec's 20s steady state, so the first Eye Beam after the pull is predicted ready ~10s
  early. A cast always restarts the timer, so the error never carries over.
- **Fury between `power` lines** is integrated from energizes minus costs.
- **The look-ahead only moves when a line arrives.** A cooldown coming up between lines shows on the
  next line (combatfeed sends aura and cooldown changes as they happen, so this is short in combat).
- **Haste before the first Eye Beam** is `--haste` (default 0).
- **Aura durations** come from the event's remaining time (wow-forever has it); events without it
  fall back to the table's `duration`.
- **Talent choices** are fixed to the profile's build; another build needs its own overlay file.
