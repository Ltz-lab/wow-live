package opcodes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/Ltz-lab/wow-live/protocol/version"
)

// Public names are candidate message names for the recovered numbering,
// carried forward to 70707 from public projects (WowPacketParser and a
// TrinityCore fork) by tools/publicnames.py; see docs/PUBLIC_OPCODES.md. The
// 70755 table is the 70707 one carried through the code-identity rebase
// (tools/opcoderebase.py, tools/rebasenames.py, docs/PUBLIC_OPCODES_70755.md):
// RebaseFrom names the 70707 opcode whose class code a 70755 opcode carries.
//
// Verdicts recovered from the client binary itself (research/s2c_handlers.json
// for handlers, research/c2s_writes.json for Write() signatures) outrank both
// sources: they override a public name wherever they disagree, and a name a
// verdict places elsewhere is removed from its stale opcode
// (PublicNameMovedTo). Names are still hints, not verification: a name never
// makes an opcode Verified in the table and is never used to encode or decode.

// NameConfidence grades a public name.
type NameConfidence string

const (
	// NameHigh: verified against the client binary, or an independent (not
	// merely shifted-from-retail) uncontested assignment for a message the
	// TrinityCore fork sends or handles.
	NameHigh NameConfidence = "high"
	// NameMedium: supported by the client binary, or either independent or
	// used by the fork, not both.
	NameMedium NameConfidence = "medium"
	// NameLow: only the naive retail shift, the sources disagree, or the name
	// sits on more than one opcode (DuplicateOf).
	NameLow NameConfidence = "low"
	// NameNone: no public source names this number.
	NameNone NameConfidence = "none"
)

// PublicName is the public-source name record for one opcode.
type PublicName struct {
	Opcode     string         `json:"opcode"`
	Direction  Direction      `json:"direction"`
	Group      string         `json:"group"`
	Name       string         `json:"name"`
	Confidence NameConfidence `json:"confidence"`
	Sources    string         `json:"sources"` // both, wpp-only, tc-only, conflict, none
	WPP        string         `json:"wpp,omitempty"`
	TC         string         `json:"tc,omitempty"`
	TCStatus   string         `json:"tc_status,omitempty"`

	// ReplacedPublicName is the public name a binary verdict overrode here.
	ReplacedPublicName string `json:"replaced_public_name,omitempty"`
	// PublicNameMovedTo is set when the public sources put a name here that a
	// binary verdict places at that other opcode; this entry is then unnamed.
	PublicNameMovedTo string `json:"public_name_moved_to,omitempty"`
	// DuplicateOf lists the other opcodes carrying the same name.
	DuplicateOf []string `json:"duplicate_of,omitempty"`

	// Rebased tables only (70755): the 70707 opcode this one carries, how its
	// class code matched (identical, identical-ambiguous, changed, new), the
	// 70707 grade when the rebase capped it, the name the TrinityCore fork's
	// 70755 translation puts here when it differs, and a note.
	RebaseFrom      string         `json:"rebase_from,omitempty"`
	RebaseCode      string         `json:"rebase_code,omitempty"`
	Confidence70707 NameConfidence `json:"confidence_70707,omitempty"`
	TC70755         string         `json:"tc_70755,omitempty"`
	Note            string         `json:"note,omitempty"`
}

// ID returns the opcode number.
func (p PublicName) ID() uint32 {
	v, _ := strconv.ParseUint(p.Opcode, 0, 32)
	return uint32(v)
}

// Names is the public-name table for one build.
type Names struct {
	Protocol string                    `json:"protocol"`
	Comment  string                    `json:"comment"`
	Sources  map[string]map[string]any `json:"sources"`
	Entries  []PublicName              `json:"entries"`

	by map[Direction]map[uint32]*PublicName
}

var (
	//go:embed names_1.60.1.70707.json
	names70707JSON []byte
	//go:embed names_1.60.1.70755.json
	names70755JSON []byte

	namesJSON = map[int][]byte{70707: names70707JSON, 70755: names70755JSON}
	nameTbls  = map[int]*lazyNames{}
	namesMu   sync.Mutex
)

type lazyNames struct {
	once sync.Once
	n    *Names
	err  error
}

// NamesForBuild returns the public-name table for a known build, checked
// against that build's opcode table: every entry must be a known opcode.
func NamesForBuild(build int) (*Names, error) {
	raw, ok := namesJSON[build]
	if !ok {
		return nil, fmt.Errorf("opcodes: no public-name table for build %d", build)
	}
	namesMu.Lock()
	l := nameTbls[build]
	if l == nil {
		l = &lazyNames{}
		nameTbls[build] = l
	}
	namesMu.Unlock()
	l.once.Do(func() {
		tbl, err := ForBuild(build)
		if err != nil {
			l.err = err
			return
		}
		l.n, l.err = LoadNames(raw, tbl)
	})
	return l.n, l.err
}

// ForeverNames returns the public-name table for the reference build.
func ForeverNames() (*Names, error) { return NamesForBuild(version.Forever().Build) }

// LoadNames parses a public-name document and checks it against t.
func LoadNames(raw []byte, t *Table) (*Names, error) {
	var n Names
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("opcodes: parse names: %w", err)
	}
	if t != nil && n.Protocol != t.Protocol {
		return nil, fmt.Errorf("opcodes: names are for %q, table is %q", n.Protocol, t.Protocol)
	}
	n.by = map[Direction]map[uint32]*PublicName{ClientToServer: {}, ServerToClient: {}}
	for i := range n.Entries {
		e := &n.Entries[i]
		m := n.by[e.Direction]
		if m == nil {
			return nil, fmt.Errorf("opcodes: name entry %s has invalid direction %q", e.Opcode, e.Direction)
		}
		id := e.ID()
		if _, dup := m[id]; dup {
			return nil, fmt.Errorf("opcodes: duplicate name entry %s %s", e.Direction, e.Opcode)
		}
		if t != nil && !t.Known(e.Direction, id) {
			return nil, fmt.Errorf("opcodes: name entry %s %s is not an opcode of this build", e.Direction, e.Opcode)
		}
		switch e.Confidence {
		case NameHigh, NameMedium, NameLow:
			if e.Name == "" {
				return nil, fmt.Errorf("opcodes: name entry %s has confidence %s but no name", e.Opcode, e.Confidence)
			}
		case NameNone:
		default:
			return nil, fmt.Errorf("opcodes: name entry %s has invalid confidence %q", e.Opcode, e.Confidence)
		}
		m[id] = e
	}
	return &n, nil
}

// Lookup returns the public name record for op.
func (n *Names) Lookup(dir Direction, op uint32) (PublicName, bool) {
	e, ok := n.by[dir][op]
	if !ok || e.Name == "" {
		return PublicName{}, false
	}
	return *e, true
}

// Count reports how many opcodes in dir have a public name at confidence c or
// better.
func (n *Names) Count(dir Direction, c NameConfidence) int {
	rank := map[NameConfidence]int{NameHigh: 3, NameMedium: 2, NameLow: 1}
	k := 0
	for _, e := range n.Entries {
		if e.Direction == dir && e.Name != "" && rank[e.Confidence] >= rank[c] {
			k++
		}
	}
	return k
}

// Describe labels op for logs and warnings in the reference build: the group
// locator, plus the public candidate name and its confidence when there is
// one, e.g. "ClientChat/0x1 ~SMSG_CHAT (medium)". The "~" marks an unverified
// name.
func Describe(dir Direction, op uint32) string {
	return DescribeBuild(version.Forever().Build, dir, op)
}

// DescribeBuild is Describe for a given known build.
func DescribeBuild(build int, dir Direction, op uint32) string {
	tbl, err := ForBuild(build)
	if err != nil {
		return fmt.Sprintf("%#08x", op)
	}
	g, ok := tbl.GroupOf(dir, op)
	if !ok {
		return fmt.Sprintf("%#08x", op)
	}
	s := g.Label(op)
	if n, err := NamesForBuild(build); err == nil {
		if e, ok := n.Lookup(dir, op); ok {
			s += fmt.Sprintf(" ~%s (%s)", e.Name, e.Confidence)
		}
	}
	return s
}
