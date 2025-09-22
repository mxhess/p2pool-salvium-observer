package main

import (
	"strings"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	base58 "git.gammaspectra.live/P2Pool/monero-base58"
)

func GetPayout(main, sub *address.Address) *address.Address {
	if sub != nil {
		return sub
	}
	return main
}

// GetOutProofV2_SpecialPayout Special derivation for subaddress P2Pool payouts
// txKey pub is pre-derivated, so do not apply the special R = s * Di
// otherwise send it via the special path
func GetOutProofV2_SpecialPayout(a address.Interface, sa address.InterfaceSubaddress, txId types.Hash, txKey crypto.PrivateKey, message string) string {
	prefixHash := crypto.Keccak256(txId[:], []byte(message))

	var signature *crypto.Signature

	sharedSecret := txKey.GetDerivation(a.ViewPublicKey())
	if sa.IsSubaddress() {
		signature = crypto.GenerateTxProofV2(prefixHash, txKey.PublicKey(), a.ViewPublicKey(), sa.SpendPublicKey(), sharedSecret, txKey)
	} else {
		signature = crypto.GenerateTxProofV2(prefixHash, txKey.PublicKey(), a.ViewPublicKey(), nil, sharedSecret, txKey)
	}

	output := make([]string, 3)
	output[0] = "OutProofV2"
	output[1] = string(base58.EncodeMoneroBase58(sharedSecret.AsSlice()))
	output[2] = string(base58.EncodeMoneroBase58(signature.Bytes()))
	return strings.Join(output, "")
}
