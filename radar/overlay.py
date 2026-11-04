#!/usr/bin/env python3
"""radar overlay: a heading-up radar drawn over the game, fed by the radar (main.go).

A transparent, click-through window that follows the WoW window (shown only
while WoW is the front app) and draws, from the server's own positions:

  - a round radar that turns with you (up = where you face), rings every
    RANGE/4 yards: players as big blue arrows pointing where they face,
    creatures as small orange dots, moving ones with a line to where they
    are heading. Players beyond the ring sit on its edge, with their distance.
  - a list of the nearest players: name, distance, height difference, and
    an arrow toward them.

Levelling mode (the default on Forever, where the update fields decode):
  - creatures coloured by con (grey, green, yellow, orange, red against your level),
    friendlies faint, critters hidden; elites ringed silver, rares purple
  - your quest targets ringed gold with their count ("Tough Wolf Meat 3/8"): mobs you
    must kill, mobs that drop a quest item, objects to loot or use (from forever.db)
  - quest givers as the client draws them: yellow ! (new quest), yellow ? (turn in),
    grey ! (low level)
  - a list of the nearest quest targets and quest givers, with direction and distance
On retail (no levels or quest log yet) it falls back to the player radar.

Gathering and safety (both modes):
  - every herb (green diamond) and ore (copper square) spot: filled when up, hollow with a
    timer when gathered ("1:20" to the measured respawn, or "4m" since), faint when out of
    view; the nearest up ones listed under the radar, the nearest beyond the ring on its edge
  - enemy players (the other faction, Forever) in red with level and class, a warning line
    above the radar for anyone near or closing in, and a chime when one first gets there

Reads http://127.0.0.1:8791/state (start the radar first).

    python3 overlay.py
    python3 overlay.py --range 150 --players-only --corner bl

--range Y      yards to the radar's edge (default 200: about as far as the server sends)
--players-only no creatures
--objects      also game objects
--corner C     tr (default), tl, br, bl
--size PX      radar diameter (default 300)
--heads        experimental: arrows over players' heads, assuming a fixed chase camera (--cam-*).
               Off unless your camera matches: the network carries your facing, not the camera
               (E54; mirroring the camera from mouse input drifted too far to use).
--heads-only   just the head arrows
--always       show on the main screen even without a WoW window (to try it)
--nodes K      herb, ore or all (default); --no-nodes for none
--no-chime     no sound for enemy players
--shot F       save a PNG of the overlay to F (after --after S seconds), then quit
"""

import argparse
import json
import math
import threading
import time
import urllib.request

import AppKit
import objc
import Quartz
from Foundation import NSObject, NSTimer

ap = argparse.ArgumentParser()
ap.add_argument("--url", default="http://127.0.0.1:8791/state")
ap.add_argument("--range", type=float, help="yards to the radar's edge (default 100 levelling, 200 players)")
ap.add_argument("--mode", default="auto", choices=["auto", "level", "players"])
ap.add_argument("--as-level", type=int, help="con colours as if you were this level (preview)")
ap.add_argument("--db", default=__import__("os").environ.get("WOW_FOREVER_DB", __import__("os").path.expanduser("~/Dev/wow/host/data/forever.db")))
ap.add_argument("--players-only", action="store_true")
ap.add_argument("--objects", action="store_true")
ap.add_argument("--corner", default="tr", choices=["tr", "tl", "br", "bl"])
ap.add_argument("--size", type=float, default=300)
ap.add_argument("--heads", action="store_true", help="arrows over players' heads in the 3D view")
ap.add_argument("--heads-only", action="store_true", help="just the head arrows, no radar")
ap.add_argument("--cam-dist", type=float, default=39.0, help="camera zoom in yards (max zoom = 15 x cameraDistanceMaxZoomFactor)")
ap.add_argument("--cam-pitch", type=float, default=26.5, help="camera pitch, degrees down")
ap.add_argument("--cam-yaw", type=float, default=0.0, help="camera yaw relative to your facing, degrees (0: behind you)")
ap.add_argument("--vfov", type=float, default=53.5, help="vertical field of view, degrees")
ap.add_argument("--cam-height", type=float, default=2.7, help="camera target above your feet, yards")
ap.add_argument("--head", type=float, default=3.3, help="arrow height above a player's feet, yards")
ap.add_argument("--always", action="store_true")
ap.add_argument("--nodes", default="all", choices=["all", "herb", "ore"])
ap.add_argument("--no-nodes", action="store_true")
ap.add_argument("--no-chime", action="store_true")
ap.add_argument("--shot")
ap.add_argument("--after", type=float, default=4)
A = ap.parse_args()
RANGE_SET = A.range is not None

PLAYER = (0.30, 0.79, 0.94)
UNIT = (0.97, 0.55, 0.42)
GO = (0.48, 0.83, 0.54)
SELF = (1.0, 0.82, 0.40)
DIM = (0.55, 0.60, 0.65)
LABELS = 10  # the nearest players get a name on the radar and a row in the list

# --- levelling: forever.db joined with the radar's quest log --------------------------

GOLD = (1.0, 0.82, 0.25)
GREY = (0.6, 0.6, 0.6)
ALLIANCE = {1, 3, 4, 7, 11, 22, 25, 29, 30, 32, 34, 37, 52}


class DB:
    def __init__(self, path):
        import sqlite3
        self.ok = False
        try:
            c = sqlite3.connect(f"file:{path}?mode=ro", uri=True, check_same_thread=False)
        except Exception:
            return
        q = lambda sql: c.execute(sql).fetchall()
        self.npcs = {i: (n, lo, hi, rank or "", ra, rh, typ or "") for i, n, lo, hi, rank, ra, rh, typ in
                     q("select id, name, minlevel, maxlevel, rank, react_alliance, react_horde, type from npcs")}
        self.objects = dict(q("select id, name from objects"))
        self.items = dict(q("select id, name from items"))
        self.quests = {i: (n, et, ei) for i, n, et, ei in q("select id, name, end_type, end_id from quests")}
        self.objs = {}
        for quest, storage, typ, target, amount in q("select quest, storage, type, target, amount from tc_quest_objectives"):
            self.objs.setdefault(quest, []).append((storage, typ, target, amount))
        self.droppers = {}
        for npc, item in q("select npc, item from loot where kind = 'drop'"):
            self.droppers.setdefault(item, set()).add(npc)
        self.containers = {}
        for obj, item in q("select object, item from object_loot"):
            self.containers.setdefault(item, set()).add(obj)
        self.ok = True


DBX = DB(A.db)


def grey_level(lvl):
    if lvl <= 5:
        return 0
    if lvl <= 39:
        return lvl - lvl // 10 - 5
    if lvl <= 59:
        return lvl - lvl // 5 - 1
    return lvl - 9


def con(mob, me):
    d = mob - me
    if d >= 5:
        return (1.0, 0.15, 0.15)
    if d >= 3:
        return (1.0, 0.5, 0.15)
    if d >= -2:
        return (1.0, 0.9, 0.2)
    if mob > grey_level(me):
        return (0.25, 0.85, 0.3)
    return GREY


def quest_targets(quests):
    """npc entry / object entry -> (label, quest name) for every objective still open."""
    npc, obj = {}, {}
    if not DBX.ok:
        return npc, obj
    for q in quests:
        qname = DBX.quests.get(q["id"], ("Quest %d" % q["id"],))[0]
        prog = q.get("p") or []
        for storage, typ, target, amount in DBX.objs.get(q["id"], []):
            have = prog[storage] if storage < len(prog) else 0
            if amount and have >= amount:
                continue  # done
            cnt = f" {have}/{amount}" if amount > 1 else ""
            if typ == 0:  # kill
                npc[target] = (DBX.npcs.get(target, (f"#{target}",))[0] + cnt, qname)
            elif typ == 1:  # item: who drops it, what holds it
                item = DBX.items.get(target, f"item {target}")
                for n in DBX.droppers.get(target, ()):
                    npc.setdefault(n, (f"{item}{cnt}", qname))
                for o in DBX.containers.get(target, ()):
                    obj.setdefault(o, (f"{item}{cnt}", qname))
            elif typ == 2:  # use an object
                obj[target] = (DBX.objects.get(target, f"#{target}") + cnt, qname)
    return npc, obj


def bang(x, y, ch, rgb, size=15):
    t = text(ch, size, rgb, 1, AppKit.NSFontWeightHeavy)
    t.drawAtPoint_((x - t.size().width / 2, y - t.size().height / 2))


def ring(x, y, r, rgb, alpha=1.0, width=1.5):
    p = AppKit.NSBezierPath.bezierPathWithOvalInRect_(((x - r, y - r), (2 * r, 2 * r)))
    color(rgb, alpha).setStroke()
    p.setLineWidth_(width)
    p.stroke()


def draw_level(s, cx, cy, R, rng):
    me = s["self"]
    lvl = A.as_level or s.get("level") or 1
    side = "react_alliance" if (s.get("race") or 1) in ALLIANCE else "react_horde"
    tnpc, tobj = quest_targets(s.get("quests") or [])
    scale = R / rng
    ups = draw_nodes(s, cx, cy, R, rng)
    rows = []  # (distance, angle, rgb, title, sub)
    marks = []
    for o in s.get("objects") or []:
        d, dz, ang = rel(me, o)
        k, e = o["k"], o.get("e", 0)
        if k == "player":
            if d <= rng and not o.get("hostile"):
                x, y = to_screen(cx, cy, scale, d, ang)
                arrow(x, y, o["o"] - me["o"], 5, PLAYER, 0.6)
            continue
        if k == "unit":
            info = DBX.npcs.get(e) if DBX.ok else None
            name = o.get("n") or (info[0] if info else f"Creature {e}")
            target = tnpc.get(e)
            qs = o.get("qs")
            if o.get("dead") and not target:
                continue
            if target:
                rows.append((d, ang, GOLD, target[0], target[1]))
            elif qs in ("available", "turnin"):
                rows.append((d, ang, GOLD, name, "turn in" if qs == "turnin" else "new quest"))
            if d > rng:
                continue
            x, y = to_screen(cx, cy, scale, d, ang)
            react = (info[4] if side == "react_alliance" else info[5]) if info else "hostile"
            if qs:
                marks.append((x, y, "?" if qs == "turnin" else "!", GOLD if qs in ("available", "turnin") else GREY))
                continue
            if react == "friendly":
                dot(x, y, 1.6, GREY, 0.35)
                continue
            if info and info[6] == "critter" and not target:
                continue
            rgb = con(o.get("lvl") or (info[1] if info else lvl), lvl)
            if o.get("dead"):
                rgb = GREY
            r = 3.0
            # (forever.db's reactions say "neutral" for mobs that attack, so no hollow neutral style)
            dot(x, y, r, rgb, 0.95 if abs(dz) < 15 else 0.5)
            rank = info[3] if info else ""
            if "rare" in rank:
                ring(x, y, 6, (0.75, 0.45, 1.0), 1, 2)
            elif rank in ("elite", "boss"):
                ring(x, y, 6, (0.85, 0.85, 0.9), 0.9, 1.5)
            if target:
                ring(x, y, 7.5, GOLD, 1, 2)
            if o.get("m"):
                d2, _, a2 = rel(me, {"x": o["tx"], "y": o["ty"], "z": o["tz"]})
                x2, y2 = to_screen(cx, cy, scale, min(d2, rng), a2)
                p = AppKit.NSBezierPath.bezierPath()
                p.moveToPoint_((x, y))
                p.lineToPoint_((x2, y2))
                color(rgb, 0.4).setStroke()
                p.setLineWidth_(1)
                p.stroke()
        elif k == "gameobject":
            target = tobj.get(e)
            if not target:
                continue
            rows.append((d, ang, GOLD, target[0], target[1]))
            if d <= rng:
                x, y = to_screen(cx, cy, scale, d, ang)
                color(GOLD, 0.95).setFill()
                AppKit.NSBezierPath.bezierPathWithRect_(((x - 3, y - 3), (6, 6))).fill()
                ring(x, y, 7.5, GOLD, 1, 2)
    for x, y, ch, rgb in marks:  # quest givers on top
        bang(x, y, ch, rgb)
    draw_threats(s, cx, cy, R, rng)
    arrow(cx, cy, 0, 8, SELF)

    # the list: nearest quest targets and givers, one row per kind of thing
    rows.sort(key=lambda r: r[0])
    seen, out = set(), []
    for r in rows:
        if r[3] not in seen:
            seen.add(r[3])
            out.append(r)
    y = cy - R - 22
    hdr = text(f"LEVEL {lvl}   QUESTS {len(s.get('quests') or [])}", 11, DIM, 1, AppKit.NSFontWeightSemibold)
    hdr.drawAtPoint_((cx - R, y))
    if not out:
        y -= 18
        text("nothing for your quests nearby", 12, DIM).drawAtPoint_((cx - R, y))
    for d, ang, rgb, title_, sub in out[:7]:
        y -= 30
        a = text(f"{compass(ang)}  {title_}", 12.5, rgb, 1, AppKit.NSFontWeightSemibold)
        a.drawAtPoint_((cx - R, y + 12))
        dist = text(f"{d:.0f} yd", 12, (0.9, 0.93, 0.95))
        dist.drawAtPoint_((cx + R - dist.size().width, y + 12))
        text(sub, 10.5, DIM).drawAtPoint_((cx - R + 18, y))
    draw_node_list(ups, cx - R, y, R)


# --- gathering nodes and enemy players ----------------------------------------------

HERB = (0.35, 0.95, 0.45)
ORE = (0.95, 0.62, 0.30)
RED = (1.0, 0.28, 0.25)


def clock(sec):
    sec = int(sec)
    if sec >= 3600:
        return f"{sec // 3600}h"
    if sec >= 600:
        return f"{sec // 60}m"
    return f"{sec // 60}:{sec % 60:02d}"


def node_shape(x, y, kind, size, rgb, alpha, filled):
    p = AppKit.NSBezierPath.bezierPath()
    if kind == "herb":  # diamond
        p.moveToPoint_((x, y + size))
        p.lineToPoint_((x + size, y))
        p.lineToPoint_((x, y - size))
        p.lineToPoint_((x - size, y))
        p.closePath()
    else:
        s = size * 0.8
        p.appendBezierPathWithRect_(((x - s, y - s), (2 * s, 2 * s)))
    if filled:
        color(rgb, alpha).setFill()
        p.fill()
        color((0, 0, 0), 0.7 * alpha).setStroke()
        p.setLineWidth_(1)
    else:
        color(rgb, alpha).setStroke()
        p.setLineWidth_(1.4)
    p.stroke()


def draw_nodes(s, cx, cy, R, rng):
    """The node spots on the radar. Returns the nearest up ones for the list."""
    if A.no_nodes:
        return []
    me, scale = s["self"], R / rng
    ups, rim = [], 0
    for n in s.get("nodes") or []:
        if A.nodes != "all" and n["k"] != A.nodes:
            continue
        rgb = HERB if n["k"] == "herb" else ORE
        d, dz, ang = rel(me, dict(n, z=n["z"] if n.get("hz") else me["z"]))
        st = n["st"]
        if st == "up":
            ups.append((d, ang, n))
        if d > rng:
            if st == "up" and rim < 3:  # the nearest few beyond the ring, on its edge
                rim += 1
                x, y = to_screen(cx, cy, scale, rng, ang)
                node_shape(x, y, n["k"], 4, rgb, 0.75, True)
            continue
        x, y = to_screen(cx, cy, scale, d, ang)
        if st == "up":
            node_shape(x, y, n["k"], 5, rgb, 1.0 if abs(dz) < 15 else 0.6, True)
        elif st == "gone":
            node_shape(x, y, n["k"], 4.5, rgb, 0.8, False)
            label = clock(n["eta"]) if n.get("eta") else (clock(n["ago"]) + " ago" if n.get("ago") else "")
            if label:
                text(label, 9.5, rgb, 0.9).drawAtPoint_((x + 6, y - 6))
        elif st == "seen":
            node_shape(x, y, n["k"], 4, rgb, 0.55, False)
        elif st == "empty":
            node_shape(x, y, n["k"], 3, rgb, 0.25, False)
        else:  # unknown
            dot(x, y, 1.3, rgb, 0.35)
    return ups


def draw_node_list(ups, x0, y, R):
    if A.no_nodes:
        return y
    y -= 26
    text(f"NODES UP  {len(ups)}", 11, DIM, 1, AppKit.NSFontWeightSemibold).drawAtPoint_((x0, y))
    for d, ang, n in ups[:3]:
        y -= 18
        rgb = HERB if n["k"] == "herb" else ORE
        text(f"{compass(ang)}  {n['n']}", 12.5, rgb, 1, AppKit.NSFontWeightSemibold).drawAtPoint_((x0, y))
        dist = text(f"{d:.0f} yd", 12, (0.9, 0.93, 0.95))
        dist.drawAtPoint_((x0 + 2 * R - dist.size().width, y))
    return y


warned = {"guids": set(), "chimed": 0.0}


def draw_threats(s, cx, cy, R, rng):
    """Enemy players in red on the radar, the nearest warning above it, a chime on a new one."""
    me, scale = s["self"], R / rng
    th = s.get("threats") or []
    by = {t["g"]: t for t in th}
    for o in s.get("objects") or []:
        t = by.get(o["g"])
        if not t:
            continue
        d, dz, ang = rel(me, o)
        x, y = to_screen(cx, cy, scale, min(d, rng), ang)
        arrow(x, y, o["o"] - me["o"], 9 if t.get("warn") else 7, RED, 1 if d <= rng else 0.6)
        lbl = f"{t.get('lvl') or '?'} {t.get('cls') or ''}".strip()
        text(f"{lbl} {d:.0f}", 10.5, RED, 0.95, AppKit.NSFontWeightSemibold).drawAtPoint_((x + 9, y - 6))
    warn = [t for t in th if t.get("warn")]
    now = set(t["g"] for t in warn)
    new = now - warned["guids"]
    warned["guids"] = now
    if new and not A.no_chime and time.monotonic() - warned["chimed"] > 8:
        warned["chimed"] = time.monotonic()
        snd = AppKit.NSSound.soundNamed_("Sosumi")
        if snd:
            snd.play()
    if warn:
        t = warn[0]
        d, dz, ang = rel(me, next((o for o in s["objects"] if o["g"] == t["g"]), me))
        who = " ".join(str(v) for v in (t.get("lvl") or "?", t.get("race") or "", t.get("cls") or "") if v)
        move = f"closing {t['close']:.0f} yd/s" if t.get("close", 0) > 1 else ("moving away" if t.get("close", 0) < -1 else "")
        more = f"   +{len(warn) - 1}" if len(warn) > 1 else ""
        line = text(f"⚠  {who}  {compass(ang)} {t['d']:.0f} yd  {move}{more}", 13, RED, 1, AppKit.NSFontWeightBold)
        line.drawAtPoint_((cx - line.size().width / 2, cy + R + 8))


lock = threading.Lock()
state = {"snap": None, "err": None, "at": 0.0}


def poller():
    while True:
        try:
            with urllib.request.urlopen(A.url, timeout=1) as r:
                s = json.load(r)
            with lock:
                state["snap"], state["err"], state["at"] = s, None, time.monotonic()
        except Exception as e:  # the radar restarts: keep trying
            with lock:
                state["err"] = str(e)
        time.sleep(0.1)


def font(size, weight=AppKit.NSFontWeightMedium):
    return AppKit.NSFont.monospacedDigitSystemFontOfSize_weight_(size, weight)


def text(s, size, rgb, alpha=1.0, weight=AppKit.NSFontWeightMedium):
    shadow = AppKit.NSShadow.alloc().init()
    shadow.setShadowOffset_((0, -1))
    shadow.setShadowBlurRadius_(2)
    shadow.setShadowColor_(AppKit.NSColor.colorWithWhite_alpha_(0, 0.9))
    attrs = {
        AppKit.NSFontAttributeName: font(size, weight),
        AppKit.NSForegroundColorAttributeName: AppKit.NSColor.colorWithRed_green_blue_alpha_(*rgb, alpha),
        AppKit.NSShadowAttributeName: shadow,
    }
    return AppKit.NSAttributedString.alloc().initWithString_attributes_(s, attrs)


def color(rgb, alpha=1.0):
    return AppKit.NSColor.colorWithRed_green_blue_alpha_(*rgb, alpha)


def rel(me, o):
    """Distance, height difference and angle from our facing (CCW, radians)."""
    dx, dy = o["x"] - me["x"], o["y"] - me["y"]
    d = math.hypot(dx, dy)
    return d, o["z"] - me["z"], math.atan2(dy, dx) - me["o"]


def to_screen(cx, cy, scale, d, ang):
    # heading-up: forward is up, counter-clockwise (toward WoW's +Y, west when facing north) is left
    return cx - d * scale * math.sin(ang), cy + d * scale * math.cos(ang)


def arrow(cx, cy, ang, size, rgb, alpha=1.0):
    """A filled arrowhead at (cx, cy) pointing at screen angle ang (0 = up, CCW positive)."""
    p = AppKit.NSBezierPath.bezierPath()
    pts = [(0, size), (-size * 0.6, -size * 0.6), (0, -size * 0.25), (size * 0.6, -size * 0.6)]
    s, c = math.sin(ang), math.cos(ang)
    for i, (x, y) in enumerate(pts):
        X, Y = cx + x * c - y * s, cy + x * s + y * c
        (p.moveToPoint_ if i == 0 else p.lineToPoint_)((X, Y))
    p.closePath()
    color(rgb, alpha).setFill()
    p.fill()
    color((0, 0, 0), 0.7).setStroke()
    p.setLineWidth_(1)
    p.stroke()


def dot(x, y, r, rgb, alpha=1.0):
    color(rgb, alpha).setFill()
    AppKit.NSBezierPath.bezierPathWithOvalInRect_(((x - r, y - r), (2 * r, 2 * r))).fill()


def compass(ang):
    arrows = "↑↖←↙↓↘→↗"
    return arrows[int(round((ang % (2 * math.pi)) / (math.pi / 4))) % 8]


def title(o):
    if o.get("n"):
        return o["n"]
    return {"player": "Player", "unit": f"Creature {o.get('e', '')}", "gameobject": "Object"}.get(o["k"], o["k"])


def project_heads(me, players, w, h):
    """World -> screen with a chase camera: target = us + cam-height, the
    camera dist back along yaw (our facing + the orbit offset), pitched down."""
    dist, pitch_deg, off = A.cam_dist, A.cam_pitch, math.radians(A.cam_yaw)
    yaw = me["o"] + off
    pitch = math.radians(pitch_deg)
    f = (math.cos(pitch) * math.cos(yaw), math.cos(pitch) * math.sin(yaw), -math.sin(pitch))
    r = (math.sin(yaw), -math.cos(yaw), 0.0)
    u = (r[1] * f[2] - r[2] * f[1], r[2] * f[0] - r[0] * f[2], r[0] * f[1] - r[1] * f[0])
    C = (me["x"] - f[0] * dist, me["y"] - f[1] * dist, me["z"] + A.cam_height - f[2] * dist)
    fo = (h / 2) / math.tan(math.radians(A.vfov) / 2)
    out = []
    for o in players:
        v = (o["x"] - C[0], o["y"] - C[1], o["z"] + A.head - C[2])
        z = v[0] * f[0] + v[1] * f[1] + v[2] * f[2]
        if z < 1:
            continue
        x = v[0] * r[0] + v[1] * r[1] + v[2] * r[2]
        y = v[0] * u[0] + v[1] * u[1] + v[2] * u[2]
        sx, sy = w / 2 + x / z * fo, h / 2 + y / z * fo  # non-flipped view: y up
        if -20 < sx < w + 20 and -20 < sy < h + 20:
            out.append((sx, sy, z, o))
    return out


def draw_heads(me, players, w, h):
    for sx, sy, z, o in sorted(project_heads(me, players, w, h), key=lambda t: -t[2]):
        d = math.hypot(o["x"] - me["x"], o["y"] - me["y"])
        k = max(0.55, min(1.0, 30 / max(z, 1)))  # nearer = bigger
        s = 9 * k
        p = AppKit.NSBezierPath.bezierPath()
        p.moveToPoint_((sx, sy))
        p.lineToPoint_((sx - s, sy + s * 1.6))
        p.lineToPoint_((sx + s, sy + s * 1.6))
        p.closePath()
        color(PLAYER, 0.95).setFill()
        p.fill()
        color((0, 0, 0), 0.8).setStroke()
        p.setLineWidth_(1)
        p.stroke()
        t = text(f"{title(o)}  {d:.0f} yd", 10 + 2 * k, PLAYER, 0.95, AppKit.NSFontWeightSemibold)
        t.drawAtPoint_((sx - t.size().width / 2, sy + s * 1.6 + 2))


class View(AppKit.NSView):
    def isFlipped(self):
        return False

    def drawRect_(self, rect):
        b = self.bounds()
        w, h = b.size.width, b.size.height
        with lock:
            s, err = state["snap"], state["err"]
        if (A.heads or A.heads_only) and s and s.get("self"):
            draw_heads(s["self"], [o for o in s.get("objects") or [] if o["k"] == "player"], w, h)
        if A.heads_only:
            return
        if not RANGE_SET:
            A.range = 100.0 if (A.mode == "level" or (A.mode == "auto" and s and s.get("level"))) else 200.0
        R = A.size / 2
        m = 24
        cx = w - m - R if A.corner in ("tr", "br") else m + R
        cy = h - m - R - 30 if A.corner in ("tr", "tl") else m + R + 30
        # backdrop and rings
        color((0.04, 0.06, 0.08), 0.55).setFill()
        AppKit.NSBezierPath.bezierPathWithOvalInRect_(((cx - R, cy - R), (2 * R, 2 * R))).fill()
        for i in (1, 2, 3, 4):
            r = R * i / 4
            p = AppKit.NSBezierPath.bezierPathWithOvalInRect_(((cx - r, cy - r), (2 * r, 2 * r)))
            color((1, 1, 1), 0.10 if i < 4 else 0.25).setStroke()
            p.setLineWidth_(1)
            p.stroke()
        t = text(f"{int(A.range)} yd", 10, DIM)
        t.drawAtPoint_((cx + R * 0.71 + 2, cy - R * 0.71 - 12))
        if s is None or s.get("self") is None:
            msg = "waiting for the radar…" if err else "waiting for you to move…"
            t = text(msg, 12, DIM)
            t.drawAtPoint_((cx - t.size().width / 2, cy - 6))
            return
        level_mode = A.mode == "level" or (A.mode == "auto" and s.get("level"))
        if level_mode:
            draw_level(s, cx, cy, R, A.range)
            return
        me = s["self"]
        scale = R / A.range
        # north marker on the rim
        nx, ny = to_screen(cx, cy, R / 1, 1, -me["o"])
        t = text("N", 11, DIM, 1, AppKit.NSFontWeightSemibold)
        t.drawAtPoint_((nx - t.size().width / 2, ny - t.size().height / 2))

        ups = draw_nodes(s, cx, cy, R, A.range)
        objs = s.get("objects") or []
        players = []
        # creatures and objects first, players on top
        for o in objs:
            k = o["k"]
            if k == "player":
                if not o.get("hostile"):
                    players.append(o)
                continue
            if k == "unit" and A.players_only:
                continue
            if k == "gameobject" and not A.objects:
                continue
            if k not in ("unit", "gameobject"):
                continue
            d, dz, ang = rel(me, o)
            if d > A.range:
                continue
            x, y = to_screen(cx, cy, scale, d, ang)
            rgb = UNIT if k == "unit" else GO
            if o.get("m"):
                d2, _, a2 = rel(me, {"x": o["tx"], "y": o["ty"], "z": o["tz"]})
                x2, y2 = to_screen(cx, cy, scale, min(d2, A.range), a2)
                p = AppKit.NSBezierPath.bezierPath()
                p.moveToPoint_((x, y))
                p.lineToPoint_((x2, y2))
                p.setLineWidth_(1)
                color(rgb, 0.5).setStroke()
                p.stroke()
            dot(x, y, 2.6 if k == "unit" else 2.0, rgb, 0.9 if abs(dz) < 15 else 0.45)

        near = sorted((rel(me, o) + (o,) for o in players), key=lambda n: n[0])
        # names on the radar go to players past the inner ring (the close ones crowd the middle; the list has them)
        labelled = {id(n[3]) for n in [n for n in near if n[0] > A.range / 4][:LABELS]}
        for d, dz, ang, o in reversed(near):
            edge = d > A.range
            x, y = to_screen(cx, cy, scale, min(d, A.range), ang)
            # the arrow points where they face, relative to us (screen: up = our facing)
            arrow(x, y, o["o"] - me["o"], 7, PLAYER, 0.55 if edge else 1)
            if id(o) in labelled:
                t = text(f"{title(o)} {d:.0f}", 10.5, PLAYER, 0.95, AppKit.NSFontWeightSemibold)
                t.drawAtPoint_((x - t.size().width / 2, y + 8))

        draw_threats(s, cx, cy, R, A.range)
        arrow(cx, cy, 0, 8, SELF)  # us, always facing up

        # the nearest players, under the radar
        y = cy - R - 22
        hdr = text(f"PLAYERS  {len(near)}", 11, DIM, 1, AppKit.NSFontWeightSemibold)
        hdr.drawAtPoint_((cx - R, y))
        for d, dz, ang, o in near[:LABELS]:
            y -= 18
            name = text(f"{compass(ang)}  {title(o)}", 12.5, PLAYER, 1, AppKit.NSFontWeightSemibold)
            name.drawAtPoint_((cx - R, y))
            dist = text(f"{d:.0f} yd  {'+' if dz >= 0 else ''}{dz:.0f}", 12, (0.9, 0.93, 0.95))
            dist.drawAtPoint_((cx + R - dist.size().width, y))
        draw_node_list(ups, cx - R, y, R)


def wow_window():
    best = None
    for w in Quartz.CGWindowListCopyWindowInfo(Quartz.kCGWindowListOptionOnScreenOnly, Quartz.kCGNullWindowID) or []:
        owner = str(w.get("kCGWindowOwnerName", ""))
        if not ("World of Warcraft" in owner or owner == "Wow") or w.get("kCGWindowLayer") != 0:
            continue
        b = w["kCGWindowBounds"]
        if best is None or b["Width"] * b["Height"] > best[0]["Width"] * best[0]["Height"]:
            best = (b, w["kCGWindowOwnerPID"])
    return best


class Controller(NSObject):
    def init(self):
        self = objc.super(Controller, self).init()
        self.started, self.last_find, self.found = time.monotonic(), 0, None
        style = AppKit.NSWindowStyleMaskBorderless | AppKit.NSWindowStyleMaskNonactivatingPanel
        self.panel = AppKit.NSPanel.alloc().initWithContentRect_styleMask_backing_defer_(
            ((0, 0), (800, 600)), style, AppKit.NSBackingStoreBuffered, False)
        self.panel.setOpaque_(False)
        self.panel.setBackgroundColor_(AppKit.NSColor.clearColor())
        self.panel.setHasShadow_(False)
        self.panel.setIgnoresMouseEvents_(True)
        self.panel.setLevel_(AppKit.NSStatusWindowLevel)
        self.panel.setCollectionBehavior_(AppKit.NSWindowCollectionBehaviorCanJoinAllSpaces
                                          | AppKit.NSWindowCollectionBehaviorStationary
                                          | AppKit.NSWindowCollectionBehaviorIgnoresCycle
                                          | AppKit.NSWindowCollectionBehaviorFullScreenAuxiliary)
        self.view = View.alloc().initWithFrame_(((0, 0), (800, 600)))
        self.panel.setContentView_(self.view)
        NSTimer.scheduledTimerWithTimeInterval_target_selector_userInfo_repeats_(1 / 30, self, "tick:", None, True)
        return self

    def tick_(self, _):
        if A.shot and time.monotonic() - self.started > A.after:
            self.saveTo_(A.shot)
            AppKit.NSApp.terminate_(None)
        screen = AppKit.NSScreen.screens()[0].frame()
        if A.always:
            rect = screen
        else:
            if time.monotonic() - self.last_find > 1:
                self.last_find, self.found = time.monotonic(), wow_window()
            front = AppKit.NSWorkspace.sharedWorkspace().frontmostApplication()
            if not self.found or front is None or front.processIdentifier() != self.found[1]:
                if self.panel.isVisible():
                    self.panel.orderOut_(None)
                return
            b = self.found[0]
            rect = ((b["X"], screen.size.height - b["Y"] - b["Height"]), (b["Width"], b["Height"]))
        if not AppKit.NSEqualRects(self.panel.frame(), rect):
            self.panel.setFrame_display_(rect, False)
        if not self.panel.isVisible():
            self.panel.orderFrontRegardless()
        self.view.setNeedsDisplay_(True)

    def saveTo_(self, path):
        b = self.view.bounds()
        w, h = int(b.size.width), int(b.size.height)
        rep = AppKit.NSBitmapImageRep.alloc().initWithBitmapDataPlanes_pixelsWide_pixelsHigh_bitsPerSample_samplesPerPixel_hasAlpha_isPlanar_colorSpaceName_bytesPerRow_bitsPerPixel_(
            None, w, h, 8, 4, True, False, AppKit.NSDeviceRGBColorSpace, 0, 0)
        ctx = AppKit.NSGraphicsContext.graphicsContextWithBitmapImageRep_(rep)
        AppKit.NSGraphicsContext.saveGraphicsState()
        AppKit.NSGraphicsContext.setCurrentContext_(ctx)
        AppKit.NSColor.colorWithRed_green_blue_alpha_(0.18, 0.22, 0.19, 1).setFill()
        AppKit.NSRectFill(b)
        self.view.drawRect_(b)
        AppKit.NSGraphicsContext.restoreGraphicsState()
        rep.representationUsingType_properties_(AppKit.NSBitmapImageFileTypePNG, None).writeToFile_atomically_(path, True)


def main():
    app = AppKit.NSApplication.sharedApplication()
    app.setActivationPolicy_(AppKit.NSApplicationActivationPolicyAccessory)
    threading.Thread(target=poller, daemon=True).start()
    Controller.alloc().init()
    app.run()


if __name__ == "__main__":
    main()
