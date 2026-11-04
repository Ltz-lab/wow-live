package updateobject

import (
	"fmt"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// Builders for fixtures: the inverse of DecodeFields and DecodeValues. Field
// values are given by name (as decoded: "Health", "Power[0]", ...); missing
// fields are zero, and counts and presence bits are fields too.

// Fields gives create field values per struct.
type Fields struct {
	Object, Unit, Player map[string]any
	// ActivePlayerRaw is appended verbatim for TypeActivePlayer.
	ActivePlayerRaw []byte
}

// BuildFragmentData encodes CGObject create data (with its active byte) for
// a create block of type t with visibility flags.
func BuildFragmentData(t ObjectType, flags uint8, f Fields) ([]byte, error) {
	w := codec.NewWriter()
	w.Uint8(1) // IndirectFragmentActive
	parts := []struct {
		prog []op
		vals map[string]any
	}{{objectDataCreate, f.Object}, {unitDataCreate, f.Unit}}
	switch t {
	case TypeActivePlayer, TypePlayer:
		parts = append(parts, struct {
			prog []op
			vals map[string]any
		}{playerDataCreate, f.Player})
	case TypeUnit:
	default:
		return nil, fmt.Errorf("%w: update fields of object type %d", ErrUnsupported, t)
	}
	for _, p := range parts {
		if err := writeLayout(w, p.prog, flags, mapValues(p.vals)); err != nil {
			return nil, err
		}
		w.FlushBits()
	}
	if t == TypeActivePlayer {
		w.RawBytes(f.ActivePlayerRaw)
	}
	return w.Bytes(), nil
}

// BuildValues encodes a CGObject values block that changes the given fields.
func BuildValues(object, unit, player map[string]any) ([]byte, error) {
	w := codec.NewWriter()
	w.Uint8(0) // owner
	w.Uint8(0) // fragment ids unchanged
	w.Uint8(3) // CGObject active, contents changed
	var typeMask uint32
	if len(object) > 0 {
		typeMask |= 1 << 0
	}
	if len(unit) > 0 {
		typeMask |= 1 << 5
	}
	if len(player) > 0 {
		typeMask |= 1 << 6
	}
	w.Uint32(typeMask)
	for _, p := range []struct {
		l    *deltaLayout
		vals map[string]any
	}{{&objectDataDelta, object}, {&unitDataDelta, unit}, {&playerDataDelta, player}} {
		if len(p.vals) == 0 {
			continue
		}
		if err := writeDelta(w, p.l, deltaValues(p.vals)); err != nil {
			return nil, err
		}
		w.FlushBits()
	}
	return w.Bytes(), nil
}
