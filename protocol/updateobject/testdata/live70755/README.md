# Two SMSG_UPDATE_OBJECT bodies from the live 70755 session

Cut from `~/wow/server/wow-forever-tc/captures/2026-11-05-live-goldshire-70755.pkt`
(29 minutes of the stock 70755 client in Goldshire on the private local
TrinityCore fork; the log itself is not committed). File names are the
log's packet indices; each file is the message body, without the opcode:

    go run ./tools/worldreplay -dump-op 0x5c0000 -dump-to /tmp/dump <log>
    tail -c +5 /tmp/dump/9968-conn1-*.bin > 09968_SMSG_UPDATE_OBJECT.bin

- `09968` (268.5 s): quest 179 accepted into quest log slot 1 — PlayerData
  bit 26 (`QuestLogQuestIdToIndex` change) and bit 59 (QuestLog[1]).
- `18314` (455.9 s): QuestLog[1] objective 4 progress → 2 (bit 59).

These were the session's last two kinds of skipped block.
