#!/usr/bin/env python3
"""Export every herb and ore spawn point in forever.db as world coordinates, for the radar.

forever.db has node spawns only as zone-map fractions (object_spawns: ui_map, x, y). The NPC
spawns (tc_spawns) have both the fraction and the world position, so each zone's map is fitted
to the world with a least-squares affine transform, and the node spawns go through it.

    python3 tools/nodes-export.py                  # -> nodes.json beside main.go
    go run . -nodes nodes.json ...                 # the radar's default

Output: {"entries": {"<object id>": "<name>"}, "spawns": [{"m": map, "n": name, "k": "herb"|"ore",
"x": wx, "y": wy}]} plus "fits" with each zone's residual, so a bad fit is visible.
"""
import argparse
import json
import os
import sqlite3

import numpy as np

# numpy on macOS Accelerate warns about overflow in matmul that does not happen
np.seterr(all="ignore")


HERBS = [
    "Peacebloom", "Silverleaf", "Earthroot", "Mageroyal", "Briarthorn", "Stranglekelp", "Bruiseweed",
    "Wild Steelbloom", "Grave Moss", "Kingsblood", "Liferoot", "Fadeleaf", "Goldthorn", "Khadgar's Whisker",
    "Wintersbite", "Firebloom", "Purple Lotus", "Arthas' Tears", "Sungrass", "Blindweed", "Ghost Mushroom",
    "Gromsblood", "Golden Sansam", "Dreamfoil", "Mountain Silversage", "Plaguebloom", "Icecap", "Black Lotus",
    "Swiftthistle", "Wildvine",
]


def kind(name):
    """'herb', 'ore' or None. Forever's variants ('Wilted Peacebloom', 'Poor Copper Vein') count."""
    if not name:
        return None
    if name.endswith(" Vein") or name.endswith(" Deposit"):
        return "ore"
    if any(h in name for h in HERBS):
        return "herb"
    return None


def fit(rows):
    """World (wx, wy) from map (x, y): [wx wy] = [x y 1] @ M, least squares. Returns M, rms yards."""
    a = np.array([[x, y, 1.0] for x, y, _, _ in rows])
    b = np.array([[wx, wy] for _, _, wx, wy in rows])
    m, *_ = np.linalg.lstsq(a, b, rcond=None)
    res = a @ m - b
    return m, float(np.sqrt((res ** 2).sum(axis=1).mean()))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default=os.environ.get("WOW_FOREVER_DB", os.path.expanduser("~/wow/host/data/forever.db")))
    ap.add_argument("--out", default=os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "nodes.json"))
    ap.add_argument("--max-rms", type=float, default=5.0, help="skip zones whose fit is worse than this (yards)")
    a = ap.parse_args()
    c = sqlite3.connect(f"file:{a.db}?mode=ro", uri=True)

    entries = {i: n for i, n in c.execute("select id, name from objects") if kind(n)}

    pts = {}
    for ui, mp, x, y, wx, wy in c.execute(
            "select ui_map, map, x, y, wx, wy from tc_spawns where ui_map is not null and x between 0 and 1 and y between 0 and 1"):
        pts.setdefault(ui, []).append((mp, x, y, wx, wy))
    fits = {}
    for ui, rows in pts.items():
        maps = {}
        for r in rows:
            maps[r[0]] = maps.get(r[0], 0) + 1
        mp = max(maps, key=maps.get)  # a zone map belongs to one world map; drop strays
        rows = [r[1:] for r in rows if r[0] == mp]
        if len(rows) < 20:
            continue
        m, rms = fit(rows)
        # one pass dropping outliers (NPCs on other floors, phased copies)
        res = np.hypot(*(np.array([[x, y, 1.0] for x, y, _, _ in rows]) @ m - np.array([[wx, wy] for _, _, wx, wy in rows])).T)
        keep = [r for r, e in zip(rows, res) if e < max(3 * rms, 10)]
        if len(keep) >= 20:
            m, rms = fit(keep)
        fits[ui] = (mp, m, rms, len(keep))

    spawns, skipped = [], {}
    seen = set()
    for obj, ui, x, y in c.execute("select object, ui_map, x, y from object_spawns"):
        name = entries.get(obj)
        if not name:
            continue
        f = fits.get(ui)
        if not f or f[2] > a.max_rms:
            skipped[ui] = skipped.get(ui, 0) + 1
            continue
        mp, m, _, _ = f
        wx, wy = (np.array([x, y, 1.0]) @ m).tolist()
        key = (mp, round(wx / 3), round(wy / 3), name)  # the same spot listed by two sources
        if key in seen:
            continue
        seen.add(key)
        spawns.append({"m": mp, "n": name, "k": kind(name), "x": round(wx, 1), "y": round(wy, 1)})

    out = {
        "entries": {str(i): n for i, n in sorted(entries.items())},
        "spawns": spawns,
        "fits": {str(ui): {"map": mp, "rms": round(rms, 2), "n": n} for ui, (mp, _, rms, n) in sorted(fits.items())},
    }
    with open(a.out, "w") as fh:
        json.dump(out, fh, separators=(",", ":"))
    good = sum(1 for f in fits.values() if f[2] <= a.max_rms)
    print(f"{len(spawns)} node spawns, {len(entries)} node entries, {good}/{len(fits)} zones fitted "
          f"(rms <= {a.max_rms} yd); skipped {sum(skipped.values())} spawns in {len(skipped)} unfitted zones -> {a.out}")


if __name__ == "__main__":
    main()
