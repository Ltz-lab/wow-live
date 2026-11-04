package updateobject

import (
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// raw builds bytes by hand, independently of codec.Writer.
type raw []byte

func (b raw) u8(v uint8) raw   { return append(b, v) }
func (b raw) u32(v uint32) raw { return binary.LittleEndian.AppendUint32(b, v) }
func (b raw) u64(v uint64) raw { return binary.LittleEndian.AppendUint64(b, v) }
func (b raw) f(v ...float32) raw {
	for _, x := range v {
		b = b.u32(math.Float32bits(x))
	}
	return b
}

// TestActivePlayerGolden decodes a hand-assembled movement block for the
// player's own character, laid out from the client readers:
// flags 0x10085e31c, MovementInfo 0x1008572ec, MovementUpdate 0x1008597e8,
// active player 0x10085e020.
func TestActivePlayerGolden(t *testing.T) {
	speeds := []float32{2.5, 7, 4.5, 4.722222, 2.5, 7, 4.5, 3.141594, 3.141594}
	adv := make([]float32, 17)
	for i := range adv {
		adv[i] = float32(i) + 0.5
	}
	b := raw{}.
		u8(0b0000_1100). // ThisIsYou (bit 3), MovementUpdate (bit 2)
		u8(0).
		u8(0b1000_0000).                     // ActivePlayer (first flag of byte 2)
		u32(0).                              // pause times
		u8(0x01).u8(0x80).u8(0x2A).u8(0x08). // mover GUID: mask 0x8001, Lo byte0, Hi byte7
		u64(0).u32(1000).
		f(-8949.95, -132.493, 83.5312, 0.5). // Elwynn: Northshire
		f(0, 0).                             // pitch, step-up elevation
		u32(0).u32(7).f(1).                  // remove-forces count, move index, gravity
		u8(0).u8(0).                         // 9 movement-info flags, none set
		f(speeds...).
		u32(0).f(1). // forces count, mod magnitude
		f(adv...).
		u8(0). // HasSpline
		u8(0)  // active player: no scenes, no runes

	r := codec.NewReader(b)
	m, err := DecodeCreateMovement(r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d bytes left over", r.Remaining())
	}
	if !m.ThisIsYou || m.Movement == nil || m.ActivePlayer == nil || m.Stationary != nil || m.HasEntityPosition {
		t.Fatalf("flags decoded wrong: %+v", m)
	}
	mi := m.Movement.Info
	if want := (GUID{Lo: 0x2A, Hi: 0x0800000000000000}); mi.Mover != want {
		t.Fatalf("mover %v, want %v", mi.Mover, want)
	}
	if want := (Pos4{-8949.95, -132.493, 83.5312, 0.5}); mi.Position != want {
		t.Fatalf("position %+v", mi.Position)
	}
	if mi.MoveTime != 1000 || mi.MoveIndex != 7 || mi.GravityModifier != 1 {
		t.Fatalf("movement info %+v", mi)
	}
	if m.Movement.Speeds[1] != 7 || m.Movement.AdvFlying[16] != 16.5 || m.Movement.ForcesModMag != 1 {
		t.Fatalf("speeds/adv %+v", m.Movement)
	}

	// The encoder must produce the same bytes.
	w := codec.NewWriter()
	EncodeCreateMovement(w, m)
	if string(w.Bytes()) != string(b) {
		t.Fatalf("re-encoded\n %x\nwant\n %x", w.Bytes(), []byte(b))
	}
}

func everything() *CreateMovement {
	g := func(n uint64) GUID { return GUID{Lo: n, Hi: n << 56} }
	return &CreateMovement{
		HasEntityPosition: true, NoBirthAnim: true, EnablePortals: true, PlayHoverAnim: true, ThisIsYou: true,
		Movement: &MovementUpdate{
			Info: MovementInfo{
				Mover: g(1), Flags: 0x1234_5678_9ABC, MoveTime: 99, Position: Pos4{1, 2, 3, 4},
				Pitch: 0.1, StepUpStartElevation: 0.2, MoveIndex: 3, GravityModifier: 1,
				RemoveForcesIDs: []GUID{g(2), g(3)},
				HasSpline:       true, HeightChangeFailed: true, RemoteTimeValid: true,
				StandingOnGameObject: ptr(g(4)),
				Transport:            &TransportInfo{GUID: g(5), Offset: Pos4{1, 1, 1, 1}, Seat: -1, MoveTime: 5, PrevMoveTime: ptr(uint32(4)), VehicleRecID: ptr(int32(-7))},
				Fall:                 &FallInfo{Time: 6, JumpVelocity: 7.5, Direction: &FallDirection{0.5, 0.25, 3}},
				Inertia:              &Inertia{ID: -2, Force: Vec3{1, 2, 3}, Lifetime: 8},
				AdvFlying:            &AdvFlyingState{1, 2},
				DriveStatus:          &DriveStatus{Speed: 3, MovementAngle: 4, Accelerating: true, Drifting: false},
			},
			Speeds:       [9]float32{1, 2, 3, 4, 5, 6, 7, 8, 9},
			ForcesModMag: 1,
			MovementForces: []MovementForce{{ID: g(6), Origin: Vec3{1, 2, 3}, Direction: Vec3{4, 5, 6}, TransportID: 7,
				Magnitude: 8, MovementForceID: -9, DurationMs: 10, EndTimestamp: 11, Flags: 12, Type: 3}},
			Spline: &CreateSpline{ID: 13, Destination: Vec3{7, 8, 9}, Move: &SplineMove{
				Flags: 0x400, Face: 2, Elapsed: -5, Duration: 3000, DurationModifier: 1, NextDurationModifier: 1,
				FaceGUID: g(7), FadeObjectTime: ptr(uint32(14)),
				Points:      []Vec3{{1, 1, 1}, {2, 2, 2}, {3, 3, 3}},
				Filter:      &SplineFilter{Keys: [][2]float32{{1, 2}, {3, 4}}, Flags: 2},
				SpellEffect: &SplineSpellEffect{Target: g(8), SpellVisualID: 1, ProgressCurveID: 2, ParabolicCurveID: 3, JumpGravity: 4},
				Jump:        &SplineJump{JumpGravity: 19.29, StartTime: 1, Duration: 2},
				Turn:        &SplineTurn{1, 2, 3},
				AnimTier:    &SplineAnimTier{TierTransitionID: -1, AnimTier: 3, StartTime: 4, ExtraDuration: 5},
				SpellVisual: &[16]SplineSpellVisualNode{{SpellID: 1, SpellXSpellVisualID: 2, ScriptVisualID: 3, StartNodeIndex: 4}},
			}},
		},
		Transport:    &TransportInfo{GUID: g(9), Offset: Pos4{2, 2, 2, 2}, Seat: 2, MoveTime: 15},
		Stationary:   &Pos4{5, 6, 7, 8},
		CombatVictim: ptr(g(10)),
		ServerTime:   ptr(uint32(16)),
		Vehicle:      &VehicleCreate{RecID: 17, InitialRawFacing: 1.5},
		AnimKit:      &AnimKitCreate{1, 2, 3},
		Rotation:     ptr(uint64(0xDEADBEEF)),
		GameObject: &GameObjectCreate{WorldEffectID: 18, Unknown: ptr(uint32(19)),
			Transport: &GameObjectTransport{TimeOffset: 20, NextStopTimestamp: 21, HasNextStop: true, Bit3: true}, PathProgress: ptr(float32(0.75))},
		SmoothPhase:  &SmoothPhasing{ReplaceActive: true, ReplaceObject: ptr(g(11))},
		SceneObject:  &SceneObjectCreate{LocalScriptData: ptr("print('hi')")},
		ActivePlayer: &ActivePlayerCreate{SceneInstanceIDs: []uint32{22, 23}, Runes: &RuneState{Start: 0x3F, Count: 0x3F, Cooldowns: []uint8{255, 0, 128, 1, 2, 3}}},
		Conversation: &ConversationCreate{TextureKitID: ptr(uint32(24))},
		Room:         ptr(g(12)),
		Decor:        ptr(g(13)),
		MeshObject:   &MeshObjectCreate{GUID: g(14), Floats: [8]float32{1, 2, 3, 4, 5, 6, 7, 8}, Byte: 9},
		PauseTimes:   []uint32{25, 26, 27},
	}
}

func TestRoundTripEverySection(t *testing.T) {
	want := everything()
	w := codec.NewWriter()
	EncodeCreateMovement(w, want)
	r := codec.NewReader(w.Bytes())
	got, err := DecodeCreateMovement(r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d bytes left over", r.Remaining())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip differs\n got %+v\nwant %+v", got, want)
	}
}

func TestEveryTruncationFails(t *testing.T) {
	w := codec.NewWriter()
	EncodeCreateMovement(w, everything())
	full := w.Bytes()
	for n := 0; n < len(full); n++ {
		if _, err := DecodeCreateMovement(codec.NewReader(full[:n])); err == nil {
			t.Fatalf("prefix of %d/%d bytes decoded without error", n, len(full))
		}
	}
}

func TestHugeCountsAreRejected(t *testing.T) {
	b := raw{}.u8(0).u8(0).u8(0).u32(0xFFFFFFFF) // 4 billion pause times, no data
	if _, err := DecodeCreateMovement(codec.NewReader(b)); !errors.Is(err, codec.ErrUnderflow) {
		t.Fatalf("want ErrUnderflow, got %v", err)
	}
}

func TestPetBattleIsUnsupported(t *testing.T) {
	b := raw{}.u8(0).u8(0b0000_0001).u8(0).u32(0). // SceneObject (last flag of byte 1)
							u8(0b0100_0000) // HasPetBattleFullUpdate
	if _, err := DecodeCreateMovement(codec.NewReader(b)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}

// TestGameObjectClientOrder pins the one place the client and the fork
// disagree: 0x10085db54 reads the first-flag u32 before the transport part.
// It also crosses two adjacent flag groups (the transport's 3 bits, then
// SmoothPhasing's), which must sit in separate bytes.
func TestGameObjectClientOrder(t *testing.T) {
	b := raw{}.
		u8(0).u8(0b0000_0110).u8(0). // GameObject (bit 2), SmoothPhasing (bit 1) of byte 1
		u32(0).
		u32(77).           // WorldEffectID
		u8(0b1100_0000).   // Unknown, IsTransport
		u32(5).            // Unknown: read first
		u32(100).u32(200). // TimeOffset, NextStopTimestamp
		u8(0b0100_0000).   // Stopped
		u8(0b1000_0000)    // SmoothPhasing: ReplaceActive
	r := codec.NewReader(b)
	m, err := DecodeCreateMovement(r)
	if err != nil {
		t.Fatal(err)
	}
	g := m.GameObject
	if g == nil || g.WorldEffectID != 77 || g.Unknown == nil || *g.Unknown != 5 || g.Transport == nil ||
		g.Transport.TimeOffset != 100 || g.Transport.NextStopTimestamp != 200 || !g.Transport.Stopped || g.PathProgress != nil {
		t.Fatalf("game object %+v", g)
	}
	if m.SmoothPhase == nil || !m.SmoothPhase.ReplaceActive || r.Remaining() != 0 {
		t.Fatalf("smooth phasing %+v, %d left", m.SmoothPhase, r.Remaining())
	}
	w := codec.NewWriter()
	EncodeCreateMovement(w, m)
	if string(w.Bytes()) != string(b) {
		t.Fatalf("re-encoded %x, want %x", w.Bytes(), []byte(b))
	}
}
