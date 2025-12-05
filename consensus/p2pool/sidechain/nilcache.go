package sidechain

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/sha3"
)

type NilDerivationCache struct {
}

func (d *NilDerivationCache) Clear() {

}

func (d *NilDerivationCache) GetEphemeralPublicKey(a *address.PackedAddress, _ crypto.PrivateKeySlice, txKeyScalar *crypto.PrivateKeyScalar, outputIndex uint64, hasher *sha3.HasherState) (crypto.PublicKeyBytes, uint8) {
	ephemeralPubKey, viewTag := address.GetEphemeralPublicKeyAndViewTagNoAllocate(a.SpendPublicKey().AsPoint().Point(), address.GetDerivationNoAllocate(a.ViewPublicKey().AsPoint().Point(), txKeyScalar.Scalar()), outputIndex, hasher)

	return ephemeralPubKey.AsBytes(), viewTag
}

func (d *NilDerivationCache) GetDeterministicTransactionKey(seed types.Hash, prevId types.Hash) *crypto.KeyPair {
	return crypto.NewKeyPairFromPrivate(address.GetDeterministicTransactionPrivateKey(seed, prevId))
}
