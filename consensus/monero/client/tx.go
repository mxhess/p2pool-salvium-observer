package client

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/rpc/daemon"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
)

func isRctBulletproof(t int) bool {
	switch t {
	case 3, 4, 5: // RCTTypeBulletproof, RCTTypeBulletproof2, RCTTypeCLSAG:
		return true
	default:
		return false
	}
}

func isRctBulletproofPlus(t int) bool {
	switch t {
	case 6: // RCTTypeBulletproofPlus:
		return true
	default:
		return false
	}
}

// NewEntryFromRPCData TODO
func NewEntryFromRPCData(id types.Hash, buf []byte, json *daemon.TransactionJSON) *mempool.Entry {
	isBulletproof := isRctBulletproof(json.RctSignatures.Type)
	isBulletproofPlus := isRctBulletproofPlus(json.RctSignatures.Type)

	var weight, paddedOutputs, bpBase, bpSize, bpClawback uint64
	if !isBulletproof && !isBulletproofPlus {
		weight = uint64(len(buf))
	} else if isBulletproofPlus {
		for _, proof := range json.RctsigPrunable.Bpp {
			LSize := len(proof.L) / 2
			n2 := uint64(1 << (LSize - 6))
			if n2 == 0 {
				paddedOutputs = 0
				break
			}
			paddedOutputs += n2
		}
		{

			bpBase = uint64(32*6+7*2) / 2

			//get_transaction_weight_clawback
			if len(json.RctSignatures.Outpk) <= 2 {
				bpClawback = 0
			} else {
				nlr := 0
				for (1 << nlr) < paddedOutputs {
					nlr++
				}
				nlr += 6

				bpSize = uint64(32*6 + 2*nlr)

				bpClawback = (bpBase*paddedOutputs - bpSize) * 4 / 5
			}
		}

		weight = uint64(len(buf)) + bpClawback
	} else {
		for _, proof := range json.RctsigPrunable.Bp {
			LSize := len(proof.L) / 2
			n2 := uint64(1 << (LSize - 6))
			if n2 == 0 {
				paddedOutputs = 0
				break
			}
			paddedOutputs += n2
		}
		{

			bpBase = uint64(32*9+7*2) / 2

			//get_transaction_weight_clawback
			if len(json.RctSignatures.Outpk) <= 2 {
				bpClawback = 0
			} else {
				nlr := 0
				for (1 << nlr) < paddedOutputs {
					nlr++
				}
				nlr += 6

				bpSize = uint64(32*9 + 2*nlr)

				bpClawback = (bpBase*paddedOutputs - bpSize) * 4 / 5
			}
		}

		weight = uint64(len(buf)) + bpClawback
	}

	return &mempool.Entry{
		Id:       id,
		BlobSize: uint64(len(buf)),
		Weight:   weight,
		Fee:      json.RctSignatures.Txnfee,
	}
}
