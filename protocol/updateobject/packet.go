package updateobject

import (
	"errors"
	"fmt"

	"github.com/Ltz-lab/wow-live/protocol/codec"
)

// Outer framing of SMSG_UPDATE_OBJECT, read off the client (docs/EVIDENCE.md
// E23):
//
//	0x1007fb750  message reader: u16 map, u32 block count, u8 flags
//	             (bit 7 -> +0x250, bit 6 HasRemovedObjects), [u16 destroy
//	             count, u32 GUID count, GUIDs], u32 blob size, blob
//	0x102cd65e8  pass 1 over the blob: u8 block type; types 0..2 then a GUID;
//	             any other type fails the whole message
//	0x102cd6d70  create block (types 1 and 2): u8 object type, movement
//	             (0x10085e31c), then 0x102cddad0
//	0x102cddad0  u32 size, u8 field flags, fragment ids (0x102cddf48),
//	             fragment data; the size must match what was read
//	0x102cd7480  values block (type 0): u32 size; unknown objects are skipped
//	             by that size, known ones go to 0x102cde4a8

// ErrMalformed marks input the client itself would reject.
var ErrMalformed = errors.New("updateobject: malformed")

// BlockType is the u8 that starts every block in the blob.
type BlockType uint8

// Block types. The client accepts exactly these three (0x102cd65e8).
const (
	BlockValues        BlockType = 0 // UPDATETYPE_VALUES
	BlockCreateObject  BlockType = 1 // UPDATETYPE_CREATE_OBJECT
	BlockCreateObject2 BlockType = 2 // UPDATETYPE_CREATE_OBJECT2 (newly spawned)
)

// ObjectType is the create block's u8 object type. The client switches on it
// at 0x1010a9600 (1..14); the numbering is the fork's TypeID.
type ObjectType uint8

// Object types.
const (
	TypeObject               ObjectType = 0
	TypeItem                 ObjectType = 1
	TypeContainer            ObjectType = 2
	TypeAzeriteEmpoweredItem ObjectType = 3
	TypeAzeriteItem          ObjectType = 4
	TypeUnit                 ObjectType = 5
	TypePlayer               ObjectType = 6
	TypeActivePlayer         ObjectType = 7
	TypeGameObject           ObjectType = 8
	TypeDynamicObject        ObjectType = 9
	TypeCorpse               ObjectType = 10
	TypeAreaTrigger          ObjectType = 11
	TypeSceneObject          ObjectType = 12
	TypeConversation         ObjectType = 13
	TypeMeshObject           ObjectType = 14
)

// Entity fragment ids as they appear on the wire. The 70707 registry
// (0x103832fa0, 0xb0 bytes per id, filled by per-fragment registration
// functions such as 0x102cf5a00 for CGObject) numbers them one above the
// retail ids; tag ids (200+) are unchanged.
const (
	FragmentCGObject  uint8 = 3   // updateable, indirect (registry kind 1)
	FragmentTagUnit   uint8 = 204 // tag: no data
	FragmentTagPlayer uint8 = 205 // tag: no data
	FragmentEnd       uint8 = 255
)

// UpdateObject is one SMSG_UPDATE_OBJECT message.
type UpdateObject struct {
	MapID uint16
	// Flag7 is bit 7 of the flag byte (client +0x250, passed on to the
	// apply step 0x102cd6108). The fork always sets it and calls it "unk".
	Flag7   bool
	Removed *RemovedObjects // bit 6 of the flag byte
	Blocks  []Block
}

// RemovedObjects lists objects to drop. The client marks the first
// DestroyCount GUIDs as destroyed and the rest as out of range (0x102cd627c
// compares the index against the u16).
type RemovedObjects struct {
	Destroyed  []GUID
	OutOfRange []GUID
}

// Block is one entry of the blob. Exactly one of Create and Values is set.
type Block struct {
	Type   BlockType
	GUID   GUID
	Create *CreateBlock
	Values *ValuesBlock
}

// CreateBlock is a create-object block (types 1 and 2).
type CreateBlock struct {
	ObjectType ObjectType
	Movement   *CreateMovement
	// FieldFlags is the visibility byte (UpdateFieldFlag: 0x01 owner, 0x02
	// party member, 0x04 unit-all, 0x08 empath). The client hands the field
	// readers FieldFlags|0x20, where 0x20 means "create".
	FieldFlags uint8
	// Fragments are the entity fragment ids, without the 255 terminator.
	Fragments []uint8
	// FragmentData is everything after the fragment list, up to the block
	// size. DecodeFields interprets it.
	FragmentData []byte
}

// ValuesBlock is a values (delta) block. Its contents depend on the object's
// type, which only the earlier create carries, so it is decoded in a second
// step by DecodeValues.
type ValuesBlock struct {
	Data []byte // the size-delimited body, without the u32 size
}

// Field visibility flags (UpdateFieldFlag) and the create bit the client adds.
const (
	FlagOwner       uint8 = 0x01
	FlagPartyMember uint8 = 0x02
	FlagUnitAll     uint8 = 0x04
	FlagEmpath      uint8 = 0x08
	flagCreate      uint8 = 0x20
)

// Decode reads a whole SMSG_UPDATE_OBJECT body.
func Decode(b []byte) (*UpdateObject, error) {
	r := codec.NewReader(b)
	d := &rd{r: r}
	u := d.updateObject()
	if d.err != nil {
		return nil, d.err
	}
	if r.Remaining() != 0 {
		return nil, fmt.Errorf("%w: %d bytes after the blob", ErrMalformed, r.Remaining())
	}
	return u, nil
}

func (d *rd) updateObject() *UpdateObject {
	u := &UpdateObject{MapID: d.u16()}
	nBlocks := d.u32()
	d.group()
	u.Flag7 = d.bit()
	hasRemoved := d.bit()
	if hasRemoved {
		nDestroy := d.u16()
		n := d.count(d.u32(), 2)
		if d.err == nil && int(nDestroy) > n {
			d.fail(fmt.Errorf("%w: destroy count %d > GUID count %d", ErrMalformed, nDestroy, n))
		}
		rm := &RemovedObjects{}
		for i := 0; i < n && d.err == nil; i++ {
			g := d.guid()
			if i < int(nDestroy) {
				rm.Destroyed = append(rm.Destroyed, g)
			} else {
				rm.OutOfRange = append(rm.OutOfRange, g)
			}
		}
		u.Removed = rm
	}
	size := d.u32()
	if d.err != nil {
		return nil
	}
	blob, err := d.r.Bytes(int(size))
	if err != nil {
		d.fail(fmt.Errorf("%w: blob size %d: %v", ErrMalformed, size, err))
		return nil
	}
	// Every block is at least 4 bytes (type, GUID mask), so a corrupt count
	// cannot allocate much.
	if int64(nBlocks)*4 > int64(len(blob)) {
		d.fail(fmt.Errorf("%w: %d blocks in a %d-byte blob", ErrMalformed, nBlocks, len(blob)))
		return nil
	}
	br := &rd{r: codec.NewReader(blob)}
	u.Blocks = make([]Block, 0, nBlocks)
	for i := uint32(0); i < nBlocks; i++ {
		blk := br.block()
		if br.err != nil {
			d.fail(fmt.Errorf("block %d: %w", i, br.err))
			return nil
		}
		u.Blocks = append(u.Blocks, blk)
	}
	if br.r.Remaining() != 0 {
		d.fail(fmt.Errorf("%w: %d bytes after %d blocks", ErrMalformed, br.r.Remaining(), nBlocks))
	}
	return u
}

func (d *rd) block() Block {
	b := Block{Type: BlockType(d.u8())}
	if d.err != nil {
		return b
	}
	switch b.Type {
	case BlockValues:
		b.GUID = d.guid()
		b.Values = &ValuesBlock{Data: d.sized()}
	case BlockCreateObject, BlockCreateObject2:
		b.GUID = d.guid()
		b.Create = d.createBlock()
	default:
		d.fail(fmt.Errorf("%w: block type %d (client accepts 0..2)", ErrMalformed, b.Type))
	}
	return b
}

// sized reads a u32 size and that many bytes.
func (d *rd) sized() []byte {
	n := d.u32()
	if d.err != nil {
		return nil
	}
	b, err := d.r.Bytes(int(n))
	if err != nil {
		d.fail(fmt.Errorf("%w: size %d: %v", ErrMalformed, n, err))
	}
	return b
}

func (d *rd) createBlock() *CreateBlock {
	c := &CreateBlock{ObjectType: ObjectType(d.u8())}
	if d.err != nil {
		return nil
	}
	c.Movement = d.createMovement()
	body := d.sized()
	if d.err != nil {
		return nil
	}
	br := &rd{r: codec.NewReader(body)}
	c.FieldFlags = br.u8()
	for br.err == nil {
		id := br.u8()
		if br.err != nil || id == FragmentEnd {
			break
		}
		c.Fragments = append(c.Fragments, id)
	}
	if br.err != nil {
		d.fail(fmt.Errorf("%w: fragment list: %v", ErrMalformed, br.err))
		return nil
	}
	c.FragmentData = body[br.r.Pos():]
	return c
}

// Encode writes u in the layout Decode reads. It exists to build fixtures.
func Encode(u *UpdateObject) []byte {
	w := codec.NewWriter()
	w.Uint16(u.MapID)
	var nBlocks uint32
	nBlocks = uint32(len(u.Blocks))
	w.Uint32(nBlocks)
	w.FlushBits()
	w.Bit(u.Flag7)
	w.Bit(u.Removed != nil)
	if rm := u.Removed; rm != nil {
		w.Uint16(uint16(len(rm.Destroyed)))
		w.Uint32(uint32(len(rm.Destroyed) + len(rm.OutOfRange)))
		for _, g := range rm.Destroyed {
			w.GUID(g)
		}
		for _, g := range rm.OutOfRange {
			w.GUID(g)
		}
	}
	blob := codec.NewWriter()
	for i := range u.Blocks {
		encBlock(blob, &u.Blocks[i])
	}
	w.Uint32(uint32(blob.Len()))
	w.RawBytes(blob.Bytes())
	return w.Bytes()
}

func encBlock(w *codec.Writer, b *Block) {
	w.Uint8(uint8(b.Type))
	w.GUID(b.GUID)
	switch {
	case b.Create != nil:
		c := b.Create
		w.Uint8(uint8(c.ObjectType))
		EncodeCreateMovement(w, c.Movement)
		body := codec.NewWriter()
		body.Uint8(c.FieldFlags)
		for _, id := range c.Fragments {
			body.Uint8(id)
		}
		body.Uint8(FragmentEnd)
		body.RawBytes(c.FragmentData)
		w.Uint32(uint32(body.Len()))
		w.RawBytes(body.Bytes())
	case b.Values != nil:
		w.Uint32(uint32(len(b.Values.Data)))
		w.RawBytes(b.Values.Data)
	}
}
