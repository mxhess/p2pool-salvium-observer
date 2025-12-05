package types

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"

	fasthex "github.com/tmthrgd/go-hex"
)

func IsPeerVersionInformation(addr netip.AddrPort) bool {
	if addr.Port() != 0xFFFF {
		return false
	}
	rawIp := addr.Addr().As16()
	return bytes.Compare(rawIp[12:], []byte{0xFF, 0xFF, 0xFF, 0xFF}) == 0
}

// ProtocolFeature List of features to check to not depend on hardcoded protocol versions.
// Use PeerVersionInformation.SupportsFeature() to query these.
type ProtocolFeature int

const (
	// FeaturePeerInformationReceive Backwards compatible, can be sent to all clients
	FeaturePeerInformationReceive = ProtocolFeature(iota)
	FeaturePeerInformationExchange
	FeaturePrunedBroadcast
	FeatureCompactBroadcast
	FeatureBlockNotify
	FeatureAuxJobDonation
	FeatureMoneroBlockBroadcast
)

type PeerVersionInformation struct {
	Protocol        ProtocolVersion
	SoftwareVersion SoftwareVersion
	SoftwareId      SoftwareId
}

func (i *PeerVersionInformation) SupportsFeature(feature ProtocolFeature) bool {
	switch feature {
	case FeaturePeerInformationReceive:
		return i.Protocol == ProtocolVersion_0_0 || i.Protocol >= ProtocolVersion_1_0
	case FeaturePeerInformationExchange:
		return i.Protocol >= ProtocolVersion_1_0
	case FeaturePrunedBroadcast:
		return i.Protocol == ProtocolVersion_0_0 || i.Protocol >= ProtocolVersion_1_0
	case FeatureCompactBroadcast:
		return i.Protocol >= ProtocolVersion_1_1
	case FeatureBlockNotify:
		return i.Protocol >= ProtocolVersion_1_2
	case FeatureAuxJobDonation:
		return i.Protocol >= ProtocolVersion_1_3
	case FeatureMoneroBlockBroadcast:
		return i.Protocol >= ProtocolVersion_1_4
	default:
		return false
	}
}

func (i *PeerVersionInformation) String() string {
	// Empty information
	if i.Protocol == 0 && i.SoftwareVersion == 0 && i.SoftwareId == 0 {
		return "Unknown"
	}

	return fmt.Sprintf("%s %s (protocol %s)", i.SoftwareId.String(), i.SoftwareVersion.SoftwareAwareString(i.SoftwareId), i.Protocol.String())
}

func (i *PeerVersionInformation) ToAddrPort() netip.AddrPort {
	var addr [16]byte

	binary.LittleEndian.PutUint32(addr[:], uint32(i.Protocol))
	binary.LittleEndian.PutUint32(addr[4:], uint32(i.SoftwareVersion))
	binary.LittleEndian.PutUint32(addr[8:], uint32(i.SoftwareId))
	binary.LittleEndian.PutUint32(addr[12:], 0xFFFFFFFF)

	return netip.AddrPortFrom(netip.AddrFrom16(addr), 0xFFFF)
}

type ProtocolVersion SemanticVersion

func (v ProtocolVersion) Major() uint16 {
	return SemanticVersion(v).Major()
}
func (v ProtocolVersion) Minor() uint16 {
	return SemanticVersion(v).MinorPatch()
}

func (v ProtocolVersion) String() string {
	return SemanticVersion(v).StringMinorPatch()
}

const (
	ProtocolVersion_0_0 ProtocolVersion = (0 << 16) | 0
	ProtocolVersion_1_0 ProtocolVersion = (1 << 16) | 0
	ProtocolVersion_1_1 ProtocolVersion = (1 << 16) | 1
	ProtocolVersion_1_2 ProtocolVersion = (1 << 16) | 2
	ProtocolVersion_1_3 ProtocolVersion = (1 << 16) | 3
	ProtocolVersion_1_4 ProtocolVersion = (1 << 16) | 4
)

type SoftwareVersion SemanticVersion

func (v SoftwareVersion) Major() uint16 {
	return SemanticVersion(v).Major()
}
func (v SoftwareVersion) MinorPatch() uint16 {
	return SemanticVersion(v).MinorPatch()
}
func (v SoftwareVersion) Minor() uint16 {
	return SemanticVersion(v).Minor()
}
func (v SoftwareVersion) Patch() uint16 {
	return SemanticVersion(v).Patch()
}

func (v SoftwareVersion) String() string {
	return SemanticVersion(v).String()
}

func (v SoftwareVersion) SoftwareAwareString(id SoftwareId) string {
	switch id {
	case SoftwareIdP2Pool, SoftwareIdGoObserver:
		if v.Major() < 3 || (v.Major() == 3 && v.MinorPatch() <= 10) {
			return SemanticVersion(v).StringMinorPatch()
		}
	}
	return SemanticVersion(v).String()
}

const SupportedProtocolVersion = ProtocolVersion_1_4

const CurrentSoftwareVersionMajor = 4 & 0xFFFF
const CurrentSoftwareVersionMinor = 9 & 0xFF
const CurrentSoftwareVersionPatch = 1 & 0xFF

const CurrentSoftwareVersion SoftwareVersion = (CurrentSoftwareVersionMajor << 16) | (CurrentSoftwareVersionMinor << 8) | CurrentSoftwareVersionPatch
const CurrentSoftwareId = SoftwareIdGoObserver

type SoftwareId uint32

func (c SoftwareId) String() string {
	switch c {
	case SoftwareIdP2Pool:
		return "P2Pool"
	case SoftwareIdGoObserver:
		return "GoObserver"
	default:
		var buf = [17]byte{'U', 'n', 'k', 'n', 'o', 'w', 'n', '(', 0, 0, 0, 0, 0, 0, 0, 0, ')'}
		var intBuf [4]byte
		binary.LittleEndian.PutUint32(intBuf[:], uint32(c))
		fasthex.Encode(buf[8:], intBuf[:])
		return string(buf[:])
	}
}

const (
	SoftwareIdP2Pool     SoftwareId = 0x00000000
	SoftwareIdGoObserver SoftwareId = 0x624F6F47 //GoOb, little endian
)
