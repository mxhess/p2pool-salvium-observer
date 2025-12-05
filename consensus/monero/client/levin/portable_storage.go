package levin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	PortableStorageSignatureA    uint32 = 0x01011101
	PortableStorageSignatureB    uint32 = 0x01020101
	PortableStorageFormatVersion byte   = 0x01

	PortableRawSizeMarkMask  byte   = 0x03
	PortableRawSizeMarkByte  byte   = 0x00
	PortableRawSizeMarkWord  uint16 = 0x01
	PortableRawSizeMarkDword uint32 = 0x02
	PortableRawSizeMarkInt64 uint64 = 0x03
)

type Entry struct {
	Name         string
	Serializable Serializable `json:"-,omitempty"`
	Value        interface{}
}

func (e Entry) String() string {
	v, ok := e.Value.(string)
	if !ok {
		panic(fmt.Errorf("interface couldnt be casted to string"))
	}

	return v
}

func (e Entry) Uint8() uint8 {
	v, ok := e.Value.(uint8)
	if !ok {
		panic(fmt.Errorf("interface couldnt be casted to uint8"))
	}

	return v
}

func (e Entry) Uint16() uint16 {
	v, ok := e.Value.(uint16)
	if !ok {
		panic(fmt.Errorf("interface couldnt be casted to uint16"))
	}

	return v
}

func (e Entry) Uint32() uint32 {
	v, ok := e.Value.(uint32)
	if !ok {
		panic(fmt.Errorf("interface couldnt be casted to uint32"))
	}

	return v
}

func (e Entry) Uint64() uint64 {
	v, ok := e.Value.(uint64)
	if !ok {
		panic(fmt.Errorf("interface couldnt be casted to uint64"))
	}

	return v
}

func (e Entry) Entries() Entries {
	v, ok := e.Value.(Entries)
	if !ok {
		panic(fmt.Errorf("interface couldnt be casted to levin.Entries"))
	}

	return v
}

func (e Entry) Bytes() []byte {
	return nil
}

type Entries []Entry

func (e Entries) Bytes() []byte {
	return nil
}

type PortableStorage struct {
	Entries Entries
}

func NewPortableStorageFromBytes(bytes []byte) (ps *PortableStorage, err error) {
	var (
		size = 0
		idx  = 0
	)

	{ // sig-a
		size = 4

		if len(bytes[idx:]) < size {
			return nil, fmt.Errorf("sig-a out of bounds")
		}

		sig := binary.LittleEndian.Uint32(bytes[idx : idx+size])
		idx += size

		if sig != uint32(PortableStorageSignatureA) {
			return nil, fmt.Errorf("sig-a doesn't match")
		}
	}

	{ // sig-b
		size = 4
		if len(bytes[idx:]) < size {
			return nil, fmt.Errorf("sig-b out of bounds")
		}

		sig := binary.LittleEndian.Uint32(bytes[idx : idx+size])
		idx += size

		if sig != uint32(PortableStorageSignatureB) {
			return nil, fmt.Errorf("sig-b doesn't match")
		}
	}

	{ // format ver
		size = 1

		if len(bytes[idx:]) < size {
			return nil, fmt.Errorf("version out of bounds")
		}

		version := bytes[idx]
		idx += size

		if version != PortableStorageFormatVersion {
			return nil, fmt.Errorf("version doesn't match")
		}
	}

	ps = &PortableStorage{}

	var n int
	n, ps.Entries, err = ReadObject(bytes[idx:])

	if err != nil {
		return nil, err
	}

	idx += n

	if len(bytes[idx:]) > 0 {
		return nil, fmt.Errorf("leftover bytes")
	}

	return ps, nil
}

func ReadString(bytes []byte) (int, string, error) {
	idx := 0

	n, strLen, err := ReadVarInt(bytes)
	if err != nil {
		return -1, "", err
	}
	idx += n

	if n < 0 {
		return -1, "", fmt.Errorf("length out of bounds")
	}

	if !CanonicalVarIntSize(n, strLen) {
		return -1, "", fmt.Errorf("non-canonical length encoding")
	}

	if len(bytes[idx:]) < strLen {
		return -1, "", io.ErrUnexpectedEOF
	}

	return idx + strLen, string(bytes[idx : idx+strLen]), nil
}

func ReadObject(bytes []byte) (int, Entries, error) {
	idx := 0

	n, i, err := ReadVarInt(bytes[idx:])
	if err != nil {
		return 0, nil, err
	}
	idx += n

	if i <= 0 {
		return 0, nil, fmt.Errorf("invalid length")
	}

	if !CanonicalVarIntSize(n, i) {
		return 0, nil, fmt.Errorf("non-canonical length encoding")
	}

	entries := make(Entries, 0, min(math.MaxUint16, i))

	for iter := 0; iter < i; iter++ {
		var entry Entry

		if len(bytes[idx:]) < 1 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		lenName := int(bytes[idx])
		idx += 1

		if len(bytes[idx:]) < lenName {
			return 0, nil, io.ErrUnexpectedEOF
		}
		entry.Name = string(bytes[idx : idx+lenName])
		idx += lenName

		if len(bytes[idx:]) < 1 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		ttype := bytes[idx]
		idx += 1

		n, obj, serializable, err := ReadAny(bytes[idx:], ttype)
		if err != nil {
			return 0, nil, err
		}
		idx += n

		entry.Value = obj
		entry.Serializable = serializable

		entries = append(entries, entry)
	}

	return idx, entries, nil
}

func ReadArray(ttype byte, bytes []byte) (int, Entries, error) {
	var (
		idx = 0
		n   = 0
	)

	n, i, err := ReadVarInt(bytes[idx:])
	if err != nil {
		return 0, nil, err
	}
	idx += n

	if i < 0 {
		return 0, nil, fmt.Errorf("invalid length")
	}

	if !CanonicalVarIntSize(n, i) {
		return 0, nil, fmt.Errorf("non-canonical length encoding")
	}

	entries := make(Entries, 0, min(math.MaxUint16, i))

	for iter := 0; iter < i; iter++ {
		n, obj, serializable, err := ReadAny(bytes[idx:], ttype)
		if err != nil {
			return 0, nil, err
		}
		idx += n

		entries = append(entries, Entry{
			Value:        obj,
			Serializable: serializable,
		})
	}

	return idx, entries, nil
}

func ReadAny(bytes []byte, ttype byte) (idx int, value interface{}, serializable Serializable, err error) {
	var (
		n = 0
	)

	if ttype&BoostSerializeFlagArray != 0 {
		internalType := ttype &^ BoostSerializeFlagArray
		n, obj, err := ReadArray(internalType, bytes[idx:])
		if err != nil {
			return 0, nil, nil, err
		}
		idx += n

		return idx, obj, nil, nil
	}

	if ttype == BoostSerializeTypeObject {
		n, obj, err := ReadObject(bytes[idx:])
		if err != nil {
			return 0, nil, nil, err
		}
		idx += n

		return idx, obj, nil, nil
	}

	if ttype == BoostSerializeTypeUint8 {
		if len(bytes[idx:]) < 1 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		obj := uint8(bytes[idx])
		n += 1
		idx += n

		return idx, obj, BoostByte(obj), nil
	}

	if ttype == BoostSerializeTypeUint16 {
		if len(bytes[idx:]) < 2 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		obj := binary.LittleEndian.Uint16(bytes[idx:])
		n += 2
		idx += n

		return idx, obj, BoostUint16(obj), nil
	}

	if ttype == BoostSerializeTypeUint32 {
		if len(bytes[idx:]) < 4 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		obj := binary.LittleEndian.Uint32(bytes[idx:])
		n += 4
		idx += n

		return idx, obj, BoostUint32(obj), nil
	}

	if ttype == BoostSerializeTypeUint64 {
		if len(bytes[idx:]) < 8 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		obj := binary.LittleEndian.Uint64(bytes[idx:])
		n += 8
		idx += n

		return idx, obj, BoostUint64(obj), nil
	}

	if ttype == BoostSerializeTypeInt64 {
		if len(bytes[idx:]) < 8 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		obj := binary.LittleEndian.Uint64(bytes[idx:])
		n += 8
		idx += n

		return idx, int64(obj), BoostInt64(obj), nil
	}

	if ttype == BoostSerializeTypeString {
		n, obj, err := ReadString(bytes[idx:])
		if err != nil {
			return 0, nil, nil, err
		}
		idx += n

		return idx, obj, BoostString(obj), nil
	}

	if ttype == BoostSerializeTypeBool {
		if len(bytes[idx:]) < 1 {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}

		if bytes[idx] > 1 {
			return 0, nil, nil, errors.New("invalid non-canonical bool encoding")
		}
		obj := bytes[idx] > 0
		n += 1
		idx += n

		return idx, obj, BoostBool(obj), nil
	}

	return -1, nil, nil, fmt.Errorf("unknown ttype %x", ttype)
}

func CanonicalVarIntSize(n, i int) bool {
	return n <= 1 || i > (1<<((n-1)*8-2))-1
}

// ReadVarInt reads var int, returning number of bytes read and the integer in that byte
// sequence.
func ReadVarInt(b []byte) (int, int, error) {
	if len(b) < 1 {
		return -1, -1, io.ErrUnexpectedEOF
	}

	sizeMask := b[0] & PortableRawSizeMarkMask

	switch uint32(sizeMask) {
	case uint32(PortableRawSizeMarkByte):
		return 1, int(b[0] >> 2), nil
	case uint32(PortableRawSizeMarkWord):
		if len(b) < 2 {
			return -1, -1, io.ErrUnexpectedEOF
		}
		return 2, int((binary.LittleEndian.Uint16(b[0:2])) >> 2), nil
	case PortableRawSizeMarkDword:
		if len(b) < 4 {
			return -1, -1, io.ErrUnexpectedEOF
		}
		return 4, int((binary.LittleEndian.Uint32(b[0:4])) >> 2), nil
	case uint32(PortableRawSizeMarkInt64):
		// TODO
		return -1, -1, errors.New("int64 not supported")
		// return int((binary.LittleEndian.Uint64(b[0:8])) >> 2)
		//         '-> bad
	default:
		return -1, -1, fmt.Errorf("malformed sizemask: %+v", sizeMask)
	}
}

func (s *PortableStorage) Bytes() ([]byte, error) {
	var (
		body = make([]byte, 9) // fit _at least_ signatures + format ver
		b    = make([]byte, 8) // biggest type

		idx  = 0
		size = 0
	)

	{ // signature a
		size = 4

		binary.LittleEndian.PutUint32(b, PortableStorageSignatureA)
		copy(body[idx:], b[:size])
		idx += size
	}

	{ // signature b
		size = 4

		binary.LittleEndian.PutUint32(b, PortableStorageSignatureB)
		copy(body[idx:], b[:size])
		idx += size
	}

	{ // format ver
		size = 1

		b[0] = PortableStorageFormatVersion
		copy(body[idx:], b[:size])
		idx += size
	}

	// // write_var_in
	varInB, err := VarIn(len(s.Entries))
	if err != nil {
		return nil, fmt.Errorf("varin '%d': %w", len(s.Entries), err)
	}

	body = append(body, varInB...)
	for _, entry := range s.Entries {
		body = append(body, byte(len(entry.Name))) // section name length
		body = append(body, []byte(entry.Name)...) // section name
		if entry.Serializable == nil {
			return nil, ErrNotSerializable
		}
		data, err := entry.Serializable.Bytes()
		if err != nil {
			return nil, err
		}
		body = append(body, data...)
	}

	return body, nil
}

var ErrNotSerializable = errors.New("not serializable")

type Serializable interface {
	Bytes() ([]byte, error)
}

type Section struct {
	Entries []Entry
}

func (s Section) Bytes() ([]byte, error) {
	body := []byte{
		BoostSerializeTypeObject,
	}

	varInB, err := VarIn(len(s.Entries))
	if err != nil {
		panic(fmt.Errorf("varin '%d': %w", len(s.Entries), err))
	}

	body = append(body, varInB...)
	for _, entry := range s.Entries {
		body = append(body, byte(len(entry.Name))) // section name length
		body = append(body, []byte(entry.Name)...) // section name
		if entry.Serializable == nil {
			return nil, ErrNotSerializable
		}
		data, err := entry.Serializable.Bytes()
		if err != nil {
			return nil, err
		}
		body = append(body, data...)
	}

	return body, nil
}

func VarIn(i int) ([]byte, error) {
	if i <= 63 {
		return []byte{
			(byte(i) << 2) | PortableRawSizeMarkByte,
		}, nil
	}

	if i <= 16383 {
		b := []byte{0x00, 0x00}
		binary.LittleEndian.PutUint16(b,
			(uint16(i)<<2)|PortableRawSizeMarkWord,
		)

		return b, nil
	}

	if i <= 1073741823 {
		b := []byte{0x00, 0x00, 0x00, 0x00}
		binary.LittleEndian.PutUint32(b,
			(uint32(i)<<2)|PortableRawSizeMarkDword,
		)

		return b, nil
	}

	return nil, fmt.Errorf("int %d too big", i)
}
