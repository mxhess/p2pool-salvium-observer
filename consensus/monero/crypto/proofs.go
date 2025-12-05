package crypto

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
)

/*
OutProof = txPub, viewPub, nil, sharedSecret
InProof = viewPub, txPub, spendPub, sharedSecret
*/

var TxProofV2DomainSeparatorHash = Keccak256([]byte("TXPROOF_V2")) // HASH_KEY_TXPROOF_V2
func GenerateTxProofV2(prefixHash types.Hash, R, A, B, D PublicKey, r PrivateKey) (signature *Signature) {
	comm := &SignatureComm_2{}
	comm.Message = prefixHash

	//shared secret
	comm.D = D

	comm.Separator = TxProofV2DomainSeparatorHash
	comm.R = R
	comm.A = A

	signature = CreateSignature(func(k PrivateKey) []byte {
		if B == nil {
			// compute X = k*G
			comm.X = k.PublicKey()
			comm.B = nil
		} else {
			// compute X = k*B
			comm.X = k.GetDerivation(B)
			comm.B = B
		}

		comm.Y = k.GetDerivation(A)

		return comm.Bytes()
	}, r)

	return signature
}

func GenerateTxProofV1(prefixHash types.Hash, A, B, D PublicKey, r PrivateKey) (signature *Signature) {
	comm := &SignatureComm_2_V1{}
	comm.Message = prefixHash

	//shared secret
	comm.D = D

	signature = CreateSignature(func(k PrivateKey) []byte {
		if B == nil {
			// compute X = k*G
			comm.X = k.PublicKey()
		} else {
			// compute X = k*B
			comm.X = k.GetDerivation(B)
		}

		comm.Y = k.GetDerivation(A)

		return comm.Bytes()
	}, r)

	return signature
}
