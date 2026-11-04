// Package updateobject decodes SMSG_UPDATE_OBJECT (0x5E0000) for build
// 1.60.1.70707.
//
// Every layout here was read off the client's own decoders (addresses in the
// comments, write-up in docs/EVIDENCE.md E21). Field names follow the
// TrinityCore "forever" fork where the two agree; where they differ the client
// wins and the difference is noted. Anything the client can send but we have
// not decoded returns ErrUnsupported instead of a guess.
package updateobject

import (
	"errors"
	"fmt"
	"math"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// ErrUnsupported marks a valid but not yet decoded part of the message.
var ErrUnsupported = errors.New("updateobject: unsupported")

// GUID is the client's 128-bit object GUID.
type GUID = codec.GUID

// Vec3 is a position or direction.
type Vec3 struct{ X, Y, Z float32 }

// Pos4 is a position with facing.
type Pos4 struct{ X, Y, Z, O float32 }

// CreateMovement is the movement block of a create-object block: 21 flag bits,
// a pause-time count and then the optional sections in flag order (client
// 0x10085e31c). A nil pointer means the flag was clear.
type CreateMovement struct {
	HasEntityPosition bool
	NoBirthAnim       bool
	EnablePortals     bool
	PlayHoverAnim     bool
	ThisIsYou         bool

	Movement     *MovementUpdate // MovementUpdate: units
	Transport    *TransportInfo  // MovementTransport
	Stationary   *Pos4
	CombatVictim *GUID
	ServerTime   *uint32
	Vehicle      *VehicleCreate
	AnimKit      *AnimKitCreate
	Rotation     *uint64 // packed local rotation
	GameObject   *GameObjectCreate
	SmoothPhase  *SmoothPhasing
	SceneObject  *SceneObjectCreate
	ActivePlayer *ActivePlayerCreate
	Conversation *ConversationCreate
	Room         *GUID // housing room (client only; the fork never sets it)
	Decor        *GUID // housing decor (client only)
	MeshObject   *MeshObjectCreate
	PauseTimes   []uint32 // game objects
}

// MovementUpdate is a unit's movement state (client 0x1008597e8).
type MovementUpdate struct {
	Info           MovementInfo
	Speeds         [9]float32 // walk, run, run back, swim, swim back, flight, flight back, turn rate, pitch rate
	ForcesModMag   float32    // MovementForcesModMagnitude
	AdvFlying      [17]float32
	MovementForces []MovementForce
	Spline         *CreateSpline
}

// Speed names in MovementUpdate.Speeds order.
var SpeedNames = [9]string{"walk", "run", "run_back", "swim", "swim_back", "flight", "flight_back", "turn_rate", "pitch_rate"}

// MovementInfo is the common movement status (client 0x1008572ec), also used
// by the CMSG move messages.
type MovementInfo struct {
	Mover                GUID
	Flags                uint64
	MoveTime             uint32
	Position             Pos4
	Pitch                float32
	StepUpStartElevation float32
	MoveIndex            uint32
	GravityModifier      float32
	RemoveForcesIDs      []GUID
	HeightChangeFailed   bool
	RemoteTimeValid      bool
	HasSpline            bool // the unit follows a spline (data in MovementUpdate.Spline)
	StandingOnGameObject *GUID
	Transport            *TransportInfo
	Fall                 *FallInfo
	Inertia              *Inertia
	AdvFlying            *AdvFlyingState
	DriveStatus          *DriveStatus
}

// TransportInfo places an object on a transport (client 0x100856c74).
type TransportInfo struct {
	GUID         GUID
	Offset       Pos4
	Seat         int8
	MoveTime     uint32
	PrevMoveTime *uint32
	VehicleRecID *int32
}

// FallInfo is jump/fall state (client 0x100857114).
type FallInfo struct {
	Time         uint32
	JumpVelocity float32
	Direction    *FallDirection
}

// FallDirection is the horizontal part of a jump.
type FallDirection struct{ Sin, Cos, Speed float32 }

// Inertia is an inertial force on the mover.
type Inertia struct {
	ID       int32
	Force    Vec3
	Lifetime uint32
}

// AdvFlyingState is dragonriding-style flight velocity.
type AdvFlyingState struct{ ForwardVelocity, UpVelocity float32 }

// DriveStatus is vehicle drive state.
type DriveStatus struct {
	Speed, MovementAngle   float32
	Accelerating, Drifting bool
}

// MovementForce is one movement force (client 0x10085942c).
type MovementForce struct {
	ID              GUID
	Origin          Vec3
	Direction       Vec3
	TransportID     uint32
	Magnitude       float32
	MovementForceID int32
	DurationMs      int32
	EndTimestamp    uint32
	Flags           uint32
	Type            uint8 // 2 bits
}

// CreateSpline is the spline block of a create (client 0x1008589bc).
type CreateSpline struct {
	ID          uint32
	Destination Vec3
	Move        *SplineMove
}

// SplineMove is an active spline (client 0x100858054).
type SplineMove struct {
	Flags                uint32
	Face                 uint8 // 1 spot, 2 target, 3 angle
	Elapsed              int32
	Duration             uint32
	DurationModifier     float32
	NextDurationModifier float32
	FaceSpot             Vec3
	FaceGUID             GUID
	FaceDirection        float32
	FadeObjectTime       *uint32
	Points               []Vec3
	Filter               *SplineFilter
	SpellEffect          *SplineSpellEffect
	Jump                 *SplineJump
	Turn                 *SplineTurn
	AnimTier             *SplineAnimTier
	SpellVisual          *[16]SplineSpellVisualNode
}

// SplineFilter is the 70707 filter: count, count x (f, f), 2 flag bits
// (client 0x100857ed8; the fork's MonsterSplineFilter is a different shape).
type SplineFilter struct {
	Keys  [][2]float32
	Flags uint8
}

// SplineSpellEffect is spell-driven spline data.
type SplineSpellEffect struct {
	Target                                           GUID
	SpellVisualID, ProgressCurveID, ParabolicCurveID uint32
	JumpGravity                                      float32
}

// SplineJump is parabolic spline data.
type SplineJump struct {
	JumpGravity         float32
	StartTime, Duration uint32
}

// SplineTurn is turning spline data.
type SplineTurn struct{ StartFacing, TotalTurnRads, RadsPerSec float32 }

// SplineAnimTier is an anim-tier transition.
type SplineAnimTier struct {
	TierTransitionID         int32
	AnimTier                 uint8
	StartTime, ExtraDuration uint32
}

// SplineSpellVisualNode is one of 16 spell-visual nodes.
type SplineSpellVisualNode struct {
	SpellID                             int32
	SpellXSpellVisualID, ScriptVisualID uint32
	StartNodeIndex                      uint32
}

// VehicleCreate is the vehicle section.
type VehicleCreate struct {
	RecID            int32
	InitialRawFacing float32
}

// AnimKitCreate is the anim-kit section.
type AnimKitCreate struct{ AI, Movement, Melee uint16 }

// GameObjectCreate is the game-object section (client 0x10085db54). The client
// reads Unknown before the transport part; the fork writes it after, but the
// fork never sets it.
type GameObjectCreate struct {
	WorldEffectID uint32
	Unknown       *uint32
	Transport     *GameObjectTransport
	PathProgress  *float32
}

// GameObjectTransport is a moving transport's timing.
type GameObjectTransport struct {
	TimeOffset, NextStopTimestamp uint32
	HasNextStop, Stopped, Bit3    bool
}

// SmoothPhasing replaces one object with another across a phase change.
type SmoothPhasing struct {
	ReplaceActive, StopAnimKits bool
	ReplaceObject               *GUID
}

// SceneObjectCreate is the scene-object section (client 0x10085df28).
type SceneObjectCreate struct {
	LocalScriptData *string // 7-bit length
}

// ActivePlayerCreate is the active-player section (client 0x10085e020).
type ActivePlayerCreate struct {
	SceneInstanceIDs []uint32 // nil: absent
	Runes            *RuneState
}

// RuneState is death-knight rune state.
type RuneState struct {
	Start, Count uint8
	Cooldowns    []uint8
}

// ConversationCreate is the conversation section.
type ConversationCreate struct{ TextureKitID *uint32 }

// MeshObjectCreate is the mesh-object section (client 0x10085e210). Field
// meanings are unknown; widths are observed.
type MeshObjectCreate struct {
	GUID   GUID
	Floats [8]float32
	Byte   uint8
}

// rd wraps a codec.Reader and keeps the first error, so decoders read
// straight through and check once.
type rd struct {
	r      *codec.Reader
	err    error
	trace  *[]traceEnt // TestLayoutsAgainstClient only
	inBits bool        // inside a run of bit fields (layout.go)
	// fullNested is PlayerData's flag after its change mask (0x1008d4e40
	// keeps it at [sp+0x38]): set, the quest log entries and the
	// QuestLogQuestIdToIndex map come as full values, not nested deltas.
	fullNested bool
}

func (d *rd) fail(err error) {
	if d.err == nil {
		d.err = err
	}
}

// group starts a new flag group. Every group starts on a byte boundary: the
// client reads each one as a whole byte (u8 getter, then lsr/ubfx), and the
// fork's writer flushes after each.
func (d *rd) group() { d.r.ResetBits() }

func (d *rd) bit() bool {
	if d.err != nil {
		return false
	}
	v, err := d.r.Bit()
	d.fail(err)
	return v
}
func (d *rd) bits(n uint) uint64 {
	if d.err != nil {
		return 0
	}
	v, err := d.r.Bits(n)
	d.fail(err)
	return v
}
func (d *rd) u8() uint8 {
	if d.err != nil {
		return 0
	}
	v, err := d.r.Uint8()
	d.fail(err)
	return v
}
func (d *rd) u16() uint16 {
	if d.err != nil {
		return 0
	}
	v, err := d.r.Uint16()
	d.fail(err)
	return v
}
func (d *rd) u32() uint32 {
	if d.err != nil {
		return 0
	}
	v, err := d.r.Uint32()
	d.fail(err)
	return v
}
func (d *rd) i32() int32 { return int32(d.u32()) }
func (d *rd) u64() uint64 {
	if d.err != nil {
		return 0
	}
	v, err := d.r.Uint64()
	d.fail(err)
	return v
}
func (d *rd) f32() float32 {
	if d.err != nil {
		return 0
	}
	v, err := d.r.Float32()
	d.fail(err)
	return v
}
func (d *rd) guid() GUID {
	if d.err != nil {
		return GUID{}
	}
	v, err := d.r.GUID()
	d.fail(err)
	return v
}
func (d *rd) vec3() Vec3 { return Vec3{d.f32(), d.f32(), d.f32()} }
func (d *rd) pos4() Pos4 { return Pos4{d.f32(), d.f32(), d.f32(), d.f32()} }

// count guards a length prefix against the bytes left (each element at least
// min bytes) so corrupt input cannot allocate unbounded memory.
func (d *rd) count(n uint32, min int) int {
	if d.err != nil {
		return 0
	}
	if min > 0 && int64(n)*int64(min) > int64(d.r.Remaining()) {
		d.fail(fmt.Errorf("updateobject: count %d exceeds remaining %d bytes: %w", n, d.r.Remaining(), codec.ErrUnderflow))
		return 0
	}
	return int(n)
}

func ptr[T any](v T) *T { return &v }

// DecodeCreateMovement reads a create block's movement section.
func DecodeCreateMovement(r *codec.Reader) (*CreateMovement, error) {
	d := &rd{r: r}
	m := d.createMovement()
	if d.err != nil {
		return nil, d.err
	}
	return m, nil
}

func (d *rd) createMovement() *CreateMovement {
	m := &CreateMovement{}
	d.group()
	m.HasEntityPosition = d.bit()
	m.NoBirthAnim = d.bit()
	m.EnablePortals = d.bit()
	m.PlayHoverAnim = d.bit()
	m.ThisIsYou = d.bit()
	hasMove, hasTransport, hasStationary := d.bit(), d.bit(), d.bit()
	hasVictim, hasServerTime, hasVehicle, hasAnimKit := d.bit(), d.bit(), d.bit(), d.bit()
	hasRotation, hasGO, hasSmooth, hasScene := d.bit(), d.bit(), d.bit(), d.bit()
	hasActive, hasConv, hasRoom, hasDecor, hasMesh := d.bit(), d.bit(), d.bit(), d.bit(), d.bit()
	nPause := d.u32()

	if hasMove {
		m.Movement = d.movementUpdate()
	}
	if hasTransport {
		m.Transport = d.transport()
	}
	if hasStationary {
		m.Stationary = ptr(d.pos4())
	}
	if hasVictim {
		m.CombatVictim = ptr(d.guid())
	}
	if hasServerTime {
		m.ServerTime = ptr(d.u32())
	}
	if hasVehicle {
		m.Vehicle = &VehicleCreate{RecID: d.i32(), InitialRawFacing: d.f32()}
	}
	if hasAnimKit {
		m.AnimKit = &AnimKitCreate{AI: d.u16(), Movement: d.u16(), Melee: d.u16()}
	}
	if hasRotation {
		m.Rotation = ptr(d.u64())
	}
	if hasGO {
		g := &GameObjectCreate{WorldEffectID: d.u32()}
		d.group()
		hasUnk, isTransport, hasPath := d.bit(), d.bit(), d.bit()
		if hasUnk {
			g.Unknown = ptr(d.u32())
		}
		if isTransport {
			t := &GameObjectTransport{TimeOffset: d.u32(), NextStopTimestamp: d.u32()}
			d.group()
			t.HasNextStop, t.Stopped, t.Bit3 = d.bit(), d.bit(), d.bit()
			g.Transport = t
		}
		if hasPath {
			g.PathProgress = ptr(d.f32())
		}
		m.GameObject = g
	}
	if hasSmooth {
		d.group()
		s := &SmoothPhasing{ReplaceActive: d.bit(), StopAnimKits: d.bit()}
		if d.bit() {
			s.ReplaceObject = ptr(d.guid())
		}
		m.SmoothPhase = s
	}
	if hasScene {
		s := &SceneObjectCreate{}
		d.group()
		hasScript, hasPetBattle := d.bit(), d.bit()
		if hasScript {
			d.group()
			n := int(d.bits(7))
			if b, err := d.r.Bytes(n); err != nil {
				d.fail(err)
			} else {
				s.LocalScriptData = ptr(string(b))
			}
		}
		if hasPetBattle {
			d.fail(fmt.Errorf("%w: scene object pet battle full update (client 0x1008611b0)", ErrUnsupported))
		}
		m.SceneObject = s
	}
	if hasActive {
		a := &ActivePlayerCreate{}
		d.group()
		hasScenes, hasRunes := d.bit(), d.bit()
		if hasScenes {
			n := d.count(d.u32(), 4)
			a.SceneInstanceIDs = make([]uint32, n)
			for i := range a.SceneInstanceIDs {
				a.SceneInstanceIDs[i] = d.u32()
			}
		}
		if hasRunes {
			rs := &RuneState{Start: d.u8(), Count: d.u8()}
			rs.Cooldowns = make([]uint8, d.count(d.u32(), 1))
			for i := range rs.Cooldowns {
				rs.Cooldowns[i] = d.u8()
			}
			a.Runes = rs
		}
		m.ActivePlayer = a
	}
	if hasConv {
		c := &ConversationCreate{}
		d.group()
		if d.bit() {
			c.TextureKitID = ptr(d.u32())
		}
		m.Conversation = c
	}
	if hasRoom {
		m.Room = ptr(d.guid())
	}
	if hasDecor {
		m.Decor = ptr(d.guid())
	}
	if hasMesh {
		me := &MeshObjectCreate{GUID: d.guid()}
		for i := range me.Floats {
			me.Floats[i] = d.f32()
		}
		me.Byte = d.u8()
		m.MeshObject = me
	}
	if n := d.count(nPause, 4); n > 0 {
		m.PauseTimes = make([]uint32, n)
		for i := range m.PauseTimes {
			m.PauseTimes[i] = d.u32()
		}
	}
	return m
}

func (d *rd) movementUpdate() *MovementUpdate {
	u := &MovementUpdate{Info: d.movementInfo()}
	for i := range u.Speeds {
		u.Speeds[i] = d.f32()
	}
	nForces := d.u32()
	u.ForcesModMag = d.f32()
	for i := range u.AdvFlying {
		u.AdvFlying[i] = d.f32()
	}
	if n := d.count(nForces, 50); n > 0 {
		u.MovementForces = make([]MovementForce, n)
		for i := range u.MovementForces {
			f := &u.MovementForces[i]
			f.ID = d.guid()
			f.Origin = d.vec3()
			f.Direction = d.vec3()
			f.TransportID = d.u32()
			f.Magnitude = d.f32()
			f.MovementForceID = d.i32()
			f.DurationMs = d.i32()
			f.EndTimestamp = d.u32()
			f.Flags = d.u32()
			d.group()
			f.Type = uint8(d.bits(2))
		}
	}
	d.group()
	if d.bit() {
		u.Spline = d.createSpline()
	}
	return u
}

func (d *rd) movementInfo() MovementInfo {
	var m MovementInfo
	m.Mover = d.guid()
	m.Flags = d.u64()
	m.MoveTime = d.u32()
	m.Position = d.pos4()
	m.Pitch = d.f32()
	m.StepUpStartElevation = d.f32()
	nRemove := d.u32()
	m.MoveIndex = d.u32()
	m.GravityModifier = d.f32()
	if n := d.count(nRemove, 2); n > 0 {
		m.RemoveForcesIDs = make([]GUID, n)
		for i := range m.RemoveForcesIDs {
			m.RemoveForcesIDs[i] = d.guid()
		}
	}
	d.group()
	hasStanding, hasTransport, hasFall := d.bit(), d.bit(), d.bit()
	m.HasSpline = d.bit()
	m.HeightChangeFailed = d.bit()
	m.RemoteTimeValid = d.bit()
	hasInertia, hasAdv, hasDrive := d.bit(), d.bit(), d.bit()
	if hasStanding {
		m.StandingOnGameObject = ptr(d.guid())
	}
	if hasTransport {
		m.Transport = d.transport()
	}
	if hasFall {
		f := &FallInfo{Time: d.u32(), JumpVelocity: d.f32()}
		d.group()
		if d.bit() {
			f.Direction = &FallDirection{Sin: d.f32(), Cos: d.f32(), Speed: d.f32()}
		}
		m.Fall = f
	}
	if hasInertia {
		m.Inertia = &Inertia{ID: d.i32(), Force: d.vec3(), Lifetime: d.u32()}
	}
	if hasAdv {
		m.AdvFlying = &AdvFlyingState{ForwardVelocity: d.f32(), UpVelocity: d.f32()}
	}
	if hasDrive {
		ds := &DriveStatus{Speed: d.f32(), MovementAngle: d.f32()}
		d.group()
		ds.Accelerating, ds.Drifting = d.bit(), d.bit()
		m.DriveStatus = ds
	}
	return m
}

func (d *rd) transport() *TransportInfo {
	t := &TransportInfo{GUID: d.guid(), Offset: d.pos4(), Seat: int8(d.u8()), MoveTime: d.u32()}
	d.group()
	hasPrev, hasVehicle := d.bit(), d.bit()
	if hasPrev {
		t.PrevMoveTime = ptr(d.u32())
	}
	if hasVehicle {
		t.VehicleRecID = ptr(d.i32())
	}
	return t
}

func (d *rd) createSpline() *CreateSpline {
	s := &CreateSpline{ID: d.u32(), Destination: d.vec3()}
	d.group()
	if !d.bit() {
		return s
	}
	m := &SplineMove{}
	m.Flags = d.u32()
	m.Face = d.u8()
	m.Elapsed = d.i32()
	m.Duration = d.u32()
	m.DurationModifier = d.f32()
	m.NextDurationModifier = d.f32()
	switch m.Face {
	case 1:
		m.FaceSpot = d.vec3()
	case 2:
		m.FaceGUID = d.guid()
	case 3:
		m.FaceDirection = d.f32()
	}
	d.group()
	hasFade := d.bit()
	nPoints := uint32(d.bits(16))
	hasFilter, hasSpell, hasJump, hasTurn, hasAnim, hasVisual := d.bit(), d.bit(), d.bit(), d.bit(), d.bit(), d.bit()
	if hasFade {
		m.FadeObjectTime = ptr(d.u32())
	}
	if n := d.count(nPoints, 12); n > 0 {
		m.Points = make([]Vec3, n)
		for i := range m.Points {
			m.Points[i] = d.vec3()
		}
	}
	if hasFilter {
		f := &SplineFilter{}
		f.Keys = make([][2]float32, d.count(d.u32(), 8))
		for i := range f.Keys {
			f.Keys[i] = [2]float32{d.f32(), d.f32()}
		}
		d.group()
		f.Flags = uint8(d.bits(2))
		m.Filter = f
	}
	if hasSpell {
		m.SpellEffect = &SplineSpellEffect{Target: d.guid(), SpellVisualID: d.u32(), ProgressCurveID: d.u32(),
			ParabolicCurveID: d.u32(), JumpGravity: d.f32()}
	}
	if hasJump {
		m.Jump = &SplineJump{JumpGravity: d.f32(), StartTime: d.u32(), Duration: d.u32()}
	}
	if hasTurn {
		m.Turn = &SplineTurn{StartFacing: d.f32(), TotalTurnRads: d.f32(), RadsPerSec: d.f32()}
	}
	if hasAnim {
		m.AnimTier = &SplineAnimTier{TierTransitionID: d.i32(), AnimTier: d.u8(), StartTime: d.u32(), ExtraDuration: d.u32()}
	}
	if hasVisual {
		var v [16]SplineSpellVisualNode
		for i := range v {
			v[i] = SplineSpellVisualNode{SpellID: d.i32(), SpellXSpellVisualID: d.u32(), ScriptVisualID: d.u32(), StartNodeIndex: d.u32()}
		}
		m.SpellVisual = &v
	}
	s.Move = m
	return s
}

// Quat is a rotation quaternion.
type Quat struct{ X, Y, Z, W float32 }

// UnpackRotation decodes the create block's packed local rotation (flag
// Rotation, a u64): x in the top 22 bits, y in the next 21, z in the low 21,
// each signed and scaled by 2^21 (x) or 2^20 (y, z); w is non-negative and
// follows from |q| = 1. This is the fork's GameObject::UpdatePackedRotation
// inverted [public].
func UnpackRotation(p uint64) Quat {
	v := int64(p)
	x := float64(v>>42) / (1 << 21)
	y := float64(v<<22>>43) / (1 << 20)
	z := float64(v<<43>>43) / (1 << 20)
	w2 := 1 - (x*x + y*y + z*z)
	var w float64
	if w2 > 0 {
		w = math.Sqrt(w2)
	}
	return Quat{X: float32(x), Y: float32(y), Z: float32(z), W: float32(w)}
}
