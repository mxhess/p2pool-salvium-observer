package main

import (
	"strings"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	base58 "git.gammaspectra.live/P2Pool/monero-base58"
)

func GetOutProofV2_SpecialPayout(a address.Interface, txId types.Hash, txKey crypto.PrivateKey, message string) string {
	prefixHash := crypto.Keccak256(txId[:], []byte(message))

	var signature *crypto.Signature

	sharedSecret := txKey.GetDerivation(a.ViewPublicKey())
	if sa, ok := a.(address.InterfaceSubaddress); ok && sa.IsSubaddress() {
		signature = crypto.GenerateTxProofV2(prefixHash, txKey.PublicKey(), sa.ViewPublicKey(), sa.SpendPublicKey(), sharedSecret, txKey)
	} else {
		signature = crypto.GenerateTxProofV2(prefixHash, txKey.PublicKey(), a.ViewPublicKey(), nil, sharedSecret, txKey)
	}

	output := make([]string, 3)
	output[0] = "OutProofV2"
	output[1] = string(base58.EncodeMoneroBase58(sharedSecret.AsSlice()))
	output[2] = string(base58.EncodeMoneroBase58(signature.Bytes()))
	return strings.Join(output, "")
}
