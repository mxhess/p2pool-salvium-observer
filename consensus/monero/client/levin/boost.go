package levin

import (
	"encoding/binary"
	"fmt"
)

const (
	BoostSerializeTypeInt64 byte = 0x1
	BoostSerializeTypeInt32 byte = 0x2
	BoostSerializeTypeInt16 byte = 0x3
	BoostSerializeTypeInt8  byte = 0x4

	BoostSerializeTypeUint64 byte = 0x5
	BoostSerializeTypeUint32 byte = 0x6
	BoostSerializeTypeUint16 byte = 0x7
	BoostSerializeTypeUint8  byte = 0x8

	BoostSerializeTypeDouble byte = 0x9

	BoostSerializeTypeString byte = 0x0a
	BoostSerializeTypeBool   byte = 0x0b
	BoostSerializeTypeObject byte = 0x0c
	BoostSerializeTypeArray  byte = 0xd

	BoostSerializeFlagArray byte = 0x80
)

type BoostBool bool

func (v BoostBool) Bytes() ([]byte, error) {
	return []byte{
		BoostSerializeTypeBool,
		func() byte {
			if v {
				return 1
			}
			return 0
		}(),
	}, nil
}

type BoostByte byte

func (v BoostByte) Bytes() ([]byte, error) {
	return []byte{
		BoostSerializeTypeUint8,
		byte(v),
	}, nil
}

type BoostUint16 uint16

func (v BoostUint16) Bytes() ([]byte, error) {
	b := []byte{
		BoostSerializeTypeUint16,
		0x00, 0x00,
	}
	binary.LittleEndian.PutUint16(b[1:], uint16(v))
	return b, nil
}

type BoostUint32 uint32

func (v BoostUint32) Bytes() ([]byte, error) {
	b := []byte{
		BoostSerializeTypeUint32,
		0x00, 0x00, 0x00, 0x00,
	}
	binary.LittleEndian.PutUint32(b[1:], uint32(v))
	return b, nil
}

type BoostInt64 int64

func (v BoostInt64) Bytes() ([]byte, error) {
	b := []byte{
		BoostSerializeTypeInt64,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}

	binary.LittleEndian.PutUint64(b[1:], uint64(v))

	return b, nil
}

type BoostUint64 uint64

func (v BoostUint64) Bytes() ([]byte, error) {
	b := []byte{
		BoostSerializeTypeUint64,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}

	binary.LittleEndian.PutUint64(b[1:], uint64(v))

	return b, nil
}

type BoostString string

func (v BoostString) Bytes() ([]byte, error) {
	b := []byte{BoostSerializeTypeString}

	varInB, err := VarIn(len(v))
	if err != nil {
		return nil, fmt.Errorf("varin '%d': %w", len(v), err)
	}

	return append(b, append(varInB, []byte(v)...)...), nil
}
