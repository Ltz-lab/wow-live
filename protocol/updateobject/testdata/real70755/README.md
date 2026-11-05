# Real 70755 update-object bytes

Every `SMSG_UPDATE_OBJECT` payload (88 packets) of one session of the stock
WoW Forever 1.60.1.**70755** client, recorded on **a private local
TrinityCore server** (the `forever` fork, run on the owner's machine), not
on Blizzard's servers. Nothing here came from Blizzard's infrastructure.

- Source: `~/Dev/wow/server/wow-forever-tc/captures/2026-11-04-goldshire-70755.pkt`
  (TrinityCore PKT 3.1, 1,614 packets, 135 s, sha256
  `814696211609d3047e783e6e4729a67697d5d18b7551584727c544bd8af0a057`).
- Session: login, character list, world entry in Goldshire (Elwynn Forest,
  map 0), walking, a quest giver, combat with a few mobs, chat, logout. The
  character is Qweqwee, a level-61 dwarf shaman, GUID
  `0801180000000000:0000000000000003`; the account's characters are made-up
  test names.
- Exported with `~/Dev/wow/server/wow-forever-tc/tools/pktread.py <pkt> --export DIR`:
  one file per packet, payload only (no opcode, no frame header), named by
  the capture's packet sequence number. The server logs a packet before it
  compresses or encrypts it, so these are the plain bodies the client's
  readers see after `SMSG_COMPRESSED_PACKET` is inflated.

Tests: `../../real70755_test.go` (decode, re-encode, fields, values,
rotation) and `../../apply/real70755_test.go` (the whole session applied
into `state`). Findings: `docs/EVIDENCE.md` E31.
