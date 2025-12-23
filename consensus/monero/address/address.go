package address

import (
	"encoding/binary"
	"errors"
	"bytes"
	
	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	base58 "git.gammaspectra.live/P2Pool/monero-base58"
)

type Address struct {
	SpendPub    crypto.PublicKeyBytes
	ViewPub     crypto.PublicKeyBytes
	TypeNetwork uint64 // Changed from uint8 to uint64 for varint support
	IsCarrot    bool   // Track if this is a Carrot address
	hasChecksum bool
	checksum    Checksum
}

const ChecksumLength = 4

type Checksum [ChecksumLength]byte

func (a *Address) Compare(b Interface) int {
	//compare spend key
	resultSpendKey := crypto.CompareConsensusPublicKeyBytes(&a.SpendPub, b.SpendPublicKey())
	if resultSpendKey != 0 {
		return resultSpendKey
	}
	// compare view key
	return crypto.CompareConsensusPublicKeyBytes(&a.ViewPub, b.ViewPublicKey())
}

func (a *Address) PublicKeys() (spend, view crypto.PublicKey) {
	return &a.SpendPub, &a.ViewPub
}

func (a *Address) SpendPublicKey() *crypto.PublicKeyBytes {
	return &a.SpendPub
}

func (a *Address) ViewPublicKey() *crypto.PublicKeyBytes {
	return &a.ViewPub
}

func (a *Address) ToAddress(network uint8, err ...error) *Address {
	// For compatibility with old code expecting uint8 network
	baseNet := a.BaseNetwork()
	if baseNet != network || (len(err) > 0 && err[0] != nil) {
		return nil
	}
	return a
}

func (a *Address) BaseNetwork() uint8 {
	// Extract base network from the varint tag
	// Legacy SaLv addresses
	if a.TypeNetwork == 0x3ef318 || a.TypeNetwork == 0x55ef318 || a.TypeNetwork == 0xf5ef318 {
		return monero.MainNetwork
	}
	if a.TypeNetwork == 0x15beb318 || a.TypeNetwork == 0xd055eb318 || a.TypeNetwork == 0xa59eb318 {
		return monero.TestNetwork
	}
	if a.TypeNetwork == 0x149eb318 || a.TypeNetwork == 0xf343eb318 || a.TypeNetwork == 0x2d47eb318 {
		return monero.StageNetwork
	}
	
	// Carrot SC1 addresses
	if a.TypeNetwork == 0x180c96 || a.TypeNetwork == 0x2ccc96 || a.TypeNetwork == 0x314c96 {
		return monero.MainNetwork
	}
	if a.TypeNetwork == 0x254c96 || a.TypeNetwork == 0x1ac50c96 || a.TypeNetwork == 0x3c54c96 {
		return monero.TestNetwork
	}
	if a.TypeNetwork == 0x24cc96 || a.TypeNetwork == 0x1a848c96 || a.TypeNetwork == 0x384cc96 {
		return monero.StageNetwork
	}
	
	return 0
}

func (a *Address) IsSubaddress() bool {
	// Legacy subaddress prefixes
	if a.TypeNetwork == 0xf5ef318 || a.TypeNetwork == 0xa59eb318 || a.TypeNetwork == 0x2d47eb318 {
		return true
	}
	// Carrot subaddress prefixes
	if a.TypeNetwork == 0x314c96 || a.TypeNetwork == 0x3c54c96 || a.TypeNetwork == 0x384cc96 {
		return true
	}
	return false
}

func (a *Address) ToPackedAddress() PackedAddress {
	return NewPackedAddressFromBytes(a.SpendPub, a.ViewPub)
}

func FromBase58(address string) *Address {
	// Decode base58 - variable length for Salvium
	preAllocatedBuf := make([]byte, 0, 128) // Larger buffer for variable-length addresses
	raw := base58.DecodeMoneroBase58PreAllocated(preAllocatedBuf, []byte(address))
	
	// Must have at least: varint(1+) + spend(32) + view(32) + checksum(4) = 69+ bytes
	if len(raw) < 69 {
		return nil
	}
	
	// Read varint tag
	tag, varintLen := utils.CanonicalUvarint(raw)
	if varintLen <= 0 {
		return nil
	}
	
	// Remaining data should be exactly 32 + 32 + 4 = 68 bytes
	dataStart := varintLen
	if len(raw)-dataStart != 68 {
		return nil
	}
	
	// Validate tag is a known address type
	isCarrot := false
	switch tag {
	// Legacy SaLv addresses - mainnet
	case 0x3ef318, 0x55ef318, 0xf5ef318:
		break
	// Legacy SaLv addresses - testnet
	case 0x15beb318, 0xd055eb318, 0xa59eb318:
		break
	// Legacy SaLv addresses - stagenet
	case 0x149eb318, 0xf343eb318, 0x2d47eb318:
		break
	// Carrot SC1 addresses - mainnet
	case 0x180c96, 0x2ccc96, 0x314c96:
		isCarrot = true
	// Carrot SC1 addresses - testnet
	case 0x254c96, 0x1ac50c96, 0x3c54c96:
		isCarrot = true
	// Carrot SC1 addresses - stagenet
	case 0x24cc96, 0x1a848c96, 0x384cc96:
		isCarrot = true
	default:
		return nil
	}
	
	// Verify checksum
	checksumData := raw[:len(raw)-4]
	checksum := checksumHash(checksumData)
	
	a := &Address{
		TypeNetwork: tag,
		IsCarrot:    isCarrot,
		checksum:    checksum,
		hasChecksum: true,
	}
	
	if bytes.Compare(a.checksum[:], raw[len(raw)-4:]) != 0 {
		return nil
	}
	
	// Extract spend and view keys
	copy(a.SpendPub[:], raw[dataStart:dataStart+32])
	copy(a.ViewPub[:], raw[dataStart+32:dataStart+64])
	
	return a
}

func FromBase58NoChecksumCheck(address []byte) *Address {
	// Decode base58
	preAllocatedBuf := make([]byte, 0, 128)
	raw := base58.DecodeMoneroBase58PreAllocated(preAllocatedBuf, address)
	
	if len(raw) < 69 {
		return nil
	}
	
	// Read varint tag
	tag, varintLen := utils.CanonicalUvarint(raw)
	if varintLen <= 0 {
		return nil
	}
	
	dataStart := varintLen
	if len(raw)-dataStart < 64 {
		return nil
	}
	
	// Validate tag
	isCarrot := false
	switch tag {
	case 0x3ef318, 0x55ef318, 0xf5ef318,
		0x15beb318, 0xd055eb318, 0xa59eb318,
		0x149eb318, 0xf343eb318, 0x2d47eb318:
		break
	case 0x180c96, 0x2ccc96, 0x314c96,
		0x254c96, 0x1ac50c96, 0x3c54c96,
		0x24cc96, 0x1a848c96, 0x384cc96:
		isCarrot = true
	default:
		return nil
	}
	
	a := &Address{
		TypeNetwork: tag,
		IsCarrot:    isCarrot,
		hasChecksum: false,
	}
	
	copy(a.SpendPub[:], raw[dataStart:dataStart+32])
	copy(a.ViewPub[:], raw[dataStart+32:dataStart+64])
	
	return a
}

func checksumHash(data []byte) (sum [ChecksumLength]byte) {
	h := crypto.PooledKeccak256(data)
	copy(sum[:], h[:ChecksumLength])
	return
}

func FromRawAddress(typeNetwork uint8, spend, view crypto.PublicKey) *Address {
	// Convert legacy uint8 network to uint64 tag using Carrot format
	var tag uint64
	switch typeNetwork {
	case monero.MainNetwork:
		tag = monero.CarrotMainAddress // Use Carrot "SC1" format (0x180c96)
	case monero.TestNetwork:
		tag = monero.CarrotTestAddress
	case monero.StageNetwork:
		tag = monero.CarrotStageAddress
	default:
		return nil
	}

	return &Address{
		TypeNetwork: tag,
		SpendPub:    spend.AsBytes(),
		ViewPub:     view.AsBytes(),
		IsCarrot:    true, // Mark as Carrot address
	}
}

func (a *Address) verifyChecksum() {
	if !a.hasChecksum {
		// Encode the address with varint prefix
		var prefixBuf [binary.MaxVarintLen64]byte
		prefixLen := binary.PutUvarint(prefixBuf[:], a.TypeNetwork)
		
		data := make([]byte, prefixLen+64)
		copy(data, prefixBuf[:prefixLen])
		copy(data[prefixLen:], a.SpendPub[:])
		copy(data[prefixLen+32:], a.ViewPub[:])
		
		a.checksum = checksumHash(data)
		a.hasChecksum = true
	}
}

func (a *Address) Valid() bool {
	return a.ViewPublicKey().AsPoint() != nil && a.SpendPublicKey().AsPoint() != nil
}

func (a *Address) ToBase58() []byte {
	a.verifyChecksum()
	
	// Encode varint prefix
	var prefixBuf [binary.MaxVarintLen64]byte
	prefixLen := binary.PutUvarint(prefixBuf[:], a.TypeNetwork)
	
	// Build the full address data: prefix + spend + view + checksum
	data := make([]byte, prefixLen+64+4)
	copy(data, prefixBuf[:prefixLen])
	copy(data[prefixLen:], a.SpendPub[:])
	copy(data[prefixLen+32:], a.ViewPub[:])
	copy(data[prefixLen+64:], a.checksum[:])
	
	// Encode to base58
	buf := make([]byte, 0, 150) // Carrot addresses can be up to 143 chars
	return base58.EncodeMoneroBase58PreAllocated(buf, data)
}

func (a *Address) MarshalJSON() ([]byte, error) {
	b58 := a.ToBase58()
	result := make([]byte, len(b58)+2)
	result[0] = '"'
	copy(result[1:], b58)
	result[len(result)-1] = '"'
	return result, nil
}

func (a *Address) UnmarshalJSON(b []byte) error {
	if len(b) < 2 {
		return errors.New("unsupported length")
	}
	if addr := FromBase58NoChecksumCheck(b[1 : len(b)-1]); addr != nil {
		*a = *addr
		a.verifyChecksum()
		return nil
	}
	return errors.New("invalid address")
}
