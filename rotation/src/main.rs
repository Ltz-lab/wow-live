//! wow-rotation: a Hekili-style "press this next" for retail WoW, from the combat log.

use serde_json::json;

/// `wow-rotation <rotation.toml> [--demo secs] [--targets n] [--haste h] [--start fury]
///  [--replay events.jsonl|-] [--next n] [--player guid] [--feed] [--audit]`: run a live-rotation overlay (src/overlay.rs) on a
/// stand-in fight, or on combat-log events (one JSON object a line), printing its picks.
fn main() {
    let mut it = std::env::args().skip(1);
    use wow_rotation::rotation::{Demo, LogEvent, Overlay, Tracker};
    let (mut path, mut demo, mut targets, mut haste, mut start, mut replay, mut next) =
        (None, 40.0, 1usize, None, 40.0, None, 3usize);
    let mut player: Option<String> = None;
    let mut feed = false;
    let mut audit = false;
    while let Some(x) = it.next() {
        let mut v = || it.next().expect("missing value");
        match x.as_str() {
            "--demo" => demo = v().parse().unwrap(),
            "--targets" => targets = v().parse().unwrap(),
            "--haste" => haste = Some(v().parse::<f64>().unwrap()),
            "--start" => start = v().parse().unwrap(),
            "--replay" => replay = Some(v()),
            "--next" => next = v().parse().unwrap(),
            "--player" => player = Some(v()),
            "--feed" => feed = true,
            "--audit" => audit = true,
            _ => path = Some(x),
        }
    }
    let Some(path) = path else {
        eprintln!("usage: wow-rotation <rotation.toml> [--demo secs] [--targets n] [--haste h] [--start fury] [--replay events.jsonl|-] [--next n] [--player guid] [--feed] [--audit]");
        std::process::exit(2);
    };
    let mut o = Overlay::load(&path).unwrap_or_else(|e| { eprintln!("{e}"); std::process::exit(1) });
    if let Some(h) = haste { o.spec.haste = h; }
    if audit {
        // combatfeed lines in; for each of the player's presses of a listed spell, what the
        // overlay said just before it and whether it thought the spell was usable.
        use std::io::BufRead;
        let mut tr = Tracker::new(&o);
        let (mut n, mut first, mut inq, mut early) = (0, 0, 0, 0);
        for line in std::io::stdin().lock().lines() {
            let Ok(line) = line else { break };
            let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) else { continue };
            let press = (v["type"] == "event" && v["sub"] == "SPELL_CAST_SUCCESS" && v["mine"] == true)
                .then(|| v["spell"].as_i64().and_then(|id| o.spell_by_id(id))).flatten();
            if let Some(i) = press {
                tr.advance(v["t"].as_f64().unwrap_or(0.0) / 1000.0);
                let name = o.spell_name(i).to_string();
                let q: Vec<String> = tr.recommend(next).into_iter().filter(|p| o.spec.spells[o.spell_index(&p.action).unwrap()].gcd != Some(0.0)).map(|p| p.action).collect();
                let cd = tr.cooldown_remains(&name);
                n += 1;
                if q.first() == Some(&name) { first += 1; } else if q.contains(&name) { inq += 1; }
                if cd > 0.05 { early += 1; }
                println!("{:>8.2}  {:<16} fury {:>4.0}  haste {:>4.0}%  own cd {:>5.2}  said: {}", tr.now, name, tr.resource,
                    tr.haste_at(tr.now) * 100.0, cd, q.join(" < "));
            }
            tr.feed_line(&v);
        }
        println!("{n} presses: {first} were queue[0], {inq} elsewhere in the queue; {early} pressed while the overlay thought it was on cooldown");
        return;
    }
    if feed {
        // combatfeed | wow-rotation <file> --feed | auraoverlay: pass every line through and add
        // {"type":"next",...} whenever the suggestion changes.
        use std::io::{BufRead, Write};
        let mut tr = Tracker::new(&o);
        let (stdin, stdout) = (std::io::stdin(), std::io::stdout());
        let mut out = stdout.lock();
        let mut last = String::new();
        for line in stdin.lock().lines() {
            let Ok(line) = line else { break };
            if writeln!(out, "{line}").is_err() { return; }
            if let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) {
                tr.feed_line(&v);
                if let Some(n) = tr.next_line(next) {
                    let key = n["queue"].to_string();
                    if key != last {
                        last = key;
                        if writeln!(out, "{n}").is_err() { return; }
                    }
                }
            }
            if out.flush().is_err() { return; }
        }
        return;
    }
    if let Some(src) = replay {
        let text = if src == "-" {
            let mut s = String::new();
            std::io::Read::read_to_string(&mut std::io::stdin(), &mut s).expect("read stdin");
            s
        } else { std::fs::read_to_string(&src).expect("read events") };
        let mut tr = Tracker::new(&o);
        tr.player = player;
        let mut last: Vec<String> = vec![];
        for line in text.lines().filter(|l| !l.trim().is_empty()) {
            let ev: LogEvent = serde_json::from_str(line).unwrap_or_else(|e| panic!("{e}: {line}"));
            tr.ingest(&ev);
            let picks: Vec<String> = tr.recommend(next).into_iter().map(|p| p.action).collect();
            if picks != last {
                println!("{}", json!({ "t": tr.now, "fury": tr.resource, "enemies": tr.active_enemies(), "next": picks }));
                last = picks;
            }
        }
        return;
    }
    let extra: Vec<(&str, &str, f64, f64)> = o.spec.name.contains("Havoc").then(|| vec![
        // what the game does on its own after these casts (Havoc): Eternal Hunt after Eye Beam's
        // last tick, the Demonsurge stacking buff with each Metamorphosis entry
        ("eye_beam", "eternal_hunt", 2.0, 8.0), ("abyssal_gaze", "eternal_hunt", 2.0, 8.0),
        ("eye_beam", "demonsurge", 0.7, 15.0), ("metamorphosis", "demonsurge", 0.7, 15.0),
    ]).unwrap_or_default();
    let d = Demo::run(&o, demo, targets, start, 1.3, 15.0, &extra);
    println!("{} — {:.0}s, {} target(s), haste {:.0}%", o.spec.name, demo, targets, o.spec.haste * 100.0);
    println!("{:>7}  {:>5}  {:<18} rule", "time", "fury", "press");
    for (t, a, f, r) in &d.presses {
        let cond = o.spec.rotation[*r].cond.as_deref().unwrap_or("");
        println!("{t:>7.2}  {f:>5.0}  {a:<18} {r:>2}  {cond}");
    }
    let mut counts: Vec<(String, usize)> = o.spec.spells.iter().map(|s| (s.name.clone(), d.count(&s.name))).filter(|c| c.1 > 0).collect();
    counts.sort_by(|a, b| b.1.cmp(&a.1));
    println!("{}", counts.iter().map(|(n, c)| format!("{n} {c}")).collect::<Vec<_>>().join(", "));
}

