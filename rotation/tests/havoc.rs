//! The Havoc overlay picks sensible next presses from combat-log events alone.
use wow_rotation::rotation::{Demo, LogEvent, Overlay, Tracker};

const ME: &str = "Player-1";
const BOSS: &str = "Creature-1";
const FURY: i64 = 17;

fn overlay() -> Overlay {
    Overlay::load(concat!(env!("CARGO_MANIFEST_DIR"), "/rotations/havoc_fel_scarred.toml")).unwrap()
}

/// A tracker that has seen the pull: the player swung at the boss with `fury` Fury.
fn pulled(o: &Overlay, fury: f64) -> Tracker<'_> {
    let mut tr = Tracker::new(o);
    tr.player = Some(ME.into());
    tr.ingest(&LogEvent::new(0.0, "SWING_DAMAGE", ME, BOSS, 0, 1000.0, 1));
    tr.ingest(&LogEvent::new(0.0, "POWER_UPDATE", ME, ME, 0, fury, FURY));
    tr
}

fn cast(tr: &mut Tracker, t: f64, id: i64) {
    tr.ingest(&LogEvent::new(t, "SPELL_CAST_SUCCESS", ME, BOSS, id, 0.0, 1));
}
fn aura(tr: &mut Tracker, t: f64, id: i64, secs: f64) {
    tr.ingest(&LogEvent::new(t, "SPELL_AURA_APPLIED", ME, ME, id, secs * 1000.0, 1));
}
fn energize(tr: &mut Tracker, t: f64, amount: f64) {
    tr.ingest(&LogEvent::new(t, "SPELL_ENERGIZE", ME, ME, 203796, amount, FURY));
}
/// The next GCD press (off-GCD picks such as Vengeful Retreat are woven in between).
fn next(tr: &Tracker) -> String {
    tr.recommend(3).into_iter().find(|p| tr.o.spec.spells[tr.o.spell_index(&p.action).unwrap()].gcd != Some(0.0)).unwrap().action
}

const EYE_BEAM: i64 = 198013;
const DEATH_SWEEP: i64 = 210152;
const BLADE_DANCE: i64 = 188499;
const META_CAST: i64 = 191427;
const META_BUFF: i64 = 162264;
const ESSENCE_BREAK: i64 = 258860;
const ANNIHILATION: i64 = 201427;
const FELBLADE: i64 = 232893;
const IMMOLATION: i64 = 258920;

#[test]
fn every_rule_parses_and_names_real_ids() {
    let o = overlay();
    assert!(o.spec.spells.iter().all(|s| !s.ids.is_empty()));
    assert!(o.spec.auras.iter().all(|a| !a.ids.is_empty()));
    assert_eq!(o.spell_by_id(452497).map(|i| o.spell_name(i)), Some("abyssal_gaze"));
    assert_eq!(o.spell_by_id(452487).map(|i| o.spell_name(i)), Some("consuming_fire"));
}

#[test]
fn opener_is_eye_beam_into_the_hunt() {
    let o = overlay();
    let tr = pulled(&o, 50.0);
    let q: Vec<_> = tr.recommend(3).into_iter().map(|p| p.action).collect();
    // Eye Beam first (Demonic opens Metamorphosis), The Hunt once Eye Beam is down and
    // Metamorphosis is still up [simc line 44], then Essence Break for the Demonsurge window.
    assert_eq!(q, ["eye_beam", "the_hunt", "essence_break"]);
}

#[test]
fn demonic_window_spends_the_empowerments() {
    let o = overlay();
    let mut tr = pulled(&o, 80.0);
    cast(&mut tr, 0.0, EYE_BEAM);
    aura(&mut tr, 0.0, META_BUFF, 6.9); // Demonic
    tr.advance(1.7);
    assert_eq!(next(&tr), "the_hunt", "[simc line 44] Eye Beam down, Metamorphosis ready");
    cast(&mut tr, 1.7, 370965);
    tr.advance(1.8);
    assert_eq!(next(&tr), "essence_break");
    cast(&mut tr, 1.8, ESSENCE_BREAK);
    tr.ingest(&LogEvent::new(1.9, "SPELL_AURA_APPLIED", ME, BOSS, 320338, 4000.0, 1));
    tr.advance(2.9);
    assert!(tr.aura_remains("essence_break") > 2.0, "debuff read from the target");
    assert_eq!(next(&tr), "death_sweep");
    cast(&mut tr, 2.9, DEATH_SWEEP);
    energize(&mut tr, 3.0, 40.0);
    tr.advance(3.9);
    assert_eq!(next(&tr), "annihilation");
    cast(&mut tr, 3.9, ANNIHILATION);
    tr.advance(4.9);
    // Both empowerments are spent, Death Sweep holds the shared Blade Dance cooldown and Eye Beam is
    // 15s off: hardcast Metamorphosis now, extending the Demonic one [simc line 42].
    assert_eq!(next(&tr), "metamorphosis");
}

#[test]
fn metamorphosis_hardcast_after_the_demonic_window() {
    let o = overlay();
    let mut tr = pulled(&o, 60.0);
    cast(&mut tr, 0.0, EYE_BEAM);
    aura(&mut tr, 0.0, META_BUFF, 6.9);
    cast(&mut tr, 1.7, 370965); // The Hunt
    cast(&mut tr, 2.0, DEATH_SWEEP);
    cast(&mut tr, 3.0, ANNIHILATION);
    tr.ingest(&LogEvent::new(6.9, "SPELL_AURA_REMOVED", ME, ME, META_BUFF, 0.0, 1));
    tr.advance(7.0);
    // Eye Beam is ~13s off, Blade Dance shares Death Sweep's cooldown: Metamorphosis [simc line 42].
    assert_eq!(next(&tr), "metamorphosis");
    cast(&mut tr, 7.0, META_CAST);
    aura(&mut tr, 7.0, META_BUFF, 20.0);
    energize(&mut tr, 7.5, 90.0);
    tr.advance(8.0);
    // Chaotic Transformation reset Eye Beam and Blade Dance; the four Demonsurge empowerments
    // are up, and Essence Break opens them [simc line 50].
    assert_eq!(tr.cooldown_remains("eye_beam"), 0.0);
    assert_eq!(next(&tr), "essence_break");
    let q: Vec<_> = tr.recommend(5).into_iter().map(|p| p.action).filter(|a| a != "vengeful_retreat").collect();
    assert_eq!(&q[1..3], ["death_sweep", "annihilation"]);
}

#[test]
fn low_fury_builds_with_felblade() {
    let o = overlay();
    let mut tr = pulled(&o, 20.0);
    // Meta first: its cast resets Eye Beam, Blade Dance and Immolation Aura. Then everything else down.
    cast(&mut tr, 0.0, META_CAST);
    cast(&mut tr, 0.0, EYE_BEAM);
    cast(&mut tr, 0.0, 370965);
    cast(&mut tr, 0.0, 198793);
    cast(&mut tr, 0.0, ESSENCE_BREAK);
    cast(&mut tr, 0.0, BLADE_DANCE);
    cast(&mut tr, 0.0, IMMOLATION);
    tr.ingest(&LogEvent::new(0.0, "POWER_UPDATE", ME, ME, 0, 20.0, FURY));
    tr.advance(3.0);
    assert_eq!(next(&tr), "felblade");
    cast(&mut tr, 3.0, FELBLADE);
    energize(&mut tr, 3.0, 15.0);
    energize(&mut tr, 3.3, 15.0);
    tr.advance(3.6);
    assert_eq!(next(&tr), "chaos_strike");
}

#[test]
fn aoe_puts_immolation_aura_above_chaos_strike() {
    let o = overlay();
    let mut tr = pulled(&o, 60.0);
    for id in [META_CAST, EYE_BEAM, 370965, ESSENCE_BREAK, BLADE_DANCE, FELBLADE, 198793] { cast(&mut tr, 0.0, id); }
    tr.ingest(&LogEvent::new(0.0, "POWER_UPDATE", ME, ME, 0, 60.0, FURY));
    tr.advance(2.0);
    assert_eq!(tr.active_enemies(), 1);
    assert_eq!(next(&tr), "chaos_strike");
    for u in ["Creature-2", "Creature-3"] {
        tr.ingest(&LogEvent::new(2.0, "SPELL_DAMAGE", ME, u, BLADE_DANCE, 5000.0, 1));
    }
    assert_eq!(tr.active_enemies(), 3);
    // SimC line 62 needs A Fire Inside or Burning Wound: not in the profile's build...
    assert_eq!(next(&tr), "chaos_strike");
    // ...but in Jack's, read live from the passive auras in the player's aura list
    tr.feed_line(&serde_json::from_str(r#"{"type":"auras","t":2000,"unit":"player","auras":[{"spell":391189,"name":"Burning Wound","stacks":0,"duration_ms":0,"remaining_ms":0,"mine":true},{"spell":388112,"stacks":0,"duration_ms":0,"remaining_ms":0,"mine":true}]}"#).unwrap());
    assert_eq!(next(&tr), "immolation_aura");
    // the extra targets drop out of the window
    tr.advance(6.0);
    assert_eq!(tr.active_enemies(), 1);
}

#[test]
fn closed_loop_fight_is_sensible() {
    let o = overlay();
    let extra = [("eye_beam", "eternal_hunt", 2.0, 8.0), ("eye_beam", "demonsurge", 0.7, 15.0), ("metamorphosis", "demonsurge", 0.7, 15.0)];
    let d = Demo::run(&o, 120.0, 1, 40.0, 1.3, 15.0, &extra);
    let n = |a| d.count(a);
    assert!(n("eye_beam") + n("abyssal_gaze") >= 5, "Eye Beam family on cooldown");
    assert!(n("metamorphosis") >= 1 && n("the_hunt") >= 2);
    assert!(n("essence_break") >= 3);
    // never a press the game would refuse for Fury
    for (t, a, fury, _) in &d.presses {
        let s = &o.spec.spells[o.spell_index(a).unwrap()];
        assert!(*fury >= s.cost, "{a} at {t:.2}s with {fury} fury");
    }
    // no long idle stretches
    let gaps = d.presses.windows(2).map(|w| w[1].0 - w[0].0).fold(0.0, f64::max);
    assert!(gaps < 4.0, "longest gap {gaps:.1}s");
}

/// combatfeed lines (Ltz-lab/wow-feed tools/combatfeed): snapshots win over the estimates.
#[test]
fn combatfeed_snapshots_drive_the_picks() {
    let o = overlay();
    let mut tr = Tracker::new(&o);
    let feed = |tr: &mut Tracker, s: &str| tr.feed_line(&serde_json::from_str(s).unwrap());
    feed(&mut tr, r#"{"type":"player","guid":"0839","t":0}"#);
    feed(&mut tr, r#"{"type":"event","t":100,"sub":"SWING_DAMAGE","src":"Player-7","dst":"Creature-1","amount":900,"mine":true,"me":false}"#);
    feed(&mut tr, r#"{"type":"power","t":100,"powers":[{"type":17,"name":"fury","amount":60}]}"#);
    assert_eq!(tr.resource, 60.0);
    assert_eq!(tr.next_line(3).unwrap()["action"], "eye_beam");
    // the server says Eye Beam is recovering (Cycle of Hatred included): not Eye Beam
    feed(&mut tr, r#"{"type":"cooldowns","t":200,"gcd_ms":0,"cooldowns":[{"spell":198013,"remaining_ms":9000},{"spell":191427,"remaining_ms":60000},{"spell":370965,"remaining_ms":30000}]}"#);
    assert!(tr.cooldown_remains("eye_beam") > 8.0);
    assert_eq!(tr.next_line(3).unwrap()["action"], "essence_break");
    // Metamorphosis from the player's aura snapshot, Essence Break mine on the target
    feed(&mut tr, r#"{"type":"event","t":300,"sub":"SPELL_CAST_SUCCESS","src":"Player-7","dst":"Creature-1","spell":258860,"mine":true,"me":false}"#);
    feed(&mut tr, r#"{"type":"auras","t":300,"unit":"player","auras":[{"spell":162264,"stacks":0,"duration_ms":20000,"remaining_ms":15000,"mine":true}]}"#);
    feed(&mut tr, r#"{"type":"auras","t":300,"unit":"target","auras":[{"spell":98892,"stacks":0,"duration_ms":0,"remaining_ms":0,"mine":false},{"spell":320338,"stacks":0,"duration_ms":4000,"remaining_ms":4000,"mine":true}]}"#);
    feed(&mut tr, r#"{"type":"cooldowns","t":300,"gcd_ms":1200,"cooldowns":[{"spell":198013,"remaining_ms":8900},{"spell":258860,"remaining_ms":40000},{"spell":191427,"remaining_ms":59900},{"spell":370965,"remaining_ms":29900}]}"#);
    let n = tr.next_line(3).unwrap();
    assert_eq!(n["name"], "Death Sweep");
    assert_eq!(n["spell"], 210152);
    assert_eq!(n["queue"][0]["spell"], 210152, "queue[0] is the press now");
    assert_eq!(n["queue"].as_array().unwrap().len(), 3);
    // the feed's GCD is unhasted, so the own one (from the Essence Break cast) is used
    assert_eq!(n["in_ms"], 1500.0);
    // the Metamorphosis aura leaves the snapshot: back to the base forms
    feed(&mut tr, r#"{"type":"auras","t":400,"unit":"player","auras":[]}"#);
    assert_eq!(tr.next_line(1).unwrap()["action"], "blade_dance");
}

/// The feed's cooldowns carry no talent effects: the own timers' resets and cuts still count.
#[test]
fn talents_apply_over_the_feeds_cooldowns() {
    let o = overlay();
    let mut tr = Tracker::new(&o);
    let feed = |tr: &mut Tracker, s: &str| tr.feed_line(&serde_json::from_str(s).unwrap());
    feed(&mut tr, r#"{"type":"target","guid":"x","unit":"Creature-1","t":0}"#);
    feed(&mut tr, r#"{"type":"power","t":0,"powers":[{"type":17,"name":"fury","amount":100}]}"#);
    feed(&mut tr, r#"{"type":"auras","t":0,"unit":"player","auras":[{"spell":1214887,"stacks":4,"duration_ms":0,"remaining_ms":0,"mine":true}]}"#);
    feed(&mut tr, r#"{"type":"event","t":0,"sub":"SPELL_CAST_SUCCESS","src":"Player-7","dst":"Creature-1","spell":198013,"mine":true,"me":false}"#);
    feed(&mut tr, r#"{"type":"cooldowns","t":0,"gcd_ms":1500,"cooldowns":[{"spell":198013,"remaining_ms":30000}]}"#);
    // Cycle of Hatred at 4 stacks: 20s beats the feed's base 30s
    tr.advance(1.0);
    assert!((tr.cooldown_remains("eye_beam") - 19.0).abs() < 1e-6);
    // Metamorphosis' Chaotic Transformation resets Eye Beam; the feed still lists it
    feed(&mut tr, r#"{"type":"event","t":3000,"sub":"SPELL_CAST_SUCCESS","src":"Player-7","dst":"Creature-1","spell":191427,"mine":true,"me":false}"#);
    feed(&mut tr, r#"{"type":"cooldowns","t":3000,"gcd_ms":1500,"cooldowns":[{"spell":198013,"remaining_ms":27000},{"spell":191427,"remaining_ms":120000}]}"#);
    assert_eq!(tr.cooldown_remains("eye_beam"), 0.0);
    assert!(tr.cooldown_remains("metamorphosis") > 100.0);
    // a cooldown the overlay never saw cast is the feed's word
    feed(&mut tr, r#"{"type":"cooldowns","t":3100,"gcd_ms":0,"cooldowns":[{"spell":191427,"remaining_ms":119900},{"category":1,"spells":[232893],"remaining_ms":5000}]}"#);
    assert!((tr.cooldown_remains("felblade") - 5.0).abs() < 1e-6);
    // a cast after the last snapshot: the snapshot is stale for it, the own timer holds
    feed(&mut tr, r#"{"type":"event","t":3200,"sub":"SPELL_CAST_SUCCESS","src":"Player-7","dst":"Creature-1","spell":258860,"mine":true,"me":false}"#);
    assert!(tr.cooldown_remains("essence_break") > 39.0);
}

/// Haste from the Eye Beam channel aura; Cycle of Hatred and A Fire Inside from the aura list.
#[test]
fn haste_and_talent_cooldowns_from_the_aura_list() {
    let o = overlay();
    let mut tr = pulled(&o, 80.0);
    let auras = |list: &str| serde_json::from_str::<serde_json::Value>(&format!(r#"{{"type":"auras","t":0,"unit":"player","auras":[{list}]}}"#)).unwrap();
    // Jack's live numbers: a 1,692 ms channel is 18.2% haste
    tr.feed_line(&auras(r#"{"spell":198013,"stacks":0,"duration_ms":1692,"remaining_ms":1692,"mine":true},{"spell":427775,"stacks":0,"duration_ms":0,"remaining_ms":0,"mine":true},{"spell":1214887,"stacks":2,"duration_ms":0,"remaining_ms":0,"mine":true}"#));
    assert!((tr.haste - 0.182).abs() < 0.001, "{}", tr.haste);
    cast(&mut tr, 0.0, EYE_BEAM);
    assert!((tr.cooldown_remains("eye_beam") - 25.0).abs() < 1e-6, "30s - 2 x 2.5s");
    cast(&mut tr, 0.0, IMMOLATION);
    assert_eq!(tr.cooldown_remains("immolation_aura"), 0.0, "A Fire Inside: a second charge");
    cast(&mut tr, 0.1, IMMOLATION);
    assert!((tr.cooldown_remains("immolation_aura") - 23.9).abs() < 1e-6, "24s recharge");
}

/// Blade Dance and Death Sweep share one category start with their own lengths (15s / 9s):
/// Death Sweep is back 9s (hasted) after either.
#[test]
fn blade_dance_and_death_sweep_share_a_start_not_a_length() {
    let o = overlay();
    let mut tr = pulled(&o, 80.0);
    cast(&mut tr, 0.0, BLADE_DANCE);
    tr.advance(1.0);
    assert!((tr.cooldown_remains("blade_dance") - 14.0).abs() < 1e-6);
    assert!((tr.cooldown_remains("death_sweep") - 8.0).abs() < 1e-6);
    cast(&mut tr, 9.0, DEATH_SWEEP);
    tr.advance(10.0);
    assert!((tr.cooldown_remains("blade_dance") - 14.0).abs() < 1e-6, "a Death Sweep restarts the category");
    // the feed's per-length entries: Death Sweep's own beside the category's
    let feed = |tr: &mut Tracker, s: &str| tr.feed_line(&serde_json::from_str(s).unwrap());
    feed(&mut tr, r#"{"type":"cooldowns","t":10000,"gcd_ms":0,"cooldowns":[{"category":1640,"spells":[188499],"remaining_ms":14000},{"category":1640,"spell":210152,"remaining_ms":8000}]}"#);
    assert!((tr.cooldown_remains("death_sweep") - 8.0).abs() < 1e-6);
    assert!((tr.cooldown_remains("blade_dance") - 14.0).abs() < 1e-6);
}
