#!/usr/bin/env python3
"""Relay between the game and the Battle.net Agent (127.0.0.1:51655 -> 127.0.0.1:51654).

The game finds the Agent through /Users/Shared/Battle.net/Agent/Agent.dat and keeps one connection
to it for the whole session. launch.sh points that file at this relay just while the game starts, so
only the game goes through here. Everything is passed through and logged.

The game's `GET /agent` gets `401 Unauthorized` and `Connection: close` (the Agent fails the game's
signature check). Directly connected, the game retries forever (~60% CPU in the Agent), and if the
Agent dies it spins on the dead socket (~120% in the game). Here the game's side stays open after the
Agent hangs up, so the game just waits and both stay idle.
"""
import argparse, asyncio, time

ap = argparse.ArgumentParser()
ap.add_argument("--listen", type=int, default=51655)
ap.add_argument("--agent", type=int, default=51654)
ap.add_argument("--log", default="/tmp/agent_relay.log")
args = ap.parse_args()

log_file = open(args.log, "a", buffering=1)
logged = {}


def log(direction, cid, data):
    # Log the first line of each distinct message a few times, then just count.
    head = data.split(b"\r\n", 1)[0][:160].decode("latin-1")
    key = (direction, head)
    logged[key] = logged.get(key, 0) + 1
    n = logged[key]
    if n <= 3 or n in (10, 100, 1000) or n % 10000 == 0:
        log_file.write(f"{time.strftime('%H:%M:%S')} c{cid} {direction} x{n} {len(data)}B {data[:600]!r}\n")


async def pipe(reader, writer, direction, cid):
    try:
        while data := await reader.read(65536):
            log(direction, cid, data)
            writer.write(data)
            await writer.drain()
    except (ConnectionError, OSError):
        pass
    finally:
        log_file.write(f"{time.strftime('%H:%M:%S')} c{cid} {direction} closed\n")


cids = iter(range(1, 1 << 30))


async def handle(game_r, game_w):
    cid = next(cids)
    log_file.write(f"{time.strftime('%H:%M:%S')} c{cid} connected\n")
    agent_r, agent_w = await asyncio.open_connection("127.0.0.1", args.agent)
    await asyncio.gather(pipe(game_r, agent_w, "game>agent", cid), pipe(agent_r, game_w, "agent>game", cid))
    for w in (game_w, agent_w):
        w.close()


async def main():
    server = await asyncio.start_server(handle, "127.0.0.1", args.listen)
    log_file.write(f"{time.strftime('%H:%M:%S')} relay :{args.listen} -> :{args.agent}\n")
    async with server:
        await server.serve_forever()


asyncio.run(main())
