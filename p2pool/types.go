// Package p2pool provides minimal types for parsing p2pool-salvium block data.
// Based directly on ~/p2pool-salvium/src/ C++ implementation.
package p2pool

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"

	"golang.org/x/crypto/sha3"
)

// Hash is a 32-byte hash (matches C++ hash type)
type Hash [32]byte

// ZeroHash is an empty hash
var ZeroHash Hash

// String returns hex representation
func (h Hash) String() string {
	return hex.EncodeToString(h[:])
}

// IsZero returns true if hash is all zeros
func (h Hash) IsZero() bool {
	return h == ZeroHash
}

// Difficulty is a 128-bit difficulty value (matches C++ difficulty_type)
type Difficulty struct {
	Lo uint64
	Hi uint64
}

// Cmp compares two difficulties. Returns -1, 0, or 1.
func (d Difficulty) Cmp(other Difficulty) int {
	if d.Hi < other.Hi {
		return -1
	}
	if d.Hi > other.Hi {
		return 1
	}
	if d.Lo < other.Lo {
		return -1
	}
	if d.Lo > other.Lo {
		return 1
	}
	return 0
}

// Uint64 returns Lo part (for small difficulties)
func (d Difficulty) Uint64() uint64 {
	return d.Lo
}

// DifficultyFromUint64 creates a Difficulty from a uint64
func DifficultyFromUint64(v uint64) Difficulty {
	return Difficulty{Lo: v, Hi: 0}
}

// Address represents a Salvium address (two 32-byte public keys)
// Based on C++ Wallet class with spend_public_key and view_public_key
type Address struct {
	SpendPublicKey Hash
	ViewPublicKey  Hash
}

// ToBase58 returns the base58-encoded Salvium address
// Salvium mainnet prefix is 0x180c96 (1576086 decimal) = "SC1" prefix
func (a Address) ToBase58() string {
	// Build address data: prefix + spend_key + view_key
	// Salvium mainnet prefix: 0x180c96 (1576086) encoded as varint = 0x96 0x99 0x60
	prefix := []byte{0x96, 0x99, 0x60}

	data := make([]byte, 0, len(prefix)+64+4)
	data = append(data, prefix...)
	data = append(data, a.SpendPublicKey[:]...)
	data = append(data, a.ViewPublicKey[:]...)

	// Calculate checksum (first 4 bytes of keccak256)
	hash := keccak256(data)
	data = append(data, hash[:4]...)

	// Encode with Monero's base58 block encoding
	return encodeMoneroBase58(data)
}

// keccak256 computes the Keccak-256 hash
func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// Monero base58 alphabet
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// encodeMoneroBase58 encodes data using Monero's block-based base58 encoding
// Monero splits data into 8-byte blocks and encodes each as 11 base58 chars
// Last block may be smaller and encodes to fewer chars
func encodeMoneroBase58(data []byte) string {
	// Block sizes for Monero base58
	fullBlockSize := 8
	fullEncodedBlockSize := 11

	// Calculate output size
	numFullBlocks := len(data) / fullBlockSize
	lastBlockSize := len(data) % fullBlockSize

	// Encoded sizes for partial blocks
	encodedBlockSizes := []int{0, 2, 3, 5, 6, 7, 9, 10, 11}

	result := make([]byte, 0, numFullBlocks*fullEncodedBlockSize+encodedBlockSizes[lastBlockSize])

	// Encode full blocks
	for i := 0; i < numFullBlocks; i++ {
		block := data[i*fullBlockSize : (i+1)*fullBlockSize]
		encoded := encodeBlock(block, fullEncodedBlockSize)
		result = append(result, encoded...)
	}

	// Encode last partial block
	if lastBlockSize > 0 {
		block := data[numFullBlocks*fullBlockSize:]
		encoded := encodeBlock(block, encodedBlockSizes[lastBlockSize])
		result = append(result, encoded...)
	}

	return string(result)
}

// encodeBlock encodes a block of bytes to base58
func encodeBlock(block []byte, encodedSize int) []byte {
	// Convert block to big integer
	var num uint64
	for _, b := range block {
		num = num*256 + uint64(b)
	}

	// Convert to base58
	result := make([]byte, encodedSize)
	for i := encodedSize - 1; i >= 0; i-- {
		result[i] = base58Alphabet[num%58]
		num /= 58
	}

	return result
}

// ReadVarint reads a varint from data at offset, returns value and new offset
// Matches C++ readVarint() in pool_block_parser.inl
func ReadVarint(data []byte, offset int) (uint64, int, error) {
	if offset >= len(data) {
		return 0, offset, fmt.Errorf("varint: offset %d beyond data length %d", offset, len(data))
	}

	var result uint64
	var shift uint
	for {
		if offset >= len(data) {
			return 0, offset, fmt.Errorf("varint: unexpected end of data")
		}
		b := data[offset]
		offset++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift > 63 {
			return 0, offset, fmt.Errorf("varint: overflow")
		}
	}
	return result, offset, nil
}

// ReadHash reads a 32-byte hash from data at offset
func ReadHash(data []byte, offset int) (Hash, int, error) {
	var h Hash
	if offset+32 > len(data) {
		return h, offset, fmt.Errorf("hash: need 32 bytes at offset %d, have %d", offset, len(data)-offset)
	}
	copy(h[:], data[offset:offset+32])
	return h, offset + 32, nil
}

// ReadUint32LE reads a little-endian uint32
func ReadUint32LE(data []byte, offset int) (uint32, int, error) {
	if offset+4 > len(data) {
		return 0, offset, fmt.Errorf("uint32: need 4 bytes at offset %d", offset)
	}
	v := binary.LittleEndian.Uint32(data[offset:])
	return v, offset + 4, nil
}

// ReadUint64LE reads a little-endian uint64
func ReadUint64LE(data []byte, offset int) (uint64, int, error) {
	if offset+8 > len(data) {
		return 0, offset, fmt.Errorf("uint64: need 8 bytes at offset %d", offset)
	}
	v := binary.LittleEndian.Uint64(data[offset:])
	return v, offset + 8, nil
}

// ReadByte reads a single byte
func ReadByte(data []byte, offset int) (byte, int, error) {
	if offset >= len(data) {
		return 0, offset, fmt.Errorf("byte: need 1 byte at offset %d", offset)
	}
	return data[offset], offset + 1, nil
}

// SkipBytes skips n bytes
func SkipBytes(data []byte, offset int, n int) (int, error) {
	if offset+n > len(data) {
		return offset, fmt.Errorf("skip: need %d bytes at offset %d, have %d", n, offset, len(data)-offset)
	}
	return offset + n, nil
}

// Add returns the sum of two difficulties
func (d Difficulty) Add(other Difficulty) Difficulty {
	lo := d.Lo + other.Lo
	hi := d.Hi + other.Hi
	if lo < d.Lo { // overflow
		hi++
	}
	return Difficulty{Lo: lo, Hi: hi}
}

// Sub returns d - other (assumes d >= other)
func (d Difficulty) Sub(other Difficulty) Difficulty {
	lo := d.Lo - other.Lo
	hi := d.Hi - other.Hi
	if d.Lo < other.Lo { // underflow
		hi--
	}
	return Difficulty{Lo: lo, Hi: hi}
}

// Mul64 multiplies a difficulty by a uint64
func (d Difficulty) Mul64(v uint64) Difficulty {
	// Use 128-bit multiplication
	// result = (Hi * 2^64 + Lo) * v
	// = Hi*v*2^64 + Lo*v
	loLo := (d.Lo & 0xFFFFFFFF) * (v & 0xFFFFFFFF)
	loHi := (d.Lo >> 32) * (v & 0xFFFFFFFF)
	hiLo := (d.Lo & 0xFFFFFFFF) * (v >> 32)
	hiHi := (d.Lo >> 32) * (v >> 32)

	mid := (loLo >> 32) + (loHi & 0xFFFFFFFF) + (hiLo & 0xFFFFFFFF)
	hi := (loHi >> 32) + (hiLo >> 32) + hiHi + (mid >> 32) + d.Hi*v

	return Difficulty{
		Lo: (loLo & 0xFFFFFFFF) | (mid << 32),
		Hi: hi,
	}
}

// Div64 divides a difficulty by a uint64
// Uses math/bits.Div64 for proper 128-bit / 64-bit division without overflow
func (d Difficulty) Div64(v uint64) Difficulty {
	if v == 0 {
		return Difficulty{Lo: 0, Hi: 0}
	}

	// If Hi >= v, we need to handle the high part first
	var resultHi, resultLo uint64
	var rem uint64

	if d.Hi >= v {
		// High part of result
		resultHi = d.Hi / v
		rem = d.Hi % v
	} else {
		resultHi = 0
		rem = d.Hi
	}

	// Use bits.Div64 for proper 128-bit / 64-bit division
	// bits.Div64(hi, lo, y) computes (hi*2^64 + lo) / y
	// Requires: hi < y to avoid overflow
	resultLo, _ = bits.Div64(rem, d.Lo, v)

	return Difficulty{Lo: resultLo, Hi: resultHi}
}

// Div divides a difficulty by another difficulty (128-bit / 128-bit)
// Returns the quotient. For PPLNS calculations, result typically fits in 64 bits.
func (d Difficulty) Div(divisor Difficulty) Difficulty {
	if divisor.IsZero() {
		return Difficulty{Lo: 0, Hi: 0}
	}

	// If divisor fits in 64 bits, use Div64 for efficiency
	if divisor.Hi == 0 {
		return d.Div64(divisor.Lo)
	}

	// Full 128-bit division using binary long division
	// For PPLNS: dividend ≈ w_cumulative * reward (up to ~90 bits)
	// divisor = total_weight (up to ~70 bits)
	// Result = payout (up to ~40 bits, fits easily in 64 bits)

	var quotient Difficulty
	var remainder Difficulty

	// Process each bit from high to low (128 bits total)
	for i := 127; i >= 0; i-- {
		// Left shift remainder by 1
		remainder.Hi = (remainder.Hi << 1) | (remainder.Lo >> 63)
		remainder.Lo = remainder.Lo << 1

		// Bring down next bit of dividend
		var bit uint64
		if i >= 64 {
			bit = (d.Hi >> uint(i-64)) & 1
		} else {
			bit = (d.Lo >> uint(i)) & 1
		}
		remainder.Lo |= bit

		// If remainder >= divisor, subtract and set quotient bit
		if remainder.Cmp(divisor) >= 0 {
			remainder = remainder.Sub(divisor)
			if i >= 64 {
				quotient.Hi |= 1 << uint(i-64)
			} else {
				quotient.Lo |= 1 << uint(i)
			}
		}
	}

	return quotient
}

// IsZero returns true if difficulty is zero
func (d Difficulty) IsZero() bool {
	return d.Lo == 0 && d.Hi == 0
}

// String returns a string representation of the difficulty
func (d Difficulty) String() string {
	if d.Hi == 0 {
		return fmt.Sprintf("%d", d.Lo)
	}
	return fmt.Sprintf("%d:%d", d.Hi, d.Lo)
}

// TruncateAddress returns a privacy-safe truncated address format: <8 chars>...<8 chars>
// Full addresses should NEVER be displayed publicly - only accepted as search input
func TruncateAddress(addr string) string {
	if len(addr) <= 19 { // 8 + 3 + 8 = 19, no point truncating
		return addr
	}
	return addr[:8] + "..." + addr[len(addr)-8:]
}

// ToTruncated returns the truncated form of this address for public display
func (a Address) ToTruncated() string {
	return TruncateAddress(a.ToBase58())
}
