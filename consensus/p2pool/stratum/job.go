package stratum

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"git.gammaspectra.live/P2Pool/consensus/v4/merge_mining"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	types2 "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

type Job struct {
	TemplateCounter uint64

	ExtraNonce       uint32
	SideRandomNumber uint32
	SideExtraNonce   uint32

	MerkleRoot       types.Hash
	MerkleProof      crypto.MerkleProof
	MergeMiningExtra sidechain.MergeMiningExtra
}

func (j Job) Id() string {
	buf := make([]byte, 0, utils.UVarInt64Size(j.TemplateCounter)+4*3+types.HashSize+1+len(j.MerkleProof)*types.HashSize+j.MergeMiningExtra.BufferLength())
	buf = binary.AppendUvarint(buf, j.TemplateCounter)
	buf = binary.LittleEndian.AppendUint32(buf, j.ExtraNonce)
	buf = binary.LittleEndian.AppendUint32(buf, j.SideRandomNumber)
	buf = binary.LittleEndian.AppendUint32(buf, j.SideExtraNonce)
	buf = append(buf, j.MerkleRoot[:]...)

	buf = append(buf, uint8(len(j.MerkleProof)))

	for _, e := range j.MerkleProof {
		buf = append(buf, e[:]...)
	}

	buf = binary.AppendUvarint(buf, uint64(len(j.MergeMiningExtra)))
	for _, extra := range j.MergeMiningExtra {
		buf = append(buf, extra.ChainId[:]...)

		buf = binary.AppendUvarint(buf, uint64(len(extra.Data)))
		buf = append(buf, extra.Data[:]...)
	}

	return base64.RawURLEncoding.EncodeToString(buf)
}

func JobFromString(s string) (j Job, err error) {
	buf, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return j, err
	}
	reader := bytes.NewReader(buf)
	if j.TemplateCounter, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return j, err
	}
	if err = binary.Read(reader, binary.LittleEndian, &j.ExtraNonce); err != nil {
		return j, err
	}
	if err = binary.Read(reader, binary.LittleEndian, &j.SideRandomNumber); err != nil {
		return j, err
	}
	if err = binary.Read(reader, binary.LittleEndian, &j.SideExtraNonce); err != nil {
		return j, err
	}
	if _, err = io.ReadFull(reader, j.MerkleRoot[:]); err != nil {
		return j, err
	}
	merkleProofSize, err := reader.ReadByte()
	if err != nil {
		return j, err
	}
	if merkleProofSize > merge_mining.MaxChainsLog2 {
		return j, fmt.Errorf("merkle proof too large: %d > %d", merkleProofSize, merge_mining.MaxChainsLog2)
	} else if merkleProofSize > 0 {
		// preallocate
		j.MerkleProof = make(crypto.MerkleProof, merkleProofSize)

		for i := 0; i < int(merkleProofSize); i++ {
			if _, err = io.ReadFull(reader, j.MerkleProof[i][:]); err != nil {
				return j, err
			}
		}
	}
	mergeMiningExtraSize, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return j, err
	} else if mergeMiningExtraSize > merge_mining.MaxChains {
		return j, fmt.Errorf("merge mining data too big: %d > %d", mergeMiningExtraSize, merge_mining.MaxChains)
	} else if mergeMiningExtraSize > 0 {
		// preallocate
		j.MergeMiningExtra = make(sidechain.MergeMiningExtra, mergeMiningExtraSize)

		var mergeMiningExtraDataSize uint64
		for i := 0; i < int(mergeMiningExtraSize); i++ {
			if _, err = io.ReadFull(reader, j.MergeMiningExtra[i].ChainId[:]); err != nil {
				return j, err
			} else if i > 0 && j.MergeMiningExtra[i-1].ChainId.Compare(j.MergeMiningExtra[i].ChainId) >= 0 {
				// IDs must be ordered to avoid duplicates
				return j, fmt.Errorf("duplicate or not ordered merge mining data chain id: %s > %s", j.MergeMiningExtra[i-1].ChainId, j.MergeMiningExtra[i].ChainId)
			} else if mergeMiningExtraDataSize, err = utils.ReadCanonicalUvarint(reader); err != nil {
				return j, err
			} else if mergeMiningExtraDataSize > sidechain.PoolBlockMaxTemplateSize {
				return j, fmt.Errorf("merge mining data size too big: %d > %d", mergeMiningExtraDataSize, sidechain.PoolBlockMaxTemplateSize)
			} else if mergeMiningExtraDataSize > 0 {
				j.MergeMiningExtra[i].Data = make(types.Bytes, mergeMiningExtraDataSize)
				if _, err = io.ReadFull(reader, j.MergeMiningExtra[i].Data); err != nil {
					return j, err
				}
			}
		}
	}

	if reader.Len() > 0 {
		return j, errors.New("leftover bytes")
	}

	return j, nil
}

// GetJobBlob Gets old job data based on returned id
func (e *MinerTrackingEntry) GetJobBlob(c *Client, consensus *sidechain.Consensus, job Job, nonce uint32) []byte {
	e.Lock.RLock()
	defer e.Lock.RUnlock()

	if t, ok := e.Templates[job.TemplateCounter]; ok {

		mmExtra := job.MergeMiningExtra
		if c.SubaddressViewPub != [33]byte{} {
			mmExtra = mmExtra.Set(sidechain.ExtraChainKeySubaddressViewPub, c.SubaddressViewPub[:])
		}

		buffer := bytes.NewBuffer(make([]byte, 0, t.BufferLength(consensus, job.MerkleProof, mmExtra)))
		if err := t.Write(buffer, consensus, nonce, job.ExtraNonce, job.SideRandomNumber, job.SideExtraNonce, job.MerkleRoot, job.MerkleProof, mmExtra, types2.CurrentSoftwareId, types2.CurrentSoftwareVersion); err != nil {
			return nil
		}
		return buffer.Bytes()
	} else {
		return nil
	}
}
