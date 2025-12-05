package address

import (
	"encoding/binary"
	"strings"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	p2poolcrypto "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/edwards25519"
	base58 "git.gammaspectra.live/P2Pool/monero-base58"
	"git.gammaspectra.live/P2Pool/sha3"
)

// ZeroPrivateKeyAddress Special address with private keys set to both zero.
// Useful to detect unsupported signatures from hardware wallets on Monero GUI
var ZeroPrivateKeyAddress PackedAddress

func init() {
	ZeroPrivateKeyAddress[PackedAddressSpend] = crypto.ZeroPrivateKeyBytes.PublicKey().AsBytes()
	ZeroPrivateKeyAddress[PackedAddressView] = crypto.ZeroPrivateKeyBytes.PublicKey().AsBytes()
}

func GetDeterministicTransactionPrivateKey(seed types.Hash, prevId types.Hash) crypto.PrivateKey {
	return p2poolcrypto.GetDeterministicTransactionPrivateKey(seed, prevId)
}

func GetPublicKeyForSharedData(a Interface, sharedData crypto.PrivateKey) crypto.PublicKey {
	return sharedData.PublicKey().AsPoint().Add(a.SpendPublicKey().AsPoint())
}

func GetEphemeralPublicKey(a Interface, txKey crypto.PrivateKey, outputIndex uint64) crypto.PublicKey {
	return GetPublicKeyForSharedData(a, crypto.GetDerivationSharedDataForOutputIndex(txKey.GetDerivationCofactor(a.ViewPublicKey()), outputIndex))
}

func GetEphemeralPublicKeyWithViewKey(a Interface, txPubKey crypto.PublicKey, viewKey crypto.PrivateKey, outputIndex uint64) crypto.PublicKey {
	return GetPublicKeyForSharedData(a, crypto.GetDerivationSharedDataForOutputIndex(viewKey.GetDerivationCofactor(txPubKey), outputIndex))
}

func getEphemeralPublicKeyInline(spendPub, viewPub *edwards25519.Point, txKey *edwards25519.Scalar, outputIndex uint64, p *edwards25519.Point) {
	//derivation
	p.UnsafeVarTimeScalarMult(txKey, viewPub).MultByCofactor(p)
	derivationAsBytes := p.Bytes()
	var varIntBuf [binary.MaxVarintLen64]byte
	sharedData := crypto.HashToScalarNoAllocate(derivationAsBytes, varIntBuf[:binary.PutUvarint(varIntBuf[:], outputIndex)])
	//public key + add
	p.UnsafeVarTimeScalarBaseMult(&sharedData).Add(p, spendPub)
}

func GetSubaddressFakeAddress(sa InterfaceSubaddress, viewKey crypto.PrivateKey) Interface {
	if !sa.IsSubaddress() {
		return sa
	}
	// mismatched view key
	if viewKey.GetDerivation(sa.SpendPublicKey()).AsBytes() != sa.ViewPublicKey().AsBytes() {
		return nil
	}
	switch t := sa.(type) {
	case *Address:
		// Check if it's a legacy subaddress
		switch t.TypeNetwork {
		case monero.SalviumMainSubaddress:
			return FromRawAddress(monero.MainNetwork, sa.SpendPublicKey(), viewKey.PublicKey())
		case monero.SalviumTestSubaddress:
			return FromRawAddress(monero.TestNetwork, sa.SpendPublicKey(), viewKey.PublicKey())
		case monero.SalviumStageSubaddress:
			return FromRawAddress(monero.StageNetwork, sa.SpendPublicKey(), viewKey.PublicKey())
		// Carrot subaddresses
		case monero.CarrotMainSubaddress:
			return FromRawAddress(monero.MainNetwork, sa.SpendPublicKey(), viewKey.PublicKey())
		case monero.CarrotTestSubaddress:
			return FromRawAddress(monero.TestNetwork, sa.SpendPublicKey(), viewKey.PublicKey())
		case monero.CarrotStageSubaddress:
			return FromRawAddress(monero.StageNetwork, sa.SpendPublicKey(), viewKey.PublicKey())
		default:
			return nil
		}
	default:
		return &PackedAddress{sa.SpendPublicKey().AsBytes(), viewKey.PublicKey().AsBytes()}
	}
}

func GetEphemeralPublicKeyAndViewTagWithViewKey(a Interface, txPubKey crypto.PublicKey, viewKey crypto.PrivateKey, outputIndex uint64) (crypto.PublicKey, uint8) {
	pK, viewTag := crypto.GetDerivationSharedDataAndViewTagForOutputIndex(viewKey.GetDerivationCofactor(txPubKey), outputIndex)
	return GetPublicKeyForSharedData(a, pK), viewTag
}

func GetEphemeralPublicKeyAndViewTag(a Interface, txKey crypto.PrivateKey, outputIndex uint64) (crypto.PublicKey, uint8) {
	pK, viewTag := crypto.GetDerivationSharedDataAndViewTagForOutputIndex(txKey.GetDerivationCofactor(a.ViewPublicKey()), outputIndex)
	return GetPublicKeyForSharedData(a, pK), viewTag
}

// GetEphemeralPublicKeyAndViewTagNoAllocate Special version of GetEphemeralPublicKeyAndViewTag
func GetEphemeralPublicKeyAndViewTagNoAllocate(spendPublicKeyPoint *edwards25519.Point, derivation crypto.PublicKeyBytes, outputIndex uint64, hasher *sha3.HasherState) (crypto.PublicKeyBytes, uint8) {
	var intermediatePublicKey, ephemeralPublicKey edwards25519.Point
	derivationSharedData, viewTag := crypto.GetDerivationSharedDataAndViewTagForOutputIndexNoAllocate(derivation, outputIndex, hasher)

	intermediatePublicKey.UnsafeVarTimeScalarBaseMult(&derivationSharedData)
	ephemeralPublicKey.Add(&intermediatePublicKey, spendPublicKeyPoint)

	var ephemeralPublicKeyBytes crypto.PublicKeyBytes
	copy(ephemeralPublicKeyBytes[:], ephemeralPublicKey.Bytes())

	return ephemeralPublicKeyBytes, viewTag
}

// GetDerivationNoAllocate Special version
func GetDerivationNoAllocate(viewPublicKeyPoint *edwards25519.Point, txKey *edwards25519.Scalar) crypto.PublicKeyBytes {
	var point, derivation edwards25519.Point
	point.UnsafeVarTimeScalarMult(txKey, viewPublicKeyPoint)
	derivation.MultByCofactor(&point)

	return crypto.PublicKeyBytes(derivation.Bytes())
}

// GetDerivationNoAllocateTable Special version but with table
func GetDerivationNoAllocateTable(viewPublicKeyTable *edwards25519.PrecomputedTable, txKey *edwards25519.Scalar) crypto.PublicKeyBytes {
	var point, derivation edwards25519.Point
	point.UnsafeVarTimeScalarMultPrecomputed(txKey, viewPublicKeyTable)
	derivation.MultByCofactor(&point)

	return crypto.PublicKeyBytes(derivation.Bytes())
}

// GetTxProofV2
// Deprecated
func GetTxProofV2(a Interface, txId types.Hash, txKey crypto.PrivateKey, message string) string {
	return GetOutProofV2(a, txId, txKey, message)
}

// GetTxProofV1
// Deprecated
func GetTxProofV1(a Interface, txId types.Hash, txKey crypto.PrivateKey, message string) string {
	return GetOutProofV1(a, txId, txKey, message)
}

func GetOutProofV2(a Interface, txId types.Hash, txKey crypto.PrivateKey, message string, additionalTxKeys ...crypto.PrivateKey) string {
	prefixHash := crypto.Keccak256(txId[:], []byte(message))

	sharedSecret := make([]crypto.PublicKey, 1, 1+len(additionalTxKeys))
	signature := make([]*crypto.Signature, 1, 1+len(additionalTxKeys))

	sharedSecret[0] = txKey.GetDerivation(a.ViewPublicKey())
	if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
		pub := txKey.GetDerivation(sa.SpendPublicKey())
		signature[0] = crypto.GenerateTxProofV2(prefixHash, pub, sa.ViewPublicKey(), sa.SpendPublicKey(), sharedSecret[0], txKey)
	} else {
		signature[0] = crypto.GenerateTxProofV2(prefixHash, txKey.PublicKey(), a.ViewPublicKey(), nil, sharedSecret[0], txKey)
	}

	for i, additionalTxKey := range additionalTxKeys {
		sharedSecret[i+1] = additionalTxKey.GetDerivation(a.ViewPublicKey())
		if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
			pub := additionalTxKey.GetDerivation(sa.SpendPublicKey())
			signature[i+1] = crypto.GenerateTxProofV2(prefixHash, pub, sa.ViewPublicKey(), sa.SpendPublicKey(), sharedSecret[i+1], additionalTxKey)
		} else {
			signature[i+1] = crypto.GenerateTxProofV2(prefixHash, additionalTxKey.PublicKey(), a.ViewPublicKey(), nil, sharedSecret[i+1], additionalTxKey)
		}
	}

	output := make([]string, 1, 1+len(sharedSecret)*2)
	output[0] = "OutProofV2"
	for i := range sharedSecret {
		output = append(output, string(base58.EncodeMoneroBase58(sharedSecret[i].AsSlice())))
		output = append(output, string(base58.EncodeMoneroBase58(signature[i].Bytes())))
	}
	return strings.Join(output, "")
}

func GetOutProofV1(a Interface, txId types.Hash, txKey crypto.PrivateKey, message string, additionalTxKeys ...crypto.PrivateKey) string {
	prefixHash := crypto.Keccak256(txId[:], []byte(message))

	sharedSecret := make([]crypto.PublicKey, 1, 1+len(additionalTxKeys))
	signature := make([]*crypto.Signature, 1, 1+len(additionalTxKeys))

	sharedSecret[0] = txKey.GetDerivation(a.ViewPublicKey())
	if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
		signature[0] = crypto.GenerateTxProofV1(prefixHash, sa.ViewPublicKey(), sa.SpendPublicKey(), sharedSecret[0], txKey)
	} else {
		signature[0] = crypto.GenerateTxProofV1(prefixHash, a.ViewPublicKey(), nil, sharedSecret[0], txKey)
	}

	for i, additionalTxKey := range additionalTxKeys {
		sharedSecret[i+1] = additionalTxKey.GetDerivation(a.ViewPublicKey())
		if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
			signature[i+1] = crypto.GenerateTxProofV1(prefixHash, sa.ViewPublicKey(), sa.SpendPublicKey(), sharedSecret[i+1], additionalTxKey)
		} else {
			signature[i+1] = crypto.GenerateTxProofV1(prefixHash, a.ViewPublicKey(), nil, sharedSecret[i+1], additionalTxKey)
		}
	}

	output := make([]string, 1, 1+len(sharedSecret)*2)
	output[0] = "OutProofV1"
	for i := range sharedSecret {
		output = append(output, string(base58.EncodeMoneroBase58(sharedSecret[i].AsSlice())))
		output = append(output, string(base58.EncodeMoneroBase58(signature[i].Bytes())))
	}
	return strings.Join(output, "")
}

func GetInProofV2(a Interface, txId types.Hash, viewKey crypto.PrivateKey, txPubKey crypto.PublicKey, message string, additionalTxPubKeys ...crypto.PublicKey) string {
	prefixHash := crypto.Keccak256(txId[:], []byte(message))

	sharedSecret := make([]crypto.PublicKey, 1, 1+len(additionalTxPubKeys))
	signature := make([]*crypto.Signature, 1, 1+len(additionalTxPubKeys))

	sharedSecret[0] = viewKey.GetDerivation(txPubKey)
	if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
		signature[0] = crypto.GenerateTxProofV2(prefixHash, sa.ViewPublicKey(), txPubKey, sa.SpendPublicKey(), sharedSecret[0], viewKey)
	} else {
		signature[0] = crypto.GenerateTxProofV2(prefixHash, a.ViewPublicKey(), txPubKey, nil, sharedSecret[0], viewKey)
	}

	for i, additionalTxPubKey := range additionalTxPubKeys {
		sharedSecret[i+1] = viewKey.GetDerivation(additionalTxPubKey)
		if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
			signature[i+1] = crypto.GenerateTxProofV2(prefixHash, sa.ViewPublicKey(), additionalTxPubKey, sa.SpendPublicKey(), sharedSecret[i+1], viewKey)
		} else {
			signature[i+1] = crypto.GenerateTxProofV2(prefixHash, a.ViewPublicKey(), additionalTxPubKey, nil, sharedSecret[i+1], viewKey)
		}
	}

	output := make([]string, 1, 1+len(sharedSecret)*2)
	output[0] = "InProofV2"
	for i := range sharedSecret {
		output = append(output, string(base58.EncodeMoneroBase58(sharedSecret[i].AsSlice())))
		output = append(output, string(base58.EncodeMoneroBase58(signature[i].Bytes())))
	}
	return strings.Join(output, "")
}

func GetInProofV1(a Interface, txId types.Hash, viewKey crypto.PrivateKey, txPubKey crypto.PublicKey, message string, additionalTxPubKeys ...crypto.PublicKey) string {
	prefixHash := crypto.Keccak256(txId[:], []byte(message))

	sharedSecret := make([]crypto.PublicKey, 1, 1+len(additionalTxPubKeys))
	signature := make([]*crypto.Signature, 1, 1+len(additionalTxPubKeys))

	sharedSecret[0] = viewKey.GetDerivation(txPubKey)
	if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
		signature[0] = crypto.GenerateTxProofV1(prefixHash, txPubKey, sa.SpendPublicKey(), sharedSecret[0], viewKey)
	} else {
		signature[0] = crypto.GenerateTxProofV1(prefixHash, txPubKey, nil, sharedSecret[0], viewKey)
	}

	for i, additionalTxPubKey := range additionalTxPubKeys {
		sharedSecret[i+1] = viewKey.GetDerivation(additionalTxPubKey)
		if sa, ok := a.(InterfaceSubaddress); ok && sa.IsSubaddress() {
			signature[i+1] = crypto.GenerateTxProofV1(prefixHash, txPubKey, sa.SpendPublicKey(), sharedSecret[i+1], viewKey)
		} else {
			signature[i+1] = crypto.GenerateTxProofV1(prefixHash, txPubKey, nil, sharedSecret[i+1], viewKey)
		}
	}

	output := make([]string, 1, 1+len(sharedSecret)*2)
	output[0] = "InProofV1"
	for i := range sharedSecret {
		output = append(output, string(base58.EncodeMoneroBase58(sharedSecret[i].AsSlice())))
		output = append(output, string(base58.EncodeMoneroBase58(signature[i].Bytes())))
	}
	return strings.Join(output, "")
}

type SignatureVerifyResult int

const (
	ResultFailZeroSpend SignatureVerifyResult = -2
	ResultFailZeroView  SignatureVerifyResult = -1
)
const (
	ResultFail = SignatureVerifyResult(iota)
	ResultSuccessSpend
	ResultSuccessView
)

func GetMessageHash(a Interface, message []byte, mode uint8) types.Hash {
	return crypto.Keccak256(
		[]byte("MoneroMessageSignature\x00"),
		a.SpendPublicKey().AsSlice(),
		a.ViewPublicKey().AsSlice(),
		[]byte{mode},
		binary.AppendUvarint(nil, uint64(len(message))),
		message,
	)
}

func VerifyMessage(a Interface, message []byte, signature string) SignatureVerifyResult {
	var hash types.Hash

	if strings.HasPrefix(signature, "SigV1") {
		hash = crypto.Keccak256(message)
	} else if strings.HasPrefix(signature, "SigV2") {
		hash = GetMessageHash(a, message, 0)
	} else {
		return ResultFail
	}
	raw := base58.DecodeMoneroBase58([]byte(signature[5:]))

	sig := crypto.NewSignatureFromBytes(raw)

	if sig == nil {
		return ResultFail
	}

	if crypto.VerifyMessageSignature(hash, a.SpendPublicKey(), sig) {
		return ResultSuccessSpend
	}

	// Special mode: view wallets in Monero GUI could generate signatures with spend public key proper, with message hash of spend wallet mode, but zero spend private key
	if crypto.VerifyMessageSignatureSplit(hash, a.SpendPublicKey(), ZeroPrivateKeyAddress.SpendPublicKey(), sig) {
		return ResultFailZeroSpend
	}

	if strings.HasPrefix(signature, "SigV2") {
		hash = GetMessageHash(a, message, 1)
	}

	if crypto.VerifyMessageSignature(hash, a.ViewPublicKey(), sig) {
		return ResultSuccessView
	}

	return ResultFail
}

// VerifyMessageFallbackToZero Check for Monero GUI behavior to generate wrong signatures on view-only wallets
func VerifyMessageFallbackToZero(a Interface, message []byte, signature string) SignatureVerifyResult {
	var hash types.Hash

	if strings.HasPrefix(signature, "SigV1") {
		hash = crypto.Keccak256(message)
	} else if strings.HasPrefix(signature, "SigV2") {
		hash = GetMessageHash(a, message, 0)
	} else {
		return ResultFail
	}
	raw := base58.DecodeMoneroBase58([]byte(signature[5:]))

	sig := crypto.NewSignatureFromBytes(raw)

	if sig == nil {
		return ResultFail
	}

	if crypto.VerifyMessageSignature(hash, a.SpendPublicKey(), sig) {
		return ResultSuccessSpend
	}

	// Special mode: view wallets in Monero GUI could generate signatures with spend public key proper, with message hash of spend wallet mode, but zero spend private key
	if crypto.VerifyMessageSignatureSplit(hash, a.SpendPublicKey(), ZeroPrivateKeyAddress.SpendPublicKey(), sig) {
		return ResultFailZeroSpend
	}

	if strings.HasPrefix(signature, "SigV2") {
		hash = GetMessageHash(a, message, 1)
	}

	if crypto.VerifyMessageSignature(hash, a.ViewPublicKey(), sig) {
		return ResultSuccessView
	}

	// Special mode
	if crypto.VerifyMessageSignatureSplit(hash, a.ViewPublicKey(), ZeroPrivateKeyAddress.ViewPublicKey(), sig) {
		return ResultFailZeroView
	}

	return ResultFail
}
