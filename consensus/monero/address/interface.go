package address

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
)

type Interface interface {
	Compare(b Interface) int

	PublicKeys() (spend, view crypto.PublicKey)

	SpendPublicKey() *crypto.PublicKeyBytes
	ViewPublicKey() *crypto.PublicKeyBytes

	ToAddress(network uint8, err ...error) *Address
	ToPackedAddress() PackedAddress
}

type InterfaceSubaddress interface {
	Interface
	IsSubaddress() bool
}
