package merge_mining

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"io"
	"time"
)

type AuxiliaryJob struct {
	Hash       types.Hash       `json:"aux_hash"`
	Blob       types.Bytes      `json:"aux_blob"`
	Difficulty types.Difficulty `json:"aux_diff"`
}

// AuxiliaryJobDonationMasterPublicKey Master key controlled by p2pool admins
// See https://github.com/SChernykh/p2pool/blob/5aea5768a7f328dbe5ba684cecab79d12fdc91cd/src/util.cpp#L63
var AuxiliaryJobDonationMasterPublicKey = ed25519.PublicKey{
	51, 175, 37, 73, 203, 241, 188, 115,
	195, 255, 123, 53, 218, 120, 90, 74,
	186, 240, 82, 178, 67, 139, 124, 91,
	180, 106, 188, 181, 187, 51, 236, 10,
}

type AuxiliaryJobDonation struct {
	SecondaryPublicKey           [ed25519.PublicKeySize]byte
	SecondaryPublicKeyExpiration int64
	SecondarySignature           [ed25519.SignatureSize]byte

	Timestamp     int64
	Entries       []AuxiliaryJobDonationDataEntry
	DataSignature [ed25519.SignatureSize]byte
}

func (j *AuxiliaryJobDonation) Verify(now time.Time) (ok bool, err error) {
	keyExpiration := time.Unix(j.SecondaryPublicKeyExpiration, 0)

	if keyExpiration.Compare(now) <= 0 {
		return false, errors.New("expired secondary key")
	}

	message := make([]byte, 0, ed25519.PublicKeySize+8)
	message = append(message, j.SecondaryPublicKey[:]...)
	message = binary.LittleEndian.AppendUint64(message, uint64(j.SecondaryPublicKeyExpiration))
	if !ed25519.Verify(AuxiliaryJobDonationMasterPublicKey, message, j.SecondarySignature[:]) {
		return false, errors.New("invalid master signature")
	}

	size := 8
	for _, e := range j.Entries {
		size += e.BufferLength()
	}

	message = make([]byte, 0, size)
	message = binary.LittleEndian.AppendUint64(message, uint64(j.Timestamp))
	for _, e := range j.Entries {
		message, err = e.AppendBinary(message)
		if err != nil {
			return false, err
		}
	}
	if !ed25519.Verify(j.SecondaryPublicKey[:], message, j.DataSignature[:]) {
		return false, errors.New("invalid data signature")
	}
	return true, nil
}

func (j *AuxiliaryJobDonation) BufferLength() int {
	size := ed25519.PublicKeySize + 8 + ed25519.SignatureSize + 8 + ed25519.SignatureSize
	for _, e := range j.Entries {
		size += e.BufferLength()
	}
	return size
}

func (j *AuxiliaryJobDonation) MarshalBinary() (data []byte, err error) {
	return j.AppendBinary(make([]byte, 0, j.BufferLength()))
}

func (j *AuxiliaryJobDonation) AppendBinary(preAllocatedBuf []byte) (data []byte, err error) {
	data = preAllocatedBuf
	data = append(data, j.SecondaryPublicKey[:]...)
	data = binary.LittleEndian.AppendUint64(data, uint64(j.SecondaryPublicKeyExpiration))
	data = append(data, j.SecondarySignature[:]...)

	data = binary.LittleEndian.AppendUint64(data, uint64(j.Timestamp))
	for _, e := range j.Entries {
		data, err = e.AppendBinary(data)
		if err != nil {
			return nil, err
		}
	}
	data = append(data, j.DataSignature[:]...)
	return data, nil
}

func (j *AuxiliaryJobDonation) FromReader(reader utils.ReaderAndByteReader) (err error) {

	if _, err := reader.Read(j.SecondaryPublicKey[:]); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.LittleEndian, &j.SecondaryPublicKeyExpiration); err != nil {
		return err
	}

	if _, err := reader.Read(j.SecondarySignature[:]); err != nil {
		return err
	}
	if err := binary.Read(reader, binary.LittleEndian, &j.Timestamp); err != nil {
		return err
	}

	buf := make([]byte, (&AuxiliaryJobDonationDataEntry{}).BufferLength())

	for {
		var dataEntry AuxiliaryJobDonationDataEntry
		_, err = io.ReadFull(reader, buf)
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		copy(dataEntry.AuxId[:], buf)
		copy(dataEntry.AuxHash[:], buf[types.HashSize:])
		dataEntry.AuxDifficulty.Lo = binary.LittleEndian.Uint64(buf[types.HashSize*2:])
		dataEntry.AuxDifficulty.Hi = binary.LittleEndian.Uint64(buf[types.HashSize*2+8:])
		j.Entries = append(j.Entries, dataEntry)
	}

	// read remainder
	copy(j.DataSignature[:], buf)

	return nil
}

type AuxiliaryJobDonationDataEntry struct {
	AuxId         types.Hash
	AuxHash       types.Hash
	AuxDifficulty types.Difficulty
}

func (e *AuxiliaryJobDonationDataEntry) BufferLength() int {
	return types.HashSize*2 + types.DifficultySize
}

func (e *AuxiliaryJobDonationDataEntry) MarshalBinary() (data []byte, err error) {
	return e.AppendBinary(make([]byte, 0, e.BufferLength()))
}

func (e *AuxiliaryJobDonationDataEntry) AppendBinary(preAllocatedBuf []byte) (data []byte, err error) {
	data = preAllocatedBuf
	data = append(data, e.AuxId[:]...)
	data = append(data, e.AuxHash[:]...)
	data = binary.LittleEndian.AppendUint64(data, e.AuxDifficulty.Lo)
	data = binary.LittleEndian.AppendUint64(data, e.AuxDifficulty.Hi)
	return data, nil
}
