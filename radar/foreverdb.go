package main

// Names from a Forever names db (WOW_FOREVER_DB) for Forever sessions, where the client's
// WDB caches start empty: creature and game-object names by entry, and quest
// names with their objectives for the MCP's quest log. Read once at start
// through the sqlite3 command line, so the radar needs no SQLite driver.

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type questInfo struct {
	Name       string      `json:"name"`
	Level      int         `json:"level,omitempty"`
	Objectives []objective `json:"objectives,omitempty"`
}

type objective struct {
	Text  string `json:"text"`
	Count int    `json:"count,omitempty"`
}

// questNames is filled by loadForeverDB; empty without a database.
var questNames = map[int32]*questInfo{}

func sqliteRows(db, query string, each func([]string)) error {
	out, err := exec.Command("sqlite3", "-readonly", "-separator", "\t", db, query).Output()
	if err != nil {
		return fmt.Errorf("sqlite3 %s: %w", db, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		each(strings.Split(sc.Text(), "\t"))
	}
	return sc.Err()
}

// loadForeverDB fills names the caches don't have; it returns how many of each it read.
func loadForeverDB(db string, w *world) (npcs, objects, quests int, err error) {
	clean := func(s string) string { return strings.ReplaceAll(s, `\n`, " ") }
	fill := func(table string, into map[uint32]string, n *int) error {
		return sqliteRows(db, "select id, name from "+table+" where name is not null and name != ''", func(f []string) {
			if len(f) < 2 {
				return
			}
			id, e := strconv.ParseUint(f[0], 10, 32)
			if e != nil {
				return
			}
			if _, ok := into[uint32(id)]; !ok {
				into[uint32(id)] = f[1]
			}
			*n++
		})
	}
	if err = fill("npcs", w.names, &npcs); err != nil {
		return
	}
	if err = fill("objects", w.goNames, &objects); err != nil {
		return
	}
	err = sqliteRows(db, "select id, name, coalesce(level, 0) from quests where name is not null", func(f []string) {
		if len(f) < 3 {
			return
		}
		id, e := strconv.Atoi(f[0])
		if e != nil {
			return
		}
		lvl, _ := strconv.Atoi(f[2])
		questNames[int32(id)] = &questInfo{Name: f[1], Level: lvl}
		quests++
	})
	if err != nil {
		return
	}
	err = sqliteRows(db, "select quest, coalesce(text, ''), coalesce(count, 0) from objectives order by quest, n", func(f []string) {
		if len(f) < 3 {
			return
		}
		id, e := strconv.Atoi(f[0])
		if e != nil {
			return
		}
		if q := questNames[int32(id)]; q != nil {
			c, _ := strconv.Atoi(f[2])
			q.Objectives = append(q.Objectives, objective{Text: clean(f[1]), Count: c})
		}
	})
	return
}
