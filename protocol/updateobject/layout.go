package updateobject

import (
	"fmt"

	"strconv"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// A layout is a small program that describes one update-field struct's create
// data in the order the client reads it. It is interpreted by readLayout and
// writeLayout, so the decoder and the fixture encoder cannot drift apart.
//
// Ops:
//   - a value of a fixed width (u8 ... guid), byte aligned
//   - bit / bits(n): a bit field; a run of them forms one byte-aligned group
//   - when(mask): body only if the visibility flags share a bit with mask
//     (the fork's EnumFlag::HasFlag; the client tests the flags argument)
//   - repeat(n): body n times, names suffixed with [i]
//   - each(count): body once per element of an earlier count field
//   - opt(bit): body only if an earlier bit field is set
//   - cstr(len): a sized C string (client 0x1006c83a0): the length counts
//     the NUL, and a length of 0 or 1 reads nothing
//   - unsupported: a valid part not decoded yet; reading it fails
type opKind uint8

const (
	opValue opKind = iota
	opBit
	opBits
	opWhen
	opRepeat
	opEach
	opOpt
	opString  // bytes whose length is an earlier bits/count field
	opCString // a sized C string: the length counts the NUL; 0 or 1 reads nothing
	opFlush   // end the current bit group (the fork's FlushBits)
	opUnsupported
)

// Width is a field's wire type.
type Width uint8

// Wire widths.
const (
	WU8 Width = iota + 1
	WI8
	WU16
	WI16
	WU32
	WI32
	WU64
	WI64
	WF32
	WGUID
	WBit
	WBits
	WString
)

func (w Width) String() string {
	return [...]string{"?", "u8", "i8", "u16", "i16", "u32", "i32", "u64", "i64", "f32", "guid", "bit", "bits", "string"}[w]
}

type op struct {
	kind  opKind
	name  string
	width Width
	n     int    // bits width, repeat count
	mask  uint8  // when
	ref   string // each/opt/string: the field it depends on
	body  []op
	why   string // unsupported: what is missing
}

func val(name string, w Width) op      { return op{kind: opValue, name: name, width: w} }
func bit(name string) op               { return op{kind: opBit, name: name, width: WBit} }
func bitsN(name string, n int) op      { return op{kind: opBits, name: name, width: WBits, n: n} }
func when(mask uint8, body ...op) op   { return op{kind: opWhen, mask: mask, body: body} }
func repeat(n int, body ...op) op      { return op{kind: opRepeat, n: n, body: body} }
func each(count string, body ...op) op { return op{kind: opEach, ref: count, body: body} }
func opt(flag string, body ...op) op   { return op{kind: opOpt, ref: flag, body: body} }
func str(name, length string) op       { return op{kind: opString, name: name, width: WString, ref: length} }
func cstr(name, length string) op {
	return op{kind: opCString, name: name, width: WString, ref: length}
}
func flush() op                 { return op{kind: opFlush} }
func unsupported(why string) op { return op{kind: opUnsupported, why: why} }
func u8(name string) op         { return val(name, WU8) }
func i8(name string) op         { return val(name, WI8) }
func u16(name string) op        { return val(name, WU16) }
func i16(name string) op        { return val(name, WI16) }
func u32(name string) op        { return val(name, WU32) }
func i32(name string) op        { return val(name, WI32) }
func u64(name string) op        { return val(name, WU64) }
func i64(name string) op        { return val(name, WI64) }
func f32(name string) op        { return val(name, WF32) }
func guid(name string) op       { return val(name, WGUID) }
func ops(o ...op) []op          { return o }
func prefixed(prefix string, o []op) []op {
	return renamed(o, func(s string) string { return prefix + s })
}

func renamed(o []op, f func(string) string) []op {
	out := make([]op, len(o))
	for i, x := range o {
		x.name = mapName(x.name, f)
		x.ref = mapName(x.ref, f)
		x.body = renamed(x.body, f)
		out[i] = x
	}
	return out
}

func mapName(s string, f func(string) string) string {
	if s == "" {
		return ""
	}
	return f(s)
}

// Field is one decoded update field. Value holds the Go type of its width:
// uint8, int8, uint16, int16, uint32, int32, uint64, int64, float32, GUID,
// bool (bit), uint64 (bits) or string.
type Field struct {
	Name  string
	Width Width
	Value any
}

func (f Field) String() string { return fmt.Sprintf("%s(%s)=%v", f.Name, f.Width, f.Value) }

// fieldSet indexes decoded fields by name.
type fieldSet struct {
	list  []Field
	index map[string]int
}

func (s *fieldSet) add(f Field) {
	if s.index == nil {
		s.index = make(map[string]int)
	}
	s.index[f.Name] = len(s.list)
	s.list = append(s.list, f)
}

func (s *fieldSet) get(name string) (any, bool) {
	i, ok := s.index[name]
	if !ok {
		return nil, false
	}
	return s.list[i].Value, true
}

// count returns an integer field's value, for each/string.
func (s *fieldSet) count(name string) (int, error) {
	v, ok := s.get(name)
	if !ok {
		return 0, fmt.Errorf("updateobject: layout refers to %q before reading it", name)
	}
	switch x := v.(type) {
	case nil:
		return 0, nil
	case uint8:
		return int(x), nil
	case uint16:
		return int(x), nil
	case uint32:
		return int(x), nil
	case uint64:
		return int(x), nil
	case int32:
		if x < 0 {
			return 0, fmt.Errorf("%w: negative count %s=%d", ErrMalformed, name, x)
		}
		return int(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("updateobject: %q is not a count", name)
}

func minBytes(w Width) int {
	switch w {
	case WU8, WI8:
		return 1
	case WU16, WI16, WGUID:
		return 2
	case WU32, WI32, WF32:
		return 4
	case WU64, WI64:
		return 8
	}
	return 0
}

// bodyMin is a lower bound on the bytes one iteration of body reads, used to
// reject absurd counts before looping.
func bodyMin(body []op, flags uint8) int {
	n := 0
	for _, o := range body {
		switch o.kind {
		case opValue:
			n += minBytes(o.width)
		case opRepeat:
			n += o.n * bodyMin(o.body, flags)
		case opWhen:
			if flags&o.mask != 0 {
				n += bodyMin(o.body, flags)
			}
		}
	}
	return n
}

// readLayout runs prog over d with the given visibility flags.
func (d *rd) readLayout(prog []op, flags uint8, out *fieldSet) {
	d.runOps(prog, flags, out, "")
}

func idx(suffix string, i int) string { return suffix + "[" + strconv.Itoa(i) + "]" }

// traceRead records a primitive read for TestLayoutsAgainstClient: the
// position and the client getter it corresponds to (1, 2, 4, 8, f, G, D).
func (d *rd) traceRead(w string, n int) {
	if d.trace != nil && d.err == nil {
		*d.trace = append(*d.trace, traceEnt{Pos: d.r.Pos(), W: w, N: n})
	}
}

type traceEnt struct {
	Pos int
	W   string
	N   int
}

func getterOf(w Width) string {
	switch w {
	case WU8, WI8:
		return "1"
	case WU16, WI16:
		return "2"
	case WU32, WI32:
		return "4"
	case WU64, WI64:
		return "8"
	case WF32:
		return "f"
	case WGUID:
		return "G"
	}
	return "?"
}

// endsBitGroup reports whether o is a byte-sized read, which closes a run of
// bit fields. Structural ops (when, repeat, each, opt) do not: a bit inside a
// when belongs to the surrounding group.
func endsBitGroup(o op) bool {
	return o.kind == opValue || o.kind == opString || o.kind == opCString || o.kind == opUnsupported || o.kind == opFlush
}

func (d *rd) runOps(prog []op, flags uint8, out *fieldSet, suffix string) {
	for _, o := range prog {
		if d.err != nil {
			return
		}
		if endsBitGroup(o) {
			d.inBits = false
		}
		switch o.kind {
		case opValue:
			d.traceRead(getterOf(o.width), 0)
			out.add(Field{Name: o.name + suffix, Width: o.width, Value: d.value(o.width)})
		case opBit, opBits:
			if !d.inBits {
				d.group()
				d.inBits = true
			}
			if o.kind == opBit {
				// The client reads a bit group a byte at a time.
				if d.r.Aligned() {
					d.traceRead("1", 0)
				}
				out.add(Field{Name: o.name + suffix, Width: WBit, Value: d.bit()})
			} else {
				v := d.bitsTraced(uint(o.n))
				out.add(Field{Name: o.name + suffix, Width: WBits, Value: v})
			}
		case opWhen:
			if flags&o.mask != 0 {
				d.runOps(o.body, flags, out, suffix)
			}
		case opRepeat:
			for i := 0; i < o.n; i++ {
				d.runOps(o.body, flags, out, idx(suffix, i))
			}
		case opEach:
			n, err := out.count(o.ref + suffix)
			if err != nil {
				d.fail(err)
				return
			}
			if m := bodyMin(o.body, flags); m > 0 {
				n = d.count(uint32(n), m)
			}
			for i := 0; i < n && d.err == nil; i++ {
				d.runOps(o.body, flags, out, idx(suffix, i))
			}
		case opOpt:
			n, err := out.count(o.ref + suffix)
			if err != nil {
				d.fail(err)
				return
			}
			if n != 0 {
				d.runOps(o.body, flags, out, suffix)
			}
		case opString:
			n, err := out.count(o.ref + suffix)
			if err != nil {
				d.fail(err)
				return
			}
			d.traceRead("D", n)
			b, err := d.r.Bytes(n)
			if err != nil {
				d.fail(err)
				return
			}
			out.add(Field{Name: o.name + suffix, Width: WString, Value: string(b)})
		case opCString:
			n, err := out.count(o.ref + suffix)
			if err != nil {
				d.fail(err)
				return
			}
			if n < 2 {
				out.add(Field{Name: o.name + suffix, Width: WString, Value: ""})
				continue
			}
			d.traceRead("D", n)
			b, err := d.r.Bytes(n)
			if err != nil {
				d.fail(err)
				return
			}
			// The client keeps the string only if it ends in a NUL.
			v := ""
			if b[n-1] == 0 {
				v = string(b[:n-1])
			}
			out.add(Field{Name: o.name + suffix, Width: WString, Value: v})
		case opUnsupported:
			d.fail(fmt.Errorf("%w: %s", ErrUnsupported, o.why))
			return
		}
	}
}

// bitsTraced reads n bits, tracing each new byte the read starts.
func (d *rd) bitsTraced(n uint) uint64 {
	var v uint64
	for i := uint(0); i < n; i++ {
		if d.r.Aligned() {
			d.traceRead("1", 0)
		}
		b := d.bit()
		v <<= 1
		if b {
			v |= 1
		}
	}
	return v
}

func (d *rd) value(w Width) any {
	switch w {
	case WU8:
		return d.u8()
	case WI8:
		return int8(d.u8())
	case WU16:
		return d.u16()
	case WI16:
		return int16(d.u16())
	case WU32:
		return d.u32()
	case WI32:
		return d.i32()
	case WU64:
		return d.u64()
	case WI64:
		return int64(d.u64())
	case WF32:
		return d.f32()
	case WGUID:
		return d.guid()
	}
	d.fail(fmt.Errorf("updateobject: bad width %d", w))
	return nil
}

// valueSource supplies field values to writeLayout by full name (with [i]
// suffixes); nil means zero.
type valueSource func(name string, w Width) any

// mapValues is a valueSource over a map.
func mapValues(m map[string]any) valueSource {
	return func(name string, _ Width) any { return m[name] }
}

// writeLayout is readLayout's inverse. Counts and presence bits come from
// the value source like any other field.
func writeLayout(w *codec.Writer, prog []op, flags uint8, vals valueSource) error {
	// Values are fetched once per name, so a count and its loop agree.
	seen := map[string]any{}
	memo := func(name string, wd Width) any {
		if v, ok := seen[name]; ok {
			return v
		}
		v := vals(name, wd)
		seen[name] = v
		return v
	}
	inBits := false
	return writeOps(w, prog, flags, memo, "", &inBits)
}

func writeOps(w *codec.Writer, prog []op, flags uint8, src valueSource, suffix string, inBits *bool) error {
	get := src
	lookupCount := func(name string) (int, error) {
		fs := fieldSet{}
		fs.add(Field{Name: name, Value: src(name, 0)})
		return fs.count(name)
	}
	for _, o := range prog {
		if endsBitGroup(o) {
			*inBits = false
		}
		switch o.kind {
		case opValue:
			if err := writeValue(w, o.width, get(o.name+suffix, o.width)); err != nil {
				return fmt.Errorf("%s: %w", o.name+suffix, err)
			}
		case opBit, opBits:
			if !*inBits {
				w.FlushBits()
				*inBits = true
			}
			v := get(o.name+suffix, o.width)
			if o.kind == opBit {
				b, _ := v.(bool)
				w.Bit(b)
			} else {
				u, err := toU64(v)
				if err != nil {
					return fmt.Errorf("%s: %w", o.name+suffix, err)
				}
				if err := w.Bits(u, uint(o.n)); err != nil {
					return err
				}
			}
		case opWhen:
			if flags&o.mask != 0 {
				if err := writeOps(w, o.body, flags, src, suffix, inBits); err != nil {
					return err
				}
			}
		case opRepeat:
			for i := 0; i < o.n; i++ {
				if err := writeOps(w, o.body, flags, src, idx(suffix, i), inBits); err != nil {
					return err
				}
			}
		case opEach, opOpt:
			n, err := lookupCount(o.ref + suffix)
			if err != nil {
				return err
			}
			if o.kind == opOpt {
				if n != 0 {
					if err := writeOps(w, o.body, flags, src, suffix, inBits); err != nil {
						return err
					}
				}
				continue
			}
			for i := 0; i < n; i++ {
				if err := writeOps(w, o.body, flags, src, idx(suffix, i), inBits); err != nil {
					return err
				}
			}
		case opString:
			s, _ := get(o.name+suffix, o.width).(string)
			w.RawBytes([]byte(s))
		case opCString:
			n, err := lookupCount(o.ref + suffix)
			if err != nil {
				return err
			}
			s, _ := get(o.name+suffix, o.width).(string)
			if n >= 2 {
				if len(s)+1 != n {
					return fmt.Errorf("%s: length %d does not fit %q and its NUL", o.name+suffix, n, s)
				}
				w.RawBytes(append([]byte(s), 0))
			}
		case opUnsupported:
			return fmt.Errorf("%w: %s", ErrUnsupported, o.why)
		}
	}
	return nil
}

func toU64(v any) (uint64, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case uint8:
		return uint64(x), nil
	case int8:
		return uint64(x), nil
	case uint16:
		return uint64(x), nil
	case int16:
		return uint64(x), nil
	case uint32:
		return uint64(x), nil
	case int32:
		return uint64(x), nil
	case uint64:
		return x, nil
	case int64:
		return uint64(x), nil
	case int:
		return uint64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("cannot write %T as an integer", v)
}

func writeValue(w *codec.Writer, wd Width, v any) error {
	switch wd {
	case WF32:
		switch x := v.(type) {
		case nil:
			w.Float32(0)
		case float32:
			w.Float32(x)
		case float64:
			w.Float32(float32(x))
		default:
			return fmt.Errorf("cannot write %T as f32", v)
		}
		return nil
	case WGUID:
		switch x := v.(type) {
		case nil:
			w.GUID(GUID{})
		case GUID:
			w.GUID(x)
		default:
			return fmt.Errorf("cannot write %T as guid", v)
		}
		return nil
	}
	u, err := toU64(v)
	if err != nil {
		return err
	}
	switch wd {
	case WU8, WI8:
		w.Uint8(uint8(u))
	case WU16, WI16:
		w.Uint16(uint16(u))
	case WU32, WI32:
		w.Uint32(uint32(u))
	case WU64, WI64:
		w.Uint64(u)
	default:
		return fmt.Errorf("bad width %v", wd)
	}
	return nil
}

// cat joins layout fragments.
func cat(parts ...[]op) []op {
	var out []op
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
