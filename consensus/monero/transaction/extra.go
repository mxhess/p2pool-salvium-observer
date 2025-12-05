package transaction

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

const TxExtraTagPadding = 0x00
const TxExtraTagPubKey = 0x01
const TxExtraTagNonce = 0x02
const TxExtraTagMergeMining = 0x03
const TxExtraTagAdditionalPubKeys = 0x04
const TxExtraTagMysteriousMinergate = 0xde

const TxExtraPaddingMaxCount = 255
const TxExtraNonceMaxCount = 255

const TxExtraTagMergeMiningMaxCount = types.HashSize + 9

const TxExtraTemplateNonceSize = 4

type ExtraTags []ExtraTag

type ExtraTag struct {
	// VarInt has different meanings. In TxExtraTagMergeMining it is depth, while in others it is length
	VarInt    uint64      `json:"var_int"`
	Tag       uint8       `json:"tag"`
	HasVarInt bool        `json:"has_var_int"`
	Data      types.Bytes `json:"data"`
}

func (t *ExtraTags) UnmarshalBinary(data []byte) (err error) {
	reader := bytes.NewReader(data)
	err = t.FromReader(reader)
	if err != nil {
		return err
	}
	if reader.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}
	return nil
}

func (t *ExtraTags) BufferLength() (length int) {
	for _, tag := range *t {
		length += tag.BufferLength()
	}
	return length
}

func (t *ExtraTags) MarshalBinary() ([]byte, error) {

	return t.AppendBinary(make([]byte, 0, t.BufferLength()))
}

func (t *ExtraTags) AppendBinary(preAllocatedBuf []byte) (buf []byte, err error) {
	if t == nil {
		return nil, nil
	}
	buf = preAllocatedBuf
	for _, tag := range *t {
		if buf, err = tag.AppendBinary(buf); err != nil {
			return nil, err
		}
	}

	return buf, nil
}

func (t *ExtraTags) SideChainHashingBlob(preAllocatedBuf []byte, zeroTemplateId bool) (buf []byte, err error) {
	if t == nil {
		return nil, nil
	}
	buf = preAllocatedBuf
	for _, tag := range *t {
		if buf, err = tag.SideChainHashingBlob(buf, zeroTemplateId); err != nil {
			return nil, err
		}
	}

	return buf, nil
}

func (t *ExtraTags) FromReader(reader utils.ReaderAndByteReader) (err error) {
	for {
		var tag ExtraTag
		if err = tag.FromReader(reader); err != nil {
			if errors.Is(err, ErrExtraTagNoMoreTags) {
				return nil
			}
			return err
		}
		if t.GetTag(tag.Tag) != nil {
			return errors.New("tag already exists")
		}
		*t = append(*t, tag)
	}
}

func (t *ExtraTags) GetTag(tag uint8) *ExtraTag {
	for i := range *t {
		if (*t)[i].Tag == tag {
			return &(*t)[i]
		}
	}

	return nil
}

func (t *ExtraTag) UnmarshalBinary(data []byte) error {
	reader := bytes.NewReader(data)
	err := t.FromReader(reader)
	if err != nil {
		return err
	}
	if reader.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}
	return nil
}

func (t *ExtraTag) BufferLength() int {
	if t.HasVarInt {
		return 1 + utils.UVarInt64Size(t.VarInt) + len(t.Data)
	}
	return 1 + len(t.Data)
}

func (t *ExtraTag) MarshalBinary() ([]byte, error) {
	return t.AppendBinary(make([]byte, 0, t.BufferLength()))
}

func (t *ExtraTag) AppendBinary(preAllocatedBuf []byte) ([]byte, error) {
	buf := preAllocatedBuf
	buf = append(buf, t.Tag)
	if t.HasVarInt {
		buf = binary.AppendUvarint(buf, t.VarInt)
	}
	buf = append(buf, t.Data...)
	return buf, nil
}

func (t *ExtraTag) SideChainHashingBlob(preAllocatedBuf []byte, zeroTemplateId bool) ([]byte, error) {
	buf := preAllocatedBuf
	buf = append(buf, t.Tag)
	if t.HasVarInt {
		buf = binary.AppendUvarint(buf, t.VarInt)
	}
	if zeroTemplateId && t.Tag == TxExtraTagMergeMining {
		// TODO: this is to comply with non-standard p2pool serialization, see https://github.com/SChernykh/p2pool/issues/249
		// v3 has some extra data included before hash
		// serialize everything but the last hash size bytes
		dataLen := max(0, len(t.Data)-types.HashSize)
		buf = append(buf, t.Data[:dataLen]...)

		// serialize zero hash or remaining data only
		buf = append(buf, make([]byte, len(t.Data)-dataLen)...)
	} else if t.Tag == TxExtraTagNonce {
		//Replace only the first four bytes
		buf = append(buf,
			[]byte{0, 0, 0, 0}[:min(TxExtraTemplateNonceSize, len(t.Data))]...,
		)
		if len(t.Data) > TxExtraTemplateNonceSize {
			buf = append(buf, t.Data[TxExtraTemplateNonceSize:]...)
		}
	} else {
		buf = append(buf, t.Data...)
	}
	return buf, nil
}

var ErrExtraTagNoMoreTags = errors.New("no more tags")

func (t *ExtraTag) FromReader(reader utils.ReaderAndByteReader) (err error) {

	if err = binary.Read(reader, binary.LittleEndian, &t.Tag); err != nil {
		if err == io.EOF {
			return ErrExtraTagNoMoreTags
		}
		return err
	}

	switch t.Tag {
	default:
		return fmt.Errorf("unknown extra tag %d", t.Tag)
	case TxExtraTagPadding:
		var size uint64
		var zero byte
		for size = 1; size <= TxExtraPaddingMaxCount; size++ {
			if zero, err = reader.ReadByte(); err != nil {
				if err == io.EOF {
					break
				} else {
					return err
				}
			}

			if zero != 0 {
				return errors.New("padding is not zero")
			}
		}

		if size > TxExtraPaddingMaxCount {
			return errors.New("padding is too big")
		}

		t.Data = make([]byte, size-1)
	case TxExtraTagPubKey:
		t.Data = make([]byte, crypto.PublicKeySize)
		if _, err = io.ReadFull(reader, t.Data); err != nil {
			return err
		}
	case TxExtraTagNonce:
		t.HasVarInt = true
		if t.VarInt, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		} else {
			if t.VarInt > TxExtraNonceMaxCount {
				return errors.New("nonce is too big")
			}

			t.Data = make([]byte, t.VarInt)
			if _, err = io.ReadFull(reader, t.Data); err != nil {
				return err
			}
		}
	case TxExtraTagAdditionalPubKeys:
		t.HasVarInt = true
		if t.VarInt, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		} else {
			_, err = utils.ReadFullProgressive(io.LimitReader(reader, int64(types.HashSize*t.VarInt)), &t.Data, int(types.HashSize*t.VarInt))
			if err != nil {
				return err
			}
		}
	case TxExtraTagMergeMining, TxExtraTagMysteriousMinergate:
		t.HasVarInt = true
		if t.VarInt, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		} else {
			_, err = utils.ReadFullProgressive(io.LimitReader(reader, int64(t.VarInt)), &t.Data, int(t.VarInt))
			if err != nil {
				return err
			}
		}
	}

	return nil
}
