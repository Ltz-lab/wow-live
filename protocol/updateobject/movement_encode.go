package updateobject

import "github.com/Ltz-lab/wow-live/protocol/codec"

// Each flag group below starts with w.FlushBits(): groups are byte aligned
// (see rd.group).

// EncodeCreateMovement writes m in the layout DecodeCreateMovement reads. It
// exists to build fixtures; presence flags are derived from nil pointers.
func EncodeCreateMovement(w *codec.Writer, m *CreateMovement) {
	w.FlushBits()
	for _, b := range []bool{
		m.HasEntityPosition, m.NoBirthAnim, m.EnablePortals, m.PlayHoverAnim, m.ThisIsYou,
		m.Movement != nil, m.Transport != nil, m.Stationary != nil,
		m.CombatVictim != nil, m.ServerTime != nil, m.Vehicle != nil, m.AnimKit != nil,
		m.Rotation != nil, m.GameObject != nil, m.SmoothPhase != nil, m.SceneObject != nil,
		m.ActivePlayer != nil, m.Conversation != nil, m.Room != nil, m.Decor != nil, m.MeshObject != nil,
	} {
		w.Bit(b)
	}
	w.Uint32(uint32(len(m.PauseTimes)))
	if m.Movement != nil {
		encMovementUpdate(w, m.Movement)
	}
	if m.Transport != nil {
		encTransport(w, m.Transport)
	}
	if p := m.Stationary; p != nil {
		encPos4(w, *p)
	}
	if m.CombatVictim != nil {
		w.GUID(*m.CombatVictim)
	}
	if m.ServerTime != nil {
		w.Uint32(*m.ServerTime)
	}
	if v := m.Vehicle; v != nil {
		w.Int32(v.RecID)
		w.Float32(v.InitialRawFacing)
	}
	if a := m.AnimKit; a != nil {
		w.Uint16(a.AI)
		w.Uint16(a.Movement)
		w.Uint16(a.Melee)
	}
	if m.Rotation != nil {
		w.Uint64(*m.Rotation)
	}
	if g := m.GameObject; g != nil {
		w.Uint32(g.WorldEffectID)
		w.FlushBits()
		w.Bit(g.Unknown != nil)
		w.Bit(g.Transport != nil)
		w.Bit(g.PathProgress != nil)
		if g.Unknown != nil {
			w.Uint32(*g.Unknown)
		}
		if t := g.Transport; t != nil {
			w.Uint32(t.TimeOffset)
			w.Uint32(t.NextStopTimestamp)
			w.FlushBits()
			w.Bit(t.HasNextStop)
			w.Bit(t.Stopped)
			w.Bit(t.Bit3)
		}
		if g.PathProgress != nil {
			w.Float32(*g.PathProgress)
		}
	}
	if s := m.SmoothPhase; s != nil {
		w.FlushBits()
		w.Bit(s.ReplaceActive)
		w.Bit(s.StopAnimKits)
		w.Bit(s.ReplaceObject != nil)
		if s.ReplaceObject != nil {
			w.GUID(*s.ReplaceObject)
		}
	}
	if s := m.SceneObject; s != nil {
		w.FlushBits()
		w.Bit(s.LocalScriptData != nil)
		w.Bit(false) // pet battle full update: not supported
		if s.LocalScriptData != nil {
			w.FlushBits()
			_ = w.Bits(uint64(len(*s.LocalScriptData)), 7)
			w.RawBytes([]byte(*s.LocalScriptData))
		}
	}
	if a := m.ActivePlayer; a != nil {
		w.FlushBits()
		w.Bit(a.SceneInstanceIDs != nil)
		w.Bit(a.Runes != nil)
		if a.SceneInstanceIDs != nil {
			w.Uint32(uint32(len(a.SceneInstanceIDs)))
			for _, id := range a.SceneInstanceIDs {
				w.Uint32(id)
			}
		}
		if r := a.Runes; r != nil {
			w.Uint8(r.Start)
			w.Uint8(r.Count)
			w.Uint32(uint32(len(r.Cooldowns)))
			for _, c := range r.Cooldowns {
				w.Uint8(c)
			}
		}
	}
	if c := m.Conversation; c != nil {
		w.FlushBits()
		w.Bit(c.TextureKitID != nil)
		if c.TextureKitID != nil {
			w.Uint32(*c.TextureKitID)
		}
	}
	if m.Room != nil {
		w.GUID(*m.Room)
	}
	if m.Decor != nil {
		w.GUID(*m.Decor)
	}
	if me := m.MeshObject; me != nil {
		w.GUID(me.GUID)
		for _, f := range me.Floats {
			w.Float32(f)
		}
		w.Uint8(me.Byte)
	}
	for _, p := range m.PauseTimes {
		w.Uint32(p)
	}
	w.FlushBits()
}

func encVec3(w *codec.Writer, v Vec3) {
	w.Float32(v.X)
	w.Float32(v.Y)
	w.Float32(v.Z)
}

func encPos4(w *codec.Writer, p Pos4) {
	w.Float32(p.X)
	w.Float32(p.Y)
	w.Float32(p.Z)
	w.Float32(p.O)
}

func encMovementUpdate(w *codec.Writer, u *MovementUpdate) {
	encMovementInfo(w, &u.Info)
	for _, s := range u.Speeds {
		w.Float32(s)
	}
	w.Uint32(uint32(len(u.MovementForces)))
	w.Float32(u.ForcesModMag)
	for _, f := range u.AdvFlying {
		w.Float32(f)
	}
	for _, f := range u.MovementForces {
		w.GUID(f.ID)
		encVec3(w, f.Origin)
		encVec3(w, f.Direction)
		w.Uint32(f.TransportID)
		w.Float32(f.Magnitude)
		w.Int32(f.MovementForceID)
		w.Int32(f.DurationMs)
		w.Uint32(f.EndTimestamp)
		w.Uint32(f.Flags)
		w.FlushBits()
		_ = w.Bits(uint64(f.Type), 2)
	}
	w.FlushBits()
	w.Bit(u.Spline != nil)
	if u.Spline != nil {
		encCreateSpline(w, u.Spline)
	}
}

func encMovementInfo(w *codec.Writer, m *MovementInfo) {
	w.GUID(m.Mover)
	w.Uint64(m.Flags)
	w.Uint32(m.MoveTime)
	encPos4(w, m.Position)
	w.Float32(m.Pitch)
	w.Float32(m.StepUpStartElevation)
	w.Uint32(uint32(len(m.RemoveForcesIDs)))
	w.Uint32(m.MoveIndex)
	w.Float32(m.GravityModifier)
	for _, g := range m.RemoveForcesIDs {
		w.GUID(g)
	}
	w.FlushBits()
	w.Bit(m.StandingOnGameObject != nil)
	w.Bit(m.Transport != nil)
	w.Bit(m.Fall != nil)
	w.Bit(m.HasSpline)
	w.Bit(m.HeightChangeFailed)
	w.Bit(m.RemoteTimeValid)
	w.Bit(m.Inertia != nil)
	w.Bit(m.AdvFlying != nil)
	w.Bit(m.DriveStatus != nil)
	if m.StandingOnGameObject != nil {
		w.GUID(*m.StandingOnGameObject)
	}
	if m.Transport != nil {
		encTransport(w, m.Transport)
	}
	if f := m.Fall; f != nil {
		w.Uint32(f.Time)
		w.Float32(f.JumpVelocity)
		w.FlushBits()
		w.Bit(f.Direction != nil)
		if f.Direction != nil {
			w.Float32(f.Direction.Sin)
			w.Float32(f.Direction.Cos)
			w.Float32(f.Direction.Speed)
		}
	}
	if in := m.Inertia; in != nil {
		w.Int32(in.ID)
		encVec3(w, in.Force)
		w.Uint32(in.Lifetime)
	}
	if a := m.AdvFlying; a != nil {
		w.Float32(a.ForwardVelocity)
		w.Float32(a.UpVelocity)
	}
	if ds := m.DriveStatus; ds != nil {
		w.Float32(ds.Speed)
		w.Float32(ds.MovementAngle)
		w.FlushBits()
		w.Bit(ds.Accelerating)
		w.Bit(ds.Drifting)
	}
}

func encTransport(w *codec.Writer, t *TransportInfo) {
	w.GUID(t.GUID)
	encPos4(w, t.Offset)
	w.Uint8(uint8(t.Seat))
	w.Uint32(t.MoveTime)
	w.FlushBits()
	w.Bit(t.PrevMoveTime != nil)
	w.Bit(t.VehicleRecID != nil)
	if t.PrevMoveTime != nil {
		w.Uint32(*t.PrevMoveTime)
	}
	if t.VehicleRecID != nil {
		w.Int32(*t.VehicleRecID)
	}
}

func encCreateSpline(w *codec.Writer, s *CreateSpline) {
	w.Uint32(s.ID)
	encVec3(w, s.Destination)
	w.FlushBits()
	w.Bit(s.Move != nil)
	m := s.Move
	if m == nil {
		return
	}
	w.Uint32(m.Flags)
	w.Uint8(m.Face)
	w.Int32(m.Elapsed)
	w.Uint32(m.Duration)
	w.Float32(m.DurationModifier)
	w.Float32(m.NextDurationModifier)
	switch m.Face {
	case 1:
		encVec3(w, m.FaceSpot)
	case 2:
		w.GUID(m.FaceGUID)
	case 3:
		w.Float32(m.FaceDirection)
	}
	w.FlushBits()
	w.Bit(m.FadeObjectTime != nil)
	_ = w.Bits(uint64(len(m.Points)), 16)
	w.Bit(m.Filter != nil)
	w.Bit(m.SpellEffect != nil)
	w.Bit(m.Jump != nil)
	w.Bit(m.Turn != nil)
	w.Bit(m.AnimTier != nil)
	w.Bit(m.SpellVisual != nil)
	if m.FadeObjectTime != nil {
		w.Uint32(*m.FadeObjectTime)
	}
	for _, p := range m.Points {
		encVec3(w, p)
	}
	if f := m.Filter; f != nil {
		w.Uint32(uint32(len(f.Keys)))
		for _, k := range f.Keys {
			w.Float32(k[0])
			w.Float32(k[1])
		}
		w.FlushBits()
		_ = w.Bits(uint64(f.Flags), 2)
	}
	if e := m.SpellEffect; e != nil {
		w.GUID(e.Target)
		w.Uint32(e.SpellVisualID)
		w.Uint32(e.ProgressCurveID)
		w.Uint32(e.ParabolicCurveID)
		w.Float32(e.JumpGravity)
	}
	if j := m.Jump; j != nil {
		w.Float32(j.JumpGravity)
		w.Uint32(j.StartTime)
		w.Uint32(j.Duration)
	}
	if t := m.Turn; t != nil {
		w.Float32(t.StartFacing)
		w.Float32(t.TotalTurnRads)
		w.Float32(t.RadsPerSec)
	}
	if a := m.AnimTier; a != nil {
		w.Int32(a.TierTransitionID)
		w.Uint8(a.AnimTier)
		w.Uint32(a.StartTime)
		w.Uint32(a.ExtraDuration)
	}
	if v := m.SpellVisual; v != nil {
		for _, n := range v {
			w.Int32(n.SpellID)
			w.Uint32(n.SpellXSpellVisualID)
			w.Uint32(n.ScriptVisualID)
			w.Uint32(n.StartNodeIndex)
		}
	}
}
