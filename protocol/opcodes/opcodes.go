// Package opcodes holds the versioned opcode table for one shipped build.
//
// The table is data, not code: it is loaded from an embedded JSON document so a
// newly recovered numbering can be dropped in without touching the decoder.
// One document per known build (version.Known): wowf 1.60.1.70707 and
// 1.60.1.70755. Each numbering is recovered as protocol groups (see
// groups.go): every server-to-client and client-to-server opcode number is
// known, but per-message names are not shipped in the image. Only the
// messages the world connection and session loop use carry a number in
// Entries (tools/sessionentries.py), each with the client evidence for that
// build; the rest are named only by the public-name hints (names.go).
package opcodes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Ltz-lab/wow-live/protocol/version"
)

// Direction is the direction a message travels on an established connection.
type Direction string

const (
	ClientToServer Direction = "client-to-server"
	ServerToClient Direction = "server-to-client"
)

// Confidence records how much is known about an entry.
type Confidence string

const (
	// Unknown means neither the number nor the payload layout is verified.
	Unknown Confidence = "unknown"
	// NameOnly means the message name is grounded in shipped strings or type
	// names but no number or layout is verified.
	NameOnly Confidence = "name-only"
	// Verified means the number and layout were confirmed against traffic or
	// against the decrypted handler table for this exact build.
	Verified Confidence = "verified"
	// Supported means the number comes from this build's client and the name
	// is supported by its handler, but the layout was not checked field for
	// field.
	Supported Confidence = "supported"
)

// Entry is one protocol message.
type Entry struct {
	Name       string     `json:"name"`
	Direction  Direction  `json:"direction"`
	ID         *uint32    `json:"id"`
	Confidence Confidence `json:"confidence"`
	Evidence   string     `json:"evidence,omitempty"`
}

// ErrUnknownOpcode is returned for an opcode number absent from the table.
type ErrUnknownOpcode struct {
	Direction Direction
	ID        uint32
	Protocol  string // the table's protocol, e.g. "wowf 1.60.1.70755"
}

func (e *ErrUnknownOpcode) Error() string {
	return fmt.Sprintf("opcode %#08x (%s) is not in the %s table", e.ID, e.Direction, e.Protocol)
}

// ErrNameOnly is returned when a numeric lookup is attempted for a message that
// is known only by name. It exists so callers cannot silently encode a guess.
type ErrNameOnly struct{ Name string }

func (e *ErrNameOnly) Error() string {
	return fmt.Sprintf("message %s has no verified opcode number for this build", e.Name)
}

// Table is an immutable opcode table for one build.
type Table struct {
	Protocol string  `json:"protocol"`
	Product  string  `json:"product"`
	Build    int     `json:"build"`
	Status   string  `json:"status"`
	Comment  string  `json:"comment"`
	Entries  []Entry `json:"entries"`
	Groups   []Group `json:"groups"`

	byName    map[string]*Entry
	byID      map[Direction]map[uint32]*Entry
	groupByID map[Direction]map[uint16]*Group
}

var (
	//go:embed table_1.60.1.70707.json
	table70707JSON []byte
	//go:embed table_1.60.1.70755.json
	table70755JSON []byte

	tableJSON = map[int][]byte{70707: table70707JSON, 70755: table70755JSON}
	tables    = map[int]*lazyTable{}
	tablesMu  sync.Mutex
)

type lazyTable struct {
	once sync.Once
	tbl  *Table
	err  error
}

// ForBuild returns the embedded table for a known build. Every known build
// (version.Known) has one.
func ForBuild(build int) (*Table, error) {
	raw, ok := tableJSON[build]
	if !ok {
		return nil, fmt.Errorf("%w: no opcode table for build %d", version.ErrUnknownBuild, build)
	}
	tablesMu.Lock()
	l := tables[build]
	if l == nil {
		l = &lazyTable{}
		tables[build] = l
	}
	tablesMu.Unlock()
	l.once.Do(func() {
		l.tbl, l.err = Load(raw)
		if l.err == nil && l.tbl.Build != build {
			l.err = fmt.Errorf("opcodes: table for %d says build %d", build, l.tbl.Build)
		}
	})
	return l.tbl, l.err
}

// Forever returns the table for the reference build (version.Forever()).
func Forever() (*Table, error) { return ForBuild(version.Forever().Build) }

// Load parses and indexes a table document.
func Load(raw []byte) (*Table, error) {
	var t Table
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("opcodes: parse table: %w", err)
	}
	t.byName = make(map[string]*Entry, len(t.Entries))
	t.byID = map[Direction]map[uint32]*Entry{
		ClientToServer: {},
		ServerToClient: {},
	}
	for i := range t.Entries {
		e := &t.Entries[i]
		if e.Name == "" {
			return nil, fmt.Errorf("opcodes: entry %d has no name", i)
		}
		if _, dup := t.byName[e.Name]; dup {
			return nil, fmt.Errorf("opcodes: duplicate name %s", e.Name)
		}
		t.byName[e.Name] = e
		if e.ID != nil {
			m := t.byID[e.Direction]
			if m == nil {
				return nil, fmt.Errorf("opcodes: entry %s has invalid direction %q", e.Name, e.Direction)
			}
			if prev, dup := m[*e.ID]; dup {
				return nil, fmt.Errorf("opcodes: duplicate id %#06x in %s (%s and %s)", *e.ID, e.Direction, prev.Name, e.Name)
			}
			m[*e.ID] = e
		}
	}
	if err := t.indexGroups(); err != nil {
		return nil, err
	}
	return &t, nil
}

// CheckVersion fails unless the table was built for v.
func (t *Table) CheckVersion(v version.ID) error {
	if t.Build != v.Build || t.Product != v.Product {
		return fmt.Errorf("%w: table is %s %d, target is %s", version.ErrBuildMismatch, t.Product, t.Build, v)
	}
	return nil
}

// ByName returns the entry with that exact name.
func (t *Table) ByName(name string) (Entry, bool) {
	e, ok := t.byName[name]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// ByID resolves a numeric opcode. Name-only entries never match.
func (t *Table) ByID(dir Direction, id uint32) (Entry, error) {
	m, ok := t.byID[dir]
	if !ok {
		return Entry{}, fmt.Errorf("opcodes: invalid direction %q", dir)
	}
	if e, ok := m[id]; ok {
		return *e, nil
	}
	return Entry{}, &ErrUnknownOpcode{Direction: dir, ID: id, Protocol: t.Protocol}
}

// RequireID resolves a message name to its verified number.
func (t *Table) RequireID(name string) (Direction, uint32, error) {
	e, ok := t.ByName(name)
	if !ok {
		return "", 0, fmt.Errorf("opcodes: no entry named %s", name)
	}
	if e.ID == nil {
		return e.Direction, 0, &ErrNameOnly{Name: name}
	}
	return e.Direction, *e.ID, nil
}

// VerifiedCount reports how many entries carry a verified number.
func (t *Table) VerifiedCount() int {
	n := 0
	for i := range t.Entries {
		if t.Entries[i].Confidence == Verified {
			n++
		}
	}
	return n
}
