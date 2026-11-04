package updateobject

import (
	"fmt"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// MonsterMove is SMSG_ON_MONSTER_MOVE: a unit starts (or stops) a spline.
// Layout from the fork's MonsterMove::Write / MovementSpline (checked
// against retail 12.1.5 captures: every message consumes exactly).
type MonsterMove struct {
	Mover    GUID
	SplineID uint32
	Flags    uint32
	Face     uint8
	Elapsed  int32
	MoveTime uint32 // ms for the whole path
	// Points are the path's full points. In the packed mode (most ground
	// moves) this is just the destination; the middle points are deltas.
	Points []Vec3
	// Start is where the unit is when the spline begins.
	Start Vec3
}

// Dest is where the move ends: the last point, or Start when it is a stop.
func (m *MonsterMove) Dest() Vec3 {
	if len(m.Points) == 0 {
		return m.Start
	}
	return m.Points[len(m.Points)-1]
}

// DecodeMonsterMove reads an SMSG_ON_MONSTER_MOVE body.
func DecodeMonsterMove(b []byte) (*MonsterMove, error) {
	d := &rd{r: codec.NewReader(b)}
	m := &MonsterMove{Mover: d.guid()}
	m.SplineID = d.u32()
	m.Flags = d.u32()
	m.Face = d.u8()
	m.Elapsed = d.i32()
	m.MoveTime = d.u32()
	_ = d.u32()  // FadeObjectTime
	_ = d.u8()   // Mode
	_ = d.guid() // TransportGUID
	_ = d.u8()   // VehicleSeat
	switch m.Face {
	case 1: // FACING_SPOT
		d.vec3()
	case 2: // FACING_TARGET
		d.f32()
		d.guid()
	case 3: // FACING_ANGLE
		d.f32()
	}
	d.group()
	nPoints := int(d.bits(16))
	d.bit() // VehicleExitVoluntary
	d.bit() // TaxiSmoothing
	nPacked := int(d.bits(16))
	hasFilter, hasSpellFx, hasJump, hasTurn, hasAnimTier, hasVisual := d.bit(), d.bit(), d.bit(), d.bit(), d.bit(), d.bit()
	if d.err != nil {
		return nil, d.err
	}
	if nPoints*12+nPacked*4 > d.r.Remaining() {
		return nil, fmt.Errorf("%w: %d points, %d packed, %d bytes", ErrMalformed, nPoints, nPacked, d.r.Remaining())
	}
	m.Points = make([]Vec3, nPoints)
	for i := range m.Points {
		m.Points[i] = d.vec3()
	}
	for i := 0; i < nPacked; i++ {
		d.u32()
	}
	if hasFilter {
		d.group()
		n := d.u32()
		d.f32() // BaseSpeed
		d.u16() // StartOffset
		d.f32() // DistToPrevFilterKey
		d.u16() // AddedToStart
		for i := 0; i < d.count(n, 4); i++ {
			d.u16()
			d.u16()
		}
		d.group()
		d.bits(2) // FilterFlags
	}
	if hasSpellFx {
		d.guid()
		d.u32()
		d.u32()
		d.u32()
	}
	if hasJump {
		d.f32()
		d.u32()
		d.u32()
	}
	if hasTurn {
		d.f32()
		d.f32()
		d.f32()
	}
	if hasAnimTier {
		d.i32()
		d.u8()
		d.u32()
		d.u32()
	}
	_ = hasVisual // trailing, not needed: Start is read from the end
	if d.err != nil {
		return nil, d.err
	}
	// Pos is the last 12 bytes (the fork writes it after the spline and
	// the two/three trailing bits flushed into one byte before it).
	if len(b) < 12 {
		return nil, fmt.Errorf("%w: short", ErrMalformed)
	}
	t := &rd{r: codec.NewReader(b[len(b)-12:])}
	m.Start = t.vec3()
	return m, nil
}

// DecodeMoveUpdate reads an SMSG_MOVE_UPDATE body (one MovementInfo: the
// mover's GUID, position and facing).
func DecodeMoveUpdate(b []byte) (*MovementInfo, error) {
	d := &rd{r: codec.NewReader(b)}
	m := d.movementInfo()
	if d.err != nil {
		return nil, d.err
	}
	return &m, nil
}
