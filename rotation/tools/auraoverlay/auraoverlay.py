#!/usr/bin/env python3
"""auraoverlay: WeakAuras-style bars over the game, fed by tools/combatfeed.

A transparent, click-through window that follows the WoW window (shown only
while WoW is the front app) and draws, from the server's own messages:

  - your timed buffs as bars with a countdown and stacks (left of centre),
  - your debuffs on your target, then everyone else's (right of centre),
  - your target, and a short ticker of what you just did,
  - your power (Fury, ...) and the GCD from the feed's power/cooldowns lines,
  - a rotation's suggestions as a row of spell icons, read right to left
    (the rightmost, biggest one is the button to press now):
      {"type":"next","spell":N,"name":"...","queue":[{"spell":N,"name":"...","key":"3"}, ...]}
    "queue" is optional (first = now); without it the row is just "spell".
    "off_gcd": [{"spell",...}] draws its first pick small on top of the press-now icon.
    Icons come from Wowhead (tooltip JSON for the icon name, then the image)
    once per spell, cached in ~/.cache/wow-live/icons.

No addon and no addon API: the data is the decrypted world stream
(protocol/combatlog). Reads combatfeed's JSON lines on stdin.

    go run ./tools/combatfeed -pkt <fork>/logs/World.pkt -start 610 -replay 1 \\
      | python3 tools/auraoverlay/auraoverlay.py --always

--always   show on the main screen even without a WoW window (to try it)
--passives also list auras with no duration (talents, passives)
--rotation only the rotation's icon row: left to right (the leftmost, biggest is now),
           small, under the character; no bars, ticker, power or GCD bar
--shot F   save a PNG of the overlay to F (after --after S seconds, default 6), then quit
"""

import json
import os
import queue
import sys
import threading
import time
import urllib.request

import AppKit
import objc
import Quartz
from Foundation import NSObject, NSTimer
from PyObjCTools import AppHelper

ALWAYS = "--always" in sys.argv
PASSIVES = "--passives" in sys.argv
SHOT = sys.argv[sys.argv.index("--shot") + 1] if "--shot" in sys.argv else None
ROTATION = "--rotation" in sys.argv
AFTER = float(sys.argv[sys.argv.index("--after") + 1]) if "--after" in sys.argv else 6.0

BAR_W, BAR_H, GAP = 230, 22, 4
MAX_BARS = 8
TICKER = 5

lock = threading.Lock()
state = {
    "player": [],   # [{"spell","name","stacks","duration","deadline","mine","born"}]
    "target": [],
    "target_name": "",
    "ticker": [],   # [(born, text, rgb)]
    "next": None,   # {"queue": [{"spell","name","key"}], "born"}: the rotation's suggestions
    "power": [],    # [(name, amount)]
    "gcd_end": 0.0,
}


def now():
    return time.monotonic()


MAX_QUEUE = 4
ICON_DIR = os.path.expanduser("~/.cache/wow-live/icons")
icons = {}        # spell -> NSImage (None while fetching or failed)
icon_q = queue.Queue()


def want_icon(spell):
    if spell not in icons:
        icons[spell] = None
        icon_q.put(spell)


def icon_fetcher():
    """Fetches spell icons once: Wowhead's tooltip JSON names the icon, its CDN serves it."""
    os.makedirs(ICON_DIR, exist_ok=True)
    while True:
        spell = icon_q.get()
        path = os.path.join(ICON_DIR, f"{spell}.jpg")
        try:
            if not os.path.exists(path):
                req = urllib.request.Request(f"https://nether.wowhead.com/tooltip/spell/{spell}", headers={"User-Agent": "wow-forever"})
                name = json.load(urllib.request.urlopen(req, timeout=10)).get("icon")
                if not name:
                    continue
                req = urllib.request.Request(f"https://wow.zamimg.com/images/wow/icons/large/{name}.jpg", headers={"User-Agent": "wow-forever"})
                data = urllib.request.urlopen(req, timeout=10).read()
                with open(path + ".part", "wb") as f:
                    f.write(data)
                os.replace(path + ".part", path)
            img = AppKit.NSImage.alloc().initWithContentsOfFile_(path)
            with lock:
                icons[spell] = img
        except Exception as e:  # noqa: BLE001 - an icon that can't be fetched is drawn as its name
            print(f"auraoverlay: icon {spell}: {e}", file=sys.stderr)


def auras_from(msg):
    t = now()
    out = []
    for a in msg.get("auras", []):
        dur = a.get("duration_ms", 0) / 1000
        if dur <= 0 and not PASSIVES:
            continue
        out.append({
            "spell": a.get("spell"), "name": a.get("name") or f"spell {a.get('spell')}",
            "stacks": a.get("stacks", 1), "duration": dur,
            "deadline": t + a.get("remaining_ms", 0) / 1000 if dur > 0 else None,
            "mine": a.get("mine", False),
        })
    return out


SCHOOL_RGB = {1: (0.95, 0.85, 0.6), 2: (1.0, 0.95, 0.55), 4: (1.0, 0.55, 0.25), 8: (0.55, 0.95, 0.45),
              16: (0.55, 0.8, 1.0), 32: (0.75, 0.55, 1.0), 64: (0.95, 0.55, 0.95)}


def ticker_line(ev):
    sub, name = ev.get("sub", ""), ev.get("name") or (f"spell {ev['spell']}" if ev.get("spell") else "")
    amt = ev.get("amount", 0)
    crit = "!" if ev.get("crit") else ""
    if sub.endswith("_DAMAGE"):
        return f"{name or 'Melee'} {amt}{crit}", SCHOOL_RGB.get(ev.get("school", 1), (1, 1, 1))
    if sub.endswith("_HEAL"):
        return f"{name} +{amt}{crit}", (0.45, 1.0, 0.5)
    if sub == "SPELL_CAST_SUCCESS" and name:
        return name, (0.85, 0.85, 0.85)
    if sub.endswith("_MISSED"):
        return f"{name or 'Melee'} miss", (0.7, 0.7, 0.7)
    if sub == "PARTY_KILL":
        return "Killing blow", (1.0, 0.8, 0.3)
    return None, None


def handle(msg):
    kind = msg.get("type")
    with lock:
        if kind == "auras":
            unit = msg.get("unit")
            if unit in ("player", "target"):
                old = {a["spell"]: a.get("born", 0) for a in state[unit]}
                new = auras_from(msg)
                for a in new:
                    a["born"] = old.get(a["spell"], now())
                state[unit] = new
        elif kind == "target":
            state["target_name"] = msg.get("unit") or ""
            if not msg.get("unit"):
                state["target"] = []
        elif kind == "next":
            q = msg.get("queue") or ([{"spell": msg.get("spell"), "name": msg.get("name"), "key": msg.get("key")}] if msg.get("spell") else [])
            q = [x for x in q if x.get("spell")][:MAX_QUEUE]
            for x in q:
                want_icon(x["spell"])
            first = q[0]["spell"] if q else None
            off = [x for x in (msg.get("off_gcd") or []) if x.get("spell")][:1]
            for x in off:
                want_icon(x["spell"])
            if not q:
                state["next"] = None
            elif not state["next"] or state["next"]["queue"][0]["spell"] != first:
                state["next"] = {"queue": q, "born": now(), "off": off}
            else:
                state["next"]["queue"], state["next"]["off"] = q, off
        elif kind == "power":
            state["power"] = [(p.get("name") or f"power {p.get('type')}", p.get("amount", 0)) for p in msg.get("powers", [])]
        elif kind == "cooldowns":
            state["gcd_end"] = now() + msg.get("gcd_ms", 0) / 1000
        elif kind == "event" and msg.get("mine"):
            text, rgb = ticker_line(msg)
            if text:
                state["ticker"].append((now(), text, rgb))
                del state["ticker"][:-TICKER]


POKE = {"controller": None, "pending": False}


def poke():
    """Redraw now on the main thread, at most once per pending batch of lines."""
    c = POKE["controller"]
    if c is None or POKE["pending"]:
        return
    POKE["pending"] = True

    def run():
        POKE["pending"] = False
        c.tick_(None)
    AppHelper.callAfter(run)


def reader(done):
    for raw in sys.stdin:
        try:
            handle(json.loads(raw))
        except ValueError:
            continue
        poke()  # a new suggestion shows this frame, not on the next timer tick
    done.set()


def font(size, weight=AppKit.NSFontWeightMedium):
    # the system sans with tabular digits, so countdowns don't jitter
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


def fill(rect, rgb, alpha, radius=4):
    AppKit.NSColor.colorWithRed_green_blue_alpha_(*rgb, alpha).setFill()
    AppKit.NSBezierPath.bezierPathWithRoundedRect_xRadius_yRadius_(rect, radius, radius).fill()


def fmt_time(left):
    if left >= 60:
        return f"{int(left // 60)}:{int(left % 60):02d}"
    if left >= 10:
        return f"{int(left)}"
    return f"{left:.1f}"


def draw_bars(auras, x, y_top, colour, title):
    t = now()
    text(title, 11, (0.8, 0.8, 0.8), 0.8, AppKit.NSFontWeightSemibold).drawAtPoint_((x + 2, y_top + 4))
    y = y_top - BAR_H
    for a in auras[:MAX_BARS]:
        left = max(0.0, a["deadline"] - t) if a["deadline"] else None
        frac = left / a["duration"] if left is not None and a["duration"] > 0 else 1.0
        rgb = colour if a["mine"] else (0.55, 0.55, 0.6)
        fill(((x, y), (BAR_W, BAR_H)), (0, 0, 0), 0.55)
        fill(((x, y), (max(4, BAR_W * frac), BAR_H)), rgb, 0.75)
        flash = max(0.0, 1 - (t - a.get("born", 0)) / 0.6)  # a new aura lights up for 0.6 s
        if flash > 0:
            fill(((x - 2, y - 2), (BAR_W + 4, BAR_H + 4)), (1, 1, 0.7), 0.45 * flash, 6)
        label = a["name"] + (f"  ×{a['stacks']}" if a["stacks"] > 1 else "")
        text(label, 12.5, (1, 1, 1)).drawAtPoint_((x + 7, y + 3))
        if left is not None:
            s = text(fmt_time(left), 12.5, (1, 1, 1), 1, AppKit.NSFontWeightSemibold)
            s.drawAtPoint_((x + BAR_W - s.size().width - 7, y + 3))
        y -= BAR_H + GAP


BIG, SMALL, QGAP = (44, 32, 4) if ROTATION else (64, 48, 6)


def draw_icon(spell, name, x, y, size, alpha=1.0):
    with lock:
        img = icons.get(spell)
    fill(((x - 2, y - 2), (size + 4, size + 4)), (0, 0, 0), 0.8 * alpha, 3)
    if img is not None:
        img.drawInRect_fromRect_operation_fraction_(((x, y), (size, size)), AppKit.NSZeroRect, AppKit.NSCompositingOperationSourceOver, alpha)
    else:
        fill(((x, y), (size, size)), (0.25, 0.25, 0.3), alpha, 2)
        s = text((name or str(spell))[:10], 9, (1, 1, 1), alpha)
        s.drawAtPoint_((x + 3, y + size / 2 - 6))


def draw_queue(nxt, cx, y, t):
    """The suggestions as icons; queue[0] (biggest) is now: leftmost with --rotation, else rightmost."""
    q = nxt["queue"]
    total = BIG + (len(q) - 1) * (SMALL + QGAP)
    step = SMALL + QGAP if ROTATION else -(SMALL + QGAP)  # --rotation reads left to right
    x = cx - total / 2 if ROTATION else cx + total / 2 - BIG
    pop = max(0.0, 1 - (t - nxt["born"]) / 0.2)  # a new first suggestion pops for 0.2 s
    for i, item in enumerate(q):
        size = BIG if i == 0 else SMALL
        iy = y if i == 0 else y + (BIG - SMALL) / 2
        grow = 6 * pop if i == 0 else 0
        draw_icon(item["spell"], item.get("name"), x - grow / 2, iy - grow / 2, size + grow, 1.0 if i == 0 else 0.85)
        if item.get("key"):
            k = text(str(item["key"]), 13 if i == 0 else 11, (1, 1, 1), 1, AppKit.NSFontWeightBold)
            k.drawAtPoint_((x + size - k.size().width - 3, iy + size - k.size().height - 1))
        if i == 0 and nxt.get("off"):  # an off-GCD pick sits small on top of the press-now icon
            o = nxt["off"][0]
            osz = SMALL * 0.6
            draw_icon(o["spell"], o.get("name"), x + BIG - osz, y + BIG + 4, osz)
        if i + 1 < len(q):
            x += step if ROTATION else 0
            if not ROTATION:
                x -= SMALL + QGAP
            elif i == 0:
                x += BIG - SMALL


class View(AppKit.NSView):
    def isFlipped(self):
        return False

    def drawRect_(self, rect):
        b = self.bounds()
        w, h = b.size.width, b.size.height
        with lock:
            player = sorted(state["player"], key=lambda a: (a["deadline"] is None, a["deadline"] or 0))
            target = sorted(state["target"], key=lambda a: (not a["mine"], a["deadline"] is None, a["deadline"] or 0))
            tname = state["target_name"]
            ticker = list(state["ticker"])
            nxt, power, gcd_end = state["next"], list(state["power"]), state["gcd_end"]
        if ROTATION:  # just the icon row, under the character
            if nxt:
                draw_queue(nxt, w * 0.5, h * 0.30 - BIG, now())
            return
        y_top = h * 0.52
        title = "YOU" + "".join(f"   {n.upper()} {a}" for n, a in power)
        draw_bars(player, w * 0.5 - BAR_W - 40, y_top, (0.25, 0.7, 1.0), title)
        t = now()
        if gcd_end > t:  # the GCD as a thin bar under the centre
            frac = min(1.0, (gcd_end - t) / 1.5)
            fill(((w * 0.5 - 32, y_top - 6), (64, 4)), (0, 0, 0), 0.5, 2)
            fill(((w * 0.5 - 32, y_top - 6), (64 * frac, 4)), (1, 0.85, 0.4), 0.85, 2)
        if nxt:
            draw_queue(nxt, w * 0.5, y_top + 150, t)
        y = h * 0.52 + 40
        for born, s, rgb in reversed(ticker):
            alpha = max(0.0, min(1.0, 4 - (t - born)))  # fade out after 3-4 s
            if alpha <= 0:
                continue
            a = text(s, 15, rgb, alpha, AppKit.NSFontWeightSemibold)
            a.drawAtPoint_((w * 0.5 - a.size().width / 2, y))
            y += 21


def wow_window():
    """The largest on-screen window owned by World of Warcraft: (bounds, pid) or None."""
    best = None
    for w in Quartz.CGWindowListCopyWindowInfo(Quartz.kCGWindowListOptionOnScreenOnly, Quartz.kCGNullWindowID) or []:
        owner = str(w.get("kCGWindowOwnerName", ""))
        # the retail client's windows are owned by "Wow" (12.1.5); older builds say World of Warcraft
        if not ("World of Warcraft" in owner or owner == "Wow") or w.get("kCGWindowLayer") != 0:
            continue
        b = w["kCGWindowBounds"]
        if best is None or b["Width"] * b["Height"] > best[0]["Width"] * best[0]["Height"]:
            best = (b, w["kCGWindowOwnerPID"])
    return best


class Controller(NSObject):
    def initWithDone_(self, done):
        self = objc.super(Controller, self).init()
        self.done, self.started, self.last_find, self.found = done, time.monotonic(), 0, None
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
        NSTimer.scheduledTimerWithTimeInterval_target_selector_userInfo_repeats_(1 / 60, self, "tick:", None, True)
        return self

    def tick_(self, _):
        if self.done.is_set() and not ALWAYS and not SHOT:
            AppKit.NSApp.terminate_(None)
        if SHOT and time.monotonic() - self.started > AFTER:
            self.saveTo_(SHOT)
            AppKit.NSApp.terminate_(None)
        screen = AppKit.NSScreen.screens()[0].frame()
        if ALWAYS:
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
        """Render the view on a dark backdrop to a PNG (for docs and checks)."""
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
    done = threading.Event()
    threading.Thread(target=reader, args=(done,), daemon=True).start()
    threading.Thread(target=icon_fetcher, daemon=True).start()
    controller = Controller.alloc().initWithDone_(done)
    POKE["controller"] = controller
    app.run()


if __name__ == "__main__":
    main()
