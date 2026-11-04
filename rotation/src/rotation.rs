//! Live rotation overlay: a Hekili-style "press this next" from combat-log events.
//!
//! A rotation file (`rotations/*.toml`) is a priority list in the same rotation language as the
//! specs (`[[rotation]]` action + `if`, see `expr.rs`), plus the tables that turn a combat-log
//! stream into the state those conditions read: which spell ids are which button and cooldown,
//! which aura ids are which buff, and "markers" kept from the player's own casts for state the
//! log has no aura for. Nothing here is class-specific; the class lives in the TOML.
//!
//! Events follow wow-forever's `protocol/combatlog.Event` (TimeMs, Sub, Source, Dest, SpellID,
//! Amount, School), which mirrors COMBAT_LOG_EVENT_UNFILTERED. HAVOC.md has the format.

use crate::expr::{self, Env, Expr, Resolver, Var};
use serde::Deserialize;
use std::collections::HashMap;

fn one() -> u32 { 1 }
fn gcd_default() -> f64 { 1.5 }
fn window_default() -> f64 { 3.0 }

#[derive(Debug, Clone, Deserialize)]
pub struct ResourceDef {
    pub name: String,
    pub power_type: i64,
    pub max: f64,
}

#[derive(Debug, Clone, Deserialize)]
pub struct CastEffect {
    /// Only when this aura is up at the cast (it is spent by the cast).
    #[serde(default)]
    pub if_aura: Option<String>,
    /// Cooldown (spell or group name) to reset.
    #[serde(default)]
    pub reset: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SpellDef {
    pub name: String,
    pub ids: Vec<i64>,
    #[serde(default)]
    pub group: Option<String>,
    #[serde(default)]
    pub cooldown: f64,
    #[serde(default)]
    pub hasted: bool,
    #[serde(default = "one")]
    pub charges: u32,
    #[serde(default)]
    pub cost: f64,
    /// Resource the cast gives: look-ahead only (live, the log's energize events carry it).
    #[serde(default)]
    pub gain: f64,
    /// GCD (or channel) the cast locks, hasted; None = the overlay's `gcd`; 0 = off the GCD.
    #[serde(default)]
    pub gcd: Option<f64>,
    #[serde(default)]
    pub requires: Option<String>,
    #[serde(default)]
    pub excludes: Option<String>,
    #[serde(default)]
    pub on_cast: Vec<CastEffect>,
    /// Auras the cast puts up (name -> seconds): look-ahead only.
    #[serde(default)]
    pub applies: HashMap<String, f64>,
    /// Name to show (default: the action name in title case).
    #[serde(default)]
    pub label: Option<String>,
    /// Cooldown with a talent (talent name -> seconds), else `cooldown`.
    #[serde(default)]
    pub talent_cooldown: HashMap<String, f64>,
    /// Charges with a talent (talent name -> charges), else `charges`.
    #[serde(default)]
    pub talent_charges: HashMap<String, u32>,
    /// Seconds added to the cooldown per stack of an aura at the cast (Cycle of Hatred: -2.5).
    #[serde(default)]
    pub stack_cooldown: Option<StackCooldown>,
    /// Keybind to draw on the icon, if known.
    #[serde(default)]
    pub key: Option<String>,
}

impl SpellDef {
    pub fn label(&self) -> String {
        self.label.clone().unwrap_or_else(|| {
            self.name.split('_').map(|w| {
                let mut c = w.chars();
                c.next().map_or(String::new(), |f| f.to_uppercase().chain(c).collect())
            }).collect::<Vec<_>>().join(" ")
        })
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct AuraDef {
    pub name: String,
    pub ids: Vec<i64>,
    /// "player" (default) or "target": cast by the player on the unit it fights.
    #[serde(default)]
    pub on: Option<String>,
    pub duration: f64,
    /// A hasted channel's aura: its base length, so base / observed duration = 1 + gear haste.
    #[serde(default)]
    pub haste_probe: Option<f64>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct StackCooldown {
    pub aura: String,
    pub seconds: f64,
}

/// A talent, known live from its passive aura in the player's aura list.
#[derive(Debug, Clone, Deserialize)]
pub struct TalentDef {
    pub name: String,
    pub ids: Vec<i64>,
    /// Taken until the first aura list that shows any talent arrives.
    #[serde(default)]
    pub default: bool,
}

#[derive(Debug, Clone, Deserialize)]
pub struct MarkerDef {
    pub name: String,
    #[serde(default)]
    pub set_by: Vec<String>,
    #[serde(default)]
    pub cleared_by: Vec<String>,
    /// The marker only counts while this aura is up, and is dropped when it ends.
    #[serde(default)]
    pub ends_with: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Entry {
    pub action: String,
    #[serde(rename = "if", default)]
    pub cond: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct OverlaySpec {
    pub name: String,
    pub resource: ResourceDef,
    #[serde(default = "gcd_default")]
    pub gcd: f64,
    #[serde(default = "window_default")]
    pub aoe_window: f64,
    #[serde(default)]
    pub haste: f64,
    #[serde(default)]
    pub haste_auras: HashMap<String, f64>,
    pub spells: Vec<SpellDef>,
    #[serde(default)]
    pub auras: Vec<AuraDef>,
    #[serde(default)]
    pub markers: Vec<MarkerDef>,
    #[serde(default)]
    pub talents: Vec<TalentDef>,
    pub rotation: Vec<Entry>,
}

/// A compiled overlay: names resolved to indices, conditions parsed.
#[derive(Debug)]
pub struct Overlay {
    pub spec: OverlaySpec,
    groups: Vec<(String, u32)>,       // cooldown groups: name, charges
    spell_group: Vec<Option<usize>>,  // per spell
    spell_by_name: HashMap<String, usize>,
    spell_by_id: HashMap<i64, usize>,
    aura_by_id: HashMap<i64, usize>,
    slot_by_name: HashMap<String, usize>, // auras, then markers
    on_target: Vec<bool>,                 // per aura
    marker_end: Vec<Option<usize>>,       // per marker: aura slot
    haste_slots: Vec<(usize, f64)>,
    talent_slots: Vec<(usize, bool)>,
    talent_ids: std::collections::HashSet<i64>,
    rules: Vec<(usize, Option<Expr>)>,
}

impl Overlay {
    pub fn from_toml(text: &str) -> Result<Overlay, String> {
        let spec: OverlaySpec = toml::from_str(text).map_err(|e| e.to_string())?;
        Overlay::new(spec)
    }

    pub fn load(path: &str) -> Result<Overlay, String> {
        let text = std::fs::read_to_string(path).map_err(|e| format!("{path}: {e}"))?;
        Overlay::from_toml(&text).map_err(|e| format!("{path}: {e}"))
    }

    pub fn new(spec: OverlaySpec) -> Result<Overlay, String> {
        let mut o = Overlay {
            groups: vec![], spell_group: vec![], spell_by_name: HashMap::new(), spell_by_id: HashMap::new(),
            aura_by_id: HashMap::new(), slot_by_name: HashMap::new(), on_target: vec![], marker_end: vec![],
            haste_slots: vec![], talent_slots: vec![], talent_ids: Default::default(), rules: vec![], spec: spec.clone(),
        };
        // talents are player auras that never run out
        let mut spec = spec;
        for t in &spec.talents {
            spec.auras.push(AuraDef { name: format!("talent:{}", t.name), ids: t.ids.clone(), on: None, duration: 1e9, haste_probe: None });
        }
        o.spec.auras = spec.auras.clone();
        for (i, a) in spec.auras.iter().enumerate() {
            o.slot_by_name.insert(a.name.clone(), i);
            o.on_target.push(match a.on.as_deref() {
                None | Some("player") => false,
                Some("target") => true,
                Some(x) => return Err(format!("aura {}: on = '{x}' (player or target)", a.name)),
            });
            for &id in &a.ids { o.aura_by_id.insert(id, i); }
        }
        let aura = |o: &Overlay, n: &str, what: &str| -> Result<usize, String> {
            o.slot_by_name.get(n).copied().filter(|&s| s < spec.auras.len()).ok_or(format!("{what}: no aura '{n}'"))
        };
        for (j, m) in spec.markers.iter().enumerate() {
            o.slot_by_name.insert(m.name.clone(), spec.auras.len() + j);
            let end = match &m.ends_with { Some(n) => Some(aura(&o, n, &m.name)?), None => None };
            o.marker_end.push(end);
        }
        for (i, s) in spec.spells.iter().enumerate() {
            o.spell_by_name.insert(s.name.clone(), i);
            for &id in &s.ids { o.spell_by_id.insert(id, i); }
            let g = if s.cooldown > 0.0 || s.group.is_some() {
                let gname = s.group.clone().unwrap_or_else(|| s.name.clone());
                Some(match o.groups.iter().position(|(n, _)| *n == gname) {
                    Some(g) => g,
                    None => { o.groups.push((gname, s.charges)); o.groups.len() - 1 }
                })
            } else { None };
            o.spell_group.push(g);
        }
        for s in &spec.spells {
            for n in s.requires.iter().chain(&s.excludes).chain(s.applies.keys()) { aura(&o, n, &s.name)?; }
            for e in &s.on_cast {
                if let Some(n) = &e.if_aura { aura(&o, n, &s.name)?; }
                if let Some(n) = &e.reset { o.group_of(n).ok_or(format!("{}: no cooldown '{n}'", s.name))?; }
            }
        }
        for m in &spec.markers {
            for n in m.set_by.iter().chain(&m.cleared_by) {
                if !o.spell_by_name.contains_key(n) { return Err(format!("marker {}: no spell '{n}'", m.name)); }
            }
        }
        for t in &spec.talents {
            o.talent_slots.push((o.slot_by_name[&format!("talent:{}", t.name)], t.default));
            o.talent_ids.extend(&t.ids);
        }
        for s in &spec.spells {
            if let Some(sc) = &s.stack_cooldown { aura(&o, &sc.aura, &s.name)?; }
            for n in s.talent_cooldown.keys().chain(s.talent_charges.keys()) {
                if !o.slot_by_name.contains_key(&format!("talent:{n}")) { return Err(format!("{}: no talent '{n}'", s.name)); }
            }
        }
        for (n, &h) in &spec.haste_auras { o.haste_slots.push((aura(&o, n, "haste_auras")?, h)); }
        for e in &spec.rotation {
            let i = *o.spell_by_name.get(&e.action).ok_or(format!("rotation: no spell '{}'", e.action))?;
            let c = match &e.cond { Some(c) => Some(expr::parse(c, &o).map_err(|err| format!("{}: {err}", e.action))?), None => None };
            o.rules.push((i, c));
        }
        Ok(o)
    }

    /// The spell whose cooldown `cooldown.<name>` reads: the spell itself, or a group's first.
    fn cd_spell(&self, name: &str) -> Option<usize> {
        let g = self.group_of(name)?;
        self.spell_by_name.get(name).copied().filter(|&i| self.spell_group[i] == Some(g))
            .or_else(|| self.spell_group.iter().position(|&x| x == Some(g)))
    }

    fn in_group(&self, g: usize) -> impl Iterator<Item = usize> + '_ {
        (0..self.spell_group.len()).filter(move |&j| self.spell_group[j] == Some(g))
    }

    fn group_of(&self, name: &str) -> Option<usize> {
        if let Some(&i) = self.spell_by_name.get(name) {
            if let Some(g) = self.spell_group[i] { return Some(g); }
        }
        self.groups.iter().position(|(n, _)| n == name)
    }

    pub fn spell_name(&self, i: usize) -> &str { &self.spec.spells[i].name }
    pub fn spell_index(&self, name: &str) -> Option<usize> { self.spell_by_name.get(name).copied() }
    pub fn spell_by_id(&self, id: i64) -> Option<usize> { self.spell_by_id.get(&id).copied() }
}

impl Resolver for Overlay {
    fn resolve(&self, name: &str) -> Result<Var, String> {
        let r = &self.spec.resource.name;
        let parts: Vec<&str> = name.split('.').collect();
        Ok(match parts.as_slice() {
            [x] if x == r => Var::Resource(0),
            [x, "max"] if x == r => Var::ResourceMax(0),
            ["gcd"] | ["gcd", "max"] => Var::Gcd,
            ["gcd", "remains"] => Var::GcdRemains,
            ["time"] => Var::Time,
            ["active_enemies"] | ["spell_targets"] => Var::ActiveEnemies,
            ["cooldown", a, f] => {
                let g = self.cd_spell(a).ok_or(format!("no cooldown '{a}'"))?;
                match *f {
                    "ready" | "up" => Var::CdReady(g),
                    "remains" => Var::CdRemains(g),
                    "charges" => Var::CdCharges(g),
                    "full_recharge_time" => Var::CdFullRecharge(g),
                    _ => return Err(format!("unknown cooldown field '{f}'")),
                }
            }
            ["talent" | "hero_tree", a] | ["talent", a, "enabled"] => {
                Var::BuffUp(*self.slot_by_name.get(&format!("talent:{a}")).ok_or(format!("no talent '{a}'"))?)
            }
            ["buff" | "debuff", a, f] => {
                let s = *self.slot_by_name.get(*a).ok_or(format!("no aura or marker '{a}'"))?;
                match *f {
                    "up" => Var::BuffUp(s),
                    "down" => return Err("use !buff.X.up".into()),
                    "remains" => Var::BuffRemains(s),
                    "stack" | "stacks" => Var::BuffStack(s),
                    _ => return Err(format!("unknown buff field '{f}'")),
                }
            }
            _ => return Err(format!("unknown identifier '{name}'")),
        })
    }
}

/// One combat-log event, as wow-forever's `combatlog.Event` serialises it (or snake_case).
#[derive(Debug, Clone, Deserialize)]
pub struct LogEvent {
    #[serde(alias = "TimeMs")]
    pub time_ms: f64,
    #[serde(alias = "Sub")]
    pub sub: String,
    #[serde(alias = "Source", default)]
    pub source: serde_json::Value,
    #[serde(alias = "Dest", default)]
    pub dest: serde_json::Value,
    #[serde(alias = "SpellID", default)]
    pub spell_id: i64,
    /// Damage / energize amount; stacks for doses; remaining ms for aura applied/refresh.
    #[serde(alias = "Amount", default)]
    pub amount: f64,
    /// School mask; the power type for SPELL_ENERGIZE.
    #[serde(alias = "School", default)]
    pub school: i64,
}

impl LogEvent {
    pub fn new(t: f64, sub: &str, src: &str, dst: &str, spell: i64, amount: f64, school: i64) -> LogEvent {
        LogEvent {
            time_ms: t * 1000.0, sub: sub.into(), source: src.into(), dest: dst.into(), spell_id: spell, amount, school,
        }
    }
}

fn guid(v: &serde_json::Value) -> String {
    match v {
        serde_json::Value::Null => String::new(),
        serde_json::Value::String(s) => s.clone(),
        x => x.to_string(),
    }
}

#[derive(Debug, Clone, Copy, Default)]
struct AuraState { exp: f64, stacks: u32 }

/// The live state, kept from events. Clone it to look ahead.
#[derive(Debug, Clone)]
pub struct Tracker<'a> {
    pub o: &'a Overlay,
    /// The stream reports cooldowns itself (combatfeed `cooldowns` lines): its list is the truth.
    pub feed_cooldowns: bool,
    pub now: f64,
    pub player: Option<String>,
    pub resource: f64,
    pub haste: f64,
    start: Option<f64>,
    gcd_end: f64,
    recharge: Vec<Vec<f64>>,            // per spell: when each spent charge comes back (own timers);
                                        // a group's casts start every member with its own length
    last_cast: Vec<Option<u64>>,        // per group: sequence number of the player's last cast of it
    casts: u64,                         // casts seen so far (live and looked ahead)
    srv: Vec<Option<(f64, u32, u32)>>,  // per spell, from the feed: next charge at, charges now, max
    snap_casts: u64,                    // `casts` when the last feed cooldown snapshot arrived
    slots: Vec<AuraState>,              // player auras, then markers
    units: HashMap<String, Vec<AuraState>>, // target auras by unit
    target: Option<String>,
    enemies: HashMap<String, f64>,
}

const TARGET: &str = "@target"; // look-ahead stand-in when no unit is known yet

#[derive(Debug, Clone)]
pub struct Pick {
    pub action: String,
    pub at: f64,
    /// Index into the overlay's rotation.
    pub rule: usize,
}

impl<'a> Tracker<'a> {
    pub fn new(o: &'a Overlay) -> Tracker<'a> {
        Tracker {
            o, feed_cooldowns: false, now: 0.0, player: None, resource: 0.0, haste: o.spec.haste, start: None, gcd_end: 0.0,
            recharge: vec![vec![]; o.spec.spells.len()],
            last_cast: vec![None; o.groups.len()], srv: vec![None; o.spec.spells.len()], casts: 0, snap_casts: 0,
            slots: vec![AuraState::default(); o.spec.auras.len() + o.spec.markers.len()],
            units: HashMap::new(), target: None, enemies: HashMap::new(),
        }.with_talent_defaults()
    }

    fn with_talent_defaults(mut self) -> Self {
        for &(sl, on) in &self.o.talent_slots {
            self.slots[sl] = if on { AuraState { exp: f64::INFINITY, stacks: 1 } } else { AuraState::default() };
        }
        self
    }

    fn probe_haste(&mut self, s: usize, duration: f64) {
        if let Some(base) = self.o.spec.auras[s].haste_probe && duration > 0.0 {
            let h = base / duration - 1.0;
            if (0.0..=1.5).contains(&h) { self.haste = h; }
        }
    }

    /// Move the clock without an event (the overlay's frame tick).
    pub fn advance(&mut self, t: f64) { if t > self.now { self.now = t; } }

    pub fn haste_at(&self, t: f64) -> f64 {
        let mut m = 1.0 + self.haste;
        for &(s, h) in &self.o.haste_slots { if self.aura_up(s, t) { m *= 1.0 + h; } }
        m - 1.0
    }

    fn unit_auras(&self) -> Option<&Vec<AuraState>> {
        self.units.get(self.target.as_deref().unwrap_or(TARGET))
    }

    fn aura(&self, s: usize, t: f64) -> AuraState {
        let a = if s < self.o.spec.auras.len() && self.o.on_target[s] {
            self.unit_auras().map(|v| v[s]).unwrap_or_default()
        } else { self.slots[s] };
        if a.exp > t { a } else { AuraState::default() }
    }

    fn aura_up(&self, s: usize, t: f64) -> bool {
        if self.aura(s, t).exp <= t { return false; }
        match s.checked_sub(self.o.spec.auras.len()) {
            Some(m) => self.o.marker_end[m].is_none_or(|e| self.aura_up(e, t)),
            None => true,
        }
    }

    fn set_aura(&mut self, s: usize, unit: Option<String>, st: AuraState) {
        if s < self.o.spec.auras.len() && self.o.on_target[s] {
            let n = self.slots.len();
            let key = unit.or_else(|| self.target.clone()).unwrap_or_else(|| TARGET.into());
            self.units.entry(key).or_insert_with(|| vec![AuraState::default(); n])[s] = st;
        } else {
            self.slots[s] = st;
        }
        if st.exp <= self.now && s < self.o.spec.auras.len() {
            // an aura ending takes its markers with it
            for (m, e) in self.o.marker_end.iter().enumerate() {
                if *e == Some(s) { self.slots[self.o.spec.auras.len() + m] = AuraState::default(); }
            }
        }
    }

    fn talent_up(&self, n: &str, t: f64) -> bool { self.aura_up(self.o.slot_by_name[&format!("talent:{n}")], t) }

    /// Charges of a cooldown group, with talents.
    fn charges(&self, g: usize, t: f64) -> u32 {
        let o = self.o;
        o.spec.spells.iter().enumerate().filter(|(i, _)| o.spell_group[*i] == Some(g))
            .flat_map(|(_, s)| s.talent_charges.iter().filter(|(n, _)| self.talent_up(n, t)).map(|(_, &c)| c))
            .fold(o.groups[g].1, u32::max)
    }

    fn own_avail(&self, i: usize, t: f64) -> u32 {
        let Some(g) = self.o.spell_group[i] else { return 1 };
        let spent = self.recharge[i].iter().filter(|&&r| r > t).count() as u32;
        self.charges(g, t).saturating_sub(spent)
    }

    fn own_remains(&self, i: usize, t: f64) -> f64 {
        if self.own_avail(i, t) > 0 { return 0.0; }
        self.recharge[i].iter().filter(|&&r| r > t).fold(f64::INFINITY, |a, &r| a.min(r)) - t
    }

    /// How the feed's cooldown snapshot combines with the own timers: the snapshot holds the
    /// base cooldowns (no talent resets or cuts), the own timers know the talents but only from
    /// casts seen. A cast after the snapshot makes the snapshot stale for that cooldown; a cast
    /// before it means both apply, and the earlier ready time wins (talents only shorten).
    fn source(&self, i: usize) -> (bool, bool) {
        let Some(g) = self.o.spell_group[i] else { return (true, false) };
        let fresh = self.feed_cooldowns && !self.last_cast[g].is_some_and(|c| c > self.snap_casts);
        (!fresh || self.last_cast[g].is_some(), fresh)
    }

    fn srv_avail(&self, i: usize, t: f64) -> u32 {
        match (self.srv[i], self.o.spell_group[i]) {
            (_, None) => 1,
            (None, Some(g)) => self.charges(g, t),
            (Some((at, have, max)), _) => (have + u32::from(t >= at)).min(max.max(1)),
        }
    }

    fn avail(&self, i: usize, t: f64) -> u32 {
        match self.source(i) {
            (true, false) => self.own_avail(i, t),
            (false, _) => self.srv_avail(i, t),
            (true, true) => self.own_avail(i, t).max(self.srv_avail(i, t)),
        }
    }

    fn cd_remains(&self, i: usize, t: f64) -> f64 {
        if self.avail(i, t) > 0 { return 0.0; }
        let srv = || self.srv[i].map_or(0.0, |(at, _, _)| (at - t).max(0.0));
        match self.source(i) {
            (true, false) => self.own_remains(i, t),
            (false, _) => srv(),
            (true, true) => self.own_remains(i, t).min(srv()),
        }
    }

    fn ready(&self, spell: usize, t: f64) -> bool { self.avail(spell, t) > 0 }

    /// A spell's cooldown length for a cast at `t`: talents, stacks, haste.
    fn cd_len(&self, j: usize, t: f64, h: f64) -> f64 {
        let s = &self.o.spec.spells[j];
        let mut base = s.talent_cooldown.iter().find(|(n, _)| self.talent_up(n, t)).map_or(s.cooldown, |(_, &c)| c);
        if let Some(sc) = &s.stack_cooldown {
            let a = self.aura(self.o.slot_by_name[&sc.aura], t);
            base = (base + sc.seconds * a.stacks as f64).max(0.0);
        }
        if s.hasted { base / (1.0 + h) } else { base }
    }

    /// Feed one event.
    pub fn ingest(&mut self, ev: &LogEvent) { self.ingest_as(ev, None) }

    /// `mine_me`: (the player is the source, the player is the dest) when the stream says so
    /// itself (combatfeed's `mine` / `me`), instead of comparing GUIDs.
    pub fn ingest_as(&mut self, ev: &LogEvent, mine_me: Option<(bool, bool)>) {
        let t = ev.time_ms / 1000.0;
        self.advance(t);
        let t = self.now;
        let (src, dst) = (guid(&ev.source), guid(&ev.dest));
        let sub = ev.sub.as_str();
        // Without a known player GUID, the player is whoever casts one of the overlay's spells
        // or energizes themselves with its resource.
        if self.player.is_none() && mine_me.is_none()
            && (sub == "SPELL_CAST_SUCCESS" && self.o.spell_by_id.contains_key(&ev.spell_id)
                || sub.ends_with("ENERGIZE") && src == dst && !src.is_empty() && ev.school == self.o.spec.resource.power_type)
        {
            self.player = Some(src.clone());
        }
        let (by_me, on_me) = match (mine_me, &self.player) {
            (Some(f), _) => f,
            (None, Some(me)) => (src == *me, dst == *me),
            (None, None) => return,
        };
        match sub {
            "SPELL_CAST_SUCCESS" if by_me => {
                if !dst.is_empty() && !on_me { self.target = Some(dst); }
                if let Some(i) = self.o.spell_by_id(ev.spell_id) { self.cast(i, t, false); }
            }
            "SPELL_AURA_APPLIED" | "SPELL_AURA_REFRESH" | "SPELL_AURA_APPLIED_DOSE" | "SPELL_AURA_REMOVED_DOSE" | "SPELL_AURA_REMOVED" => {
                let Some(&s) = self.o.aura_by_id.get(&ev.spell_id) else { return };
                let target = self.o.on_target[s];
                if target && !(by_me && !on_me) || !target && !on_me { return; }
                let unit = if target { Some(dst.clone()) } else { None };
                let old = if target {
                    self.units.get(&dst).map(|v| v[s]).filter(|a| a.exp > t).unwrap_or_default()
                } else { self.aura(s, t) };
                let st = match sub {
                    "SPELL_AURA_REMOVED" => AuraState::default(),
                    "SPELL_AURA_APPLIED_DOSE" | "SPELL_AURA_REMOVED_DOSE" => {
                        let exp = if old.exp > t { old.exp } else { t + self.o.spec.auras[s].duration };
                        AuraState { exp, stacks: ev.amount.max(1.0) as u32 }
                    }
                    _ => {
                        let rem = if ev.amount > 0.0 { ev.amount / 1000.0 } else { self.o.spec.auras[s].duration };
                        if sub == "SPELL_AURA_APPLIED" && ev.amount > 0.0 { self.probe_haste(s, rem); }
                        AuraState { exp: t + rem, stacks: old.stacks.max(1) }
                    }
                };
                self.set_aura(s, unit, st);
            }
            "SPELL_ENERGIZE" | "SPELL_PERIODIC_ENERGIZE" if on_me && ev.school == self.o.spec.resource.power_type => {
                self.resource = (self.resource + ev.amount).clamp(0.0, self.o.spec.resource.max);
            }
            // Not a combat-log event: an exact power reading (SMSG_POWER_UPDATE), if the stream has one.
            "POWER_UPDATE" if on_me || by_me => self.resource = ev.amount.clamp(0.0, self.o.spec.resource.max),
            "SWING_DAMAGE" | "SWING_MISSED" | "SPELL_DAMAGE" | "SPELL_MISSED" | "SPELL_PERIODIC_DAMAGE" | "RANGE_DAMAGE"
                if by_me && !on_me && !dst.is_empty() =>
            {
                self.enemies.insert(dst.clone(), t);
                if sub.starts_with("SWING") || self.target.is_none() { self.target = Some(dst); }
            }
            "UNIT_DIED" | "UNIT_DESTROYED" | "PARTY_KILL" => {
                self.enemies.remove(&dst);
                self.units.remove(&dst);
                if self.target.as_deref() == Some(dst.as_str()) { self.target = None; }
            }
            _ => {}
        }
    }

    /// A cast lands at `t`: spend, start the cooldown, run its effects. `predict` adds what the
    /// live stream would report as its own events (gains, applied auras, consumed auras).
    pub fn cast(&mut self, i: usize, t: f64, predict: bool) {
        let o = self.o;
        let s = &o.spec.spells[i];
        self.start.get_or_insert(t);
        let h = self.haste_at(t);
        self.resource = (self.resource - s.cost).max(0.0);
        if let Some(g) = o.spell_group[i] && s.cooldown > 0.0 {
            // one category start; each member comes back after its own length (Blade Dance 15s,
            // Death Sweep 9s after either)
            self.casts += 1;
            self.last_cast[g] = Some(self.casts);
            let single = self.charges(g, t) <= 1;
            for j in o.in_group(g).collect::<Vec<_>>() {
                if o.spec.spells[j].cooldown <= 0.0 { continue; }
                let len = self.cd_len(j, t, h);
                let q = &mut self.recharge[j];
                q.retain(|&r| r > t);
                if single { q.clear(); } // one charge: the category restarts for every member
                let from = q.iter().copied().fold(t, f64::max);
                q.push(from + len);
            }
        }
        for e in &s.on_cast {
            let hit = match &e.if_aura {
                Some(a) => {
                    let sl = o.slot_by_name[a];
                    let up = self.aura_up(sl, t);
                    if up && predict { self.set_aura(sl, None, AuraState::default()); }
                    up
                }
                None => true,
            };
            if hit && let Some(c) = &e.reset && let Some(g) = o.group_of(c) {
                for j in o.in_group(g) { self.recharge[j].clear(); }
            }
        }
        let nauras = o.spec.auras.len();
        for (m, d) in o.spec.markers.iter().enumerate() {
            if d.cleared_by.contains(&s.name) { self.slots[nauras + m] = AuraState::default(); }
            if d.set_by.contains(&s.name) { self.slots[nauras + m] = AuraState { exp: f64::INFINITY, stacks: 1 }; }
        }
        if predict {
            self.resource = (self.resource + s.gain).min(o.spec.resource.max);
            let mut applies: Vec<_> = s.applies.iter().collect();
            applies.sort_by(|a, b| a.0.cmp(b.0));
            for (a, &d) in applies {
                let sl = o.slot_by_name[a];
                let old = self.aura(sl, t);
                self.set_aura(sl, None, AuraState { exp: old.exp.max(t + d), stacks: old.stacks.max(1) });
            }
        }
        let g = s.gcd.unwrap_or(o.spec.gcd);
        if g > 0.0 {
            let hasted = g / (1.0 + h);
            self.gcd_end = t + if s.gcd.is_none() { hasted.max(0.75) } else { hasted };
        }
    }

    /// The time the next GCD action can go out.
    pub fn next_free(&self) -> f64 { self.now.max(self.gcd_end) }

    fn usable(&self, i: usize, t: f64) -> bool {
        let s = &self.o.spec.spells[i];
        let up = |n: &String| self.aura_up(self.o.slot_by_name[n], t);
        self.ready(i, t) && self.resource >= s.cost && s.requires.as_ref().is_none_or(up) && !s.excludes.as_ref().is_some_and(up)
    }

    /// The first rule that fires at `t`.
    pub fn pick_at(&self, t: f64) -> Option<Pick> {
        let view = View { tr: self, t };
        self.o.rules.iter().enumerate().find_map(|(k, (i, c))| {
            (self.usable(*i, t) && c.as_ref().is_none_or(|c| c.eval(&view) != 0.0))
                .then(|| Pick { action: self.o.spell_name(*i).to_string(), at: t, rule: k })
        })
    }

    /// The next `n` presses, looking ahead by applying each pick to a copy of the state.
    pub fn recommend(&self, n: usize) -> Vec<Pick> {
        let mut tr = self.clone();
        let mut out = vec![];
        for _ in 0..n {
            let mut t = tr.next_free();
            // nothing usable now: wait for the first cooldown or resource-free rule to come up
            let mut p = tr.pick_at(t);
            let mut waited = 0;
            while p.is_none() && waited < 40 {
                t += 0.25;
                waited += 1;
                p = tr.pick_at(t);
            }
            let Some(p) = p else { break };
            tr.advance(p.at);
            tr.cast(tr.o.spell_index(&p.action).unwrap(), p.at, true);
            out.push(p);
        }
        out
    }

    pub fn active_enemies(&self) -> usize {
        let w = self.o.spec.aoe_window;
        self.enemies.values().filter(|&&s| s >= self.now - w).count().max(1)
    }

    pub fn aura_remains(&self, name: &str) -> f64 {
        let s = self.o.slot_by_name[name];
        if self.aura_up(s, self.now) { (self.aura(s, self.now).exp - self.now).max(0.0) } else { 0.0 }
    }

    pub fn cooldown_remains(&self, spell: &str) -> f64 {
        self.o.cd_spell(spell).map_or(0.0, |i| self.cd_remains(i, self.now))
    }
}

/// The state as the conditions see it at time `t` (the end of the current GCD).
struct View<'t, 'a> { tr: &'t Tracker<'a>, t: f64 }

impl Env for View<'_, '_> {
    fn var(&self, v: &Var) -> f64 {
        let (tr, t) = (self.tr, self.t);
        let b = |x: bool| if x { 1.0 } else { 0.0 };
        match *v {
            Var::Resource(_) => tr.resource,
            Var::ResourceMax(_) => tr.o.spec.resource.max,
            Var::Gcd => (tr.o.spec.gcd / (1.0 + tr.haste_at(t))).max(0.75),
            Var::GcdRemains => (tr.gcd_end - tr.now).max(0.0),
            Var::Time => tr.start.map_or(0.0, |s| t - s),
            Var::ActiveEnemies => tr.active_enemies() as f64,
            Var::CdReady(g) => b(tr.avail(g, t) > 0),
            Var::CdRemains(g) => tr.cd_remains(g, t),
            Var::CdCharges(g) => tr.avail(g, t) as f64,
            Var::CdFullRecharge(i) => tr.recharge[i].iter().fold(t, |a, &r| a.max(r)) - t,
            Var::BuffUp(s) => b(tr.aura_up(s, t)),
            Var::BuffRemains(s) => if tr.aura_up(s, t) { (tr.aura(s, t).exp - t).max(0.0) } else { 0.0 },
            Var::BuffStack(s) => if tr.aura_up(s, t) { tr.aura(s, t).stacks as f64 } else { 0.0 },
            Var::FightRemains => 1e9,
            Var::Const(x) => x,
            _ => 0.0,
        }
    }
}

/// A closed loop for testing and showing an overlay: a stand-in game that does what the
/// overlay says and reports it back only as combat-log events (casts, auras with remaining
/// time, energizes, damage). Its own bookkeeping is deliberately simple; the point is that the
/// tracker rebuilds its state from those events alone.
pub struct Demo<'a> {
    pub tracker: Tracker<'a>,
    pub log: Vec<LogEvent>,
    pub presses: Vec<(f64, String, f64, usize)>, // time, action, resource before, rule
    pending: Vec<LogEvent>,
    enemies: Vec<String>,
}

pub const DEMO_PLAYER: &str = "Player-1";

impl<'a> Demo<'a> {
    /// `extra`: (spell name, what the game does after it) pairs for class-specific echoes
    /// such as an aura that follows a channel. Swings every `swing` seconds give `swing_gain`.
    pub fn run(o: &'a Overlay, secs: f64, targets: usize, start_resource: f64, swing: f64, swing_gain: f64,
               extra: &[(&str, &str, f64, f64)]) -> Demo<'a> {
        let enemies: Vec<String> = (0..targets.max(1)).map(|i| format!("Creature-{}", i + 1)).collect();
        let mut d = Demo { tracker: Tracker::new(o), log: vec![], presses: vec![], pending: vec![], enemies };
        let me = DEMO_PLAYER;
        let pt = o.spec.resource.power_type;
        // the pull: the player has some resource and swings at the boss
        d.tracker.player = Some(me.into());
        d.emit(LogEvent::new(0.0, "POWER_UPDATE", me, me, 0, start_resource, pt));
        let mut next_swing = 0.0;
        let mut t = 0.0;
        // auras the game has up: name -> (id, expires)
        let mut up: HashMap<String, (i64, f64)> = HashMap::new();
        while t < secs {
            // swings and their resource up to t
            while next_swing <= t {
                let e = d.enemies[0].clone();
                d.emit(LogEvent::new(next_swing, "SWING_DAMAGE", me, &e, 0, 1000.0, 1));
                d.emit(LogEvent::new(next_swing, "SPELL_ENERGIZE", me, me, 0, swing_gain, pt));
                next_swing += swing / (1.0 + d.tracker.haste_at(next_swing));
            }
            // auras running out
            let mut gone: Vec<_> = up.iter().filter(|(_, v)| v.1 <= t).map(|(k, v)| (k.clone(), v.0, v.1)).collect();
            gone.sort_by(|a, b| a.2.total_cmp(&b.2));
            for (name, id, at) in gone {
                up.remove(&name);
                let on_target = o.spec.auras.iter().find(|a| a.name == name).is_some_and(|a| a.on.as_deref() == Some("target"));
                let dst = if on_target { d.enemies[0].clone() } else { me.to_string() };
                d.emit(LogEvent::new(at, "SPELL_AURA_REMOVED", me, &dst, id, 0.0, 0));
            }
            d.flush(t);
            d.tracker.advance(t);
            let Some(p) = d.tracker.pick_at(t.max(d.tracker.next_free())) else { t += 0.1; continue };
            if p.at > t + 1e-9 { t = p.at; continue; }
            let i = o.spell_index(&p.action).unwrap();
            let s = &o.spec.spells[i];
            d.presses.push((t, p.action.clone(), d.tracker.resource, p.rule));
            let boss = d.enemies[0].clone();
            d.emit(LogEvent::new(t, "SPELL_CAST_SUCCESS", me, &boss, s.ids[0], 0.0, 1));
            for e in d.enemies.clone() { d.emit(LogEvent::new(t + 0.05, "SPELL_DAMAGE", me, &e, s.ids[0], 5000.0, 1)); }
            if s.gain > 0.0 { d.emit(LogEvent::new(t, "SPELL_ENERGIZE", me, me, s.ids[0], s.gain, pt)); }
            let mut applies: Vec<_> = s.applies.iter().collect();
            applies.sort_by(|a, b| a.0.cmp(b.0));
            for (a, &dur) in applies {
                let def = o.spec.auras.iter().find(|x| &x.name == a).unwrap();
                let exp = up.get(a).map_or(t + dur, |v| v.1.max(t + dur));
                let dst = if def.on.as_deref() == Some("target") { boss.clone() } else { me.to_string() };
                let sub = if up.contains_key(a) { "SPELL_AURA_REFRESH" } else { "SPELL_AURA_APPLIED" };
                d.emit(LogEvent::new(t, sub, me, &dst, def.ids[0], (exp - t) * 1000.0, 0));
                up.insert(a.clone(), (def.ids[0], exp));
            }
            // auras the cast consumes
            for e in &s.on_cast {
                if let Some(a) = &e.if_aura && let Some((id, _)) = up.remove(a) {
                    d.emit(LogEvent::new(t, "SPELL_AURA_REMOVED", me, me, id, 0.0, 0));
                }
            }
            for &(after, aura, delay, dur) in extra {
                if after == s.name {
                    let def = o.spec.auras.iter().find(|x| x.name == aura).unwrap();
                    d.pending.push(LogEvent::new(t + delay, "SPELL_AURA_APPLIED", me, me, def.ids[0], dur * 1000.0, 0));
                    up.insert(aura.into(), (def.ids[0], t + delay + dur));
                }
            }
        }
        d
    }

    fn emit(&mut self, ev: LogEvent) {
        self.tracker.ingest(&ev);
        self.log.push(ev);
    }

    fn flush(&mut self, t: f64) {
        self.pending.sort_by(|a, b| a.time_ms.total_cmp(&b.time_ms));
        while self.pending.first().is_some_and(|e| e.time_ms / 1000.0 <= t) {
            let e = self.pending.remove(0);
            self.emit(e);
        }
    }

    pub fn count(&self, action: &str) -> usize { self.presses.iter().filter(|p| p.1 == action).count() }
}

/// combatfeed adapter (wow-forever `tools/combatfeed`): one JSON object a line, `type` = player,
/// target, event, auras, cooldowns or power. Snapshots (auras, cooldowns, power) replace the
/// estimates kept from events; anything a snapshot doesn't carry keeps being estimated.
impl Tracker<'_> {
    pub fn feed_line(&mut self, v: &serde_json::Value) {
        let f = |k: &str| v.get(k).and_then(|x| x.as_f64()).unwrap_or(0.0);
        let s = |k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
        let t = f("t") / 1000.0;
        self.advance(t);
        match v.get("type").and_then(|x| x.as_str()).unwrap_or("") {
            "event" => {
                let b = |k: &str| v.get(k).and_then(|x| x.as_bool()).unwrap_or(false);
                let ev = LogEvent::new(t, &s("sub"), &s("src"), &s("dst"), f("spell") as i64, f("amount"), f("school") as i64);
                self.ingest_as(&ev, Some((b("mine"), b("me"))));
            }
            "auras" => {
                let unit = s("unit");
                let list = v.get("auras").and_then(|x| x.as_array()).cloned().unwrap_or_default();
                let mut seen = vec![None; self.o.spec.auras.len()];
                let talents_listed = list.iter().any(|a| a.get("spell").and_then(|x| x.as_i64()).is_some_and(|id| self.o.talent_ids.contains(&id)));
                for a in &list {
                    let Some(&sl) = self.o.aura_by_id.get(&(a.get("spell").and_then(|x| x.as_i64()).unwrap_or(0))) else { continue };
                    let mine = a.get("mine").and_then(|x| x.as_bool()).unwrap_or(false);
                    if self.o.on_target[sl] != (unit == "target") || unit == "target" && !mine { continue; }
                    let rem = a.get("remaining_ms").and_then(|x| x.as_f64()).unwrap_or(0.0);
                    let dur = a.get("duration_ms").and_then(|x| x.as_f64()).unwrap_or(0.0);
                    let exp = if rem > 0.0 { t + rem / 1000.0 } else if dur == 0.0 { f64::INFINITY } else { t };
                    let stacks = a.get("stacks").and_then(|x| x.as_u64()).unwrap_or(0).max(1) as u32;
                    if dur > 0.0 && self.aura(sl, t).exp <= t { self.probe_haste(sl, dur / 1000.0); }
                    seen[sl] = Some(AuraState { exp, stacks });
                }
                for (sl, st) in seen.into_iter().enumerate() {
                    if self.o.on_target[sl] != (unit == "target") { continue; }
                    if !talents_listed && self.o.talent_slots.iter().any(|&(x, _)| x == sl) { continue; }
                    let st = st.unwrap_or_default();
                    let old = self.aura(sl, t);
                    if st.exp > t || old.exp > t { self.set_aura(sl, None, st); }
                }
            }
            "cooldowns" => {
                self.feed_cooldowns = true;
                self.snap_casts = self.casts;
                // the feed's GCD is unhasted: keep the own one once a cast has been seen
                if self.start.is_none() { self.gcd_end = t + f("gcd_ms") / 1000.0; }
                // An entry with "spell" is that spell's own state (a category whose members have
                // different lengths comes as one entry per length); one with only "spells" covers
                // every member of that cooldown group not given its own entry.
                self.srv = vec![None; self.recharge.len()];
                let list = v.get("cooldowns").and_then(|x| x.as_array()).cloned().unwrap_or_default();
                let state = |c: &serde_json::Value| {
                    let rem = c.get("remaining_ms").and_then(|x| x.as_f64()).unwrap_or(0.0) / 1000.0;
                    let max = c.get("max_charges").and_then(|x| x.as_u64()).map_or(1, |m| m as u32);
                    let have = c.get("charges").and_then(|x| x.as_u64()).map_or(0, |m| m as u32);
                    (t + rem, have, max)
                };
                for c in &list {
                    if let Some(i) = c.get("spell").and_then(|x| x.as_i64()).and_then(|id| self.o.spell_by_id(id)) {
                        self.srv[i] = Some(state(c));
                    }
                }
                for c in list.iter().filter(|c| c.get("spell").is_none()) {
                    let ids: Vec<i64> = c.get("spells").and_then(|x| x.as_array()).map(|a| a.iter().filter_map(|x| x.as_i64()).collect()).unwrap_or_default();
                    let Some(g) = ids.iter().find_map(|id| self.o.spell_by_id(*id).and_then(|i| self.o.spell_group[i])) else { continue };
                    for j in self.o.in_group(g).collect::<Vec<_>>() {
                        if self.srv[j].is_none() { self.srv[j] = Some(state(c)); }
                    }
                }
            }
            "target" => {
                let u = s("unit");
                self.target = (!u.is_empty()).then_some(u);
            }
            "power" => {
                for p in v.get("powers").and_then(|x| x.as_array()).cloned().unwrap_or_default() {
                    if p.get("type").and_then(|x| x.as_i64()) == Some(self.o.spec.resource.power_type) {
                        self.resource = p.get("amount").and_then(|x| x.as_f64()).unwrap_or(0.0);
                    }
                }
            }
            _ => {}
        }
    }

    /// The `{"type":"next",...}` line for the current state, or None with nothing to press.
    /// `queue[0]` is the press now; the rest is the look-ahead.
    pub fn next_line(&self, queue: usize) -> Option<serde_json::Value> {
        let picks = self.recommend(queue.max(1) + 2); // room for off-GCD picks
        let first = picks.first()?;
        let def = |a: &str| &self.o.spec.spells[self.o.spell_index(a).unwrap()];
        let entry = |a: &str| {
            let d = def(a);
            let mut e = serde_json::json!({ "spell": d.ids[0], "name": d.label(), "action": a });
            if let Some(k) = &d.key { e["key"] = k.clone().into(); }
            e
        };
        let on_gcd: Vec<_> = picks.iter().filter(|p| def(&p.action).gcd != Some(0.0)).take(queue.max(1)).collect();
        let off: Vec<_> = picks.iter().take_while(|p| def(&p.action).gcd == Some(0.0)).collect();
        let now = on_gcd.first().copied().unwrap_or(first);
        let mut out = entry(&now.action);
        out["type"] = "next".into();
        out["t"] = (self.now * 1000.0).round().into();
        out["in_ms"] = ((now.at - self.now).max(0.0) * 1000.0).round().into();
        // off-GCD presses (Vengeful Retreat) ride beside the row, not in it
        out["off_gcd"] = off.iter().map(|p| entry(&p.action)).collect::<Vec<_>>().into();
        out["queue"] = on_gcd.iter().map(|p| entry(&p.action)).collect::<Vec<_>>().into();
        Some(out)
    }
}
