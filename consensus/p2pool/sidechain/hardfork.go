package sidechain

import (
	"fmt"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
)

// List of historical hardforks that p2pool networks went through
// These are not kept in later p2pool releases.
// If you ever find yourself back in time with a new p2pool release, it will start at its latest supported
var p2poolMainNetHardForks = []monero.HardFork{
	{uint8(ShareVersion_V1), 0, 0, 0},
	// p2pool hardforks at 2023-03-18 21:00 UTC
	{uint8(ShareVersion_V2), 0, 0, 1679173200},
	// Oct 12 2024 20:00:00 GMT+0000
	{uint8(ShareVersion_V3), 0, 0, 1728763200},
}

var p2poolTestNetHardForks = []monero.HardFork{
	{uint8(ShareVersion_V1), 0, 0, 0},
	// p2pool hardforks at 2023-01-23 21:00 UTC
	{uint8(ShareVersion_V2), 0, 0, 1674507600},
	//alternate hardfork at 2023-03-07 20:00 UTC 1678219200
	//{uint8(ShareVersion_V2), 0, 0, 1678219200},

	// first fork mm_test Jun 09 2024 20:00:00 GMT+0000
	// {uint8(ShareVersion_V3), 0, 0, 1717963200},
	// second fork mm_test2 Jun 18 2024 20:00:00 GMT+0000
	{uint8(ShareVersion_V3), 0, 0, 1718740800},
}

var p2poolStageNetHardForks = []monero.HardFork{
	//always latest version
	{p2poolMainNetHardForks[len(p2poolMainNetHardForks)-1].Version, 0, 0, 0},
}

type ShareVersion uint8

func (v ShareVersion) String() string {
	switch v {
	case ShareVersion_None:
		return "none"
	default:
		return fmt.Sprintf("v%d", v)
	}
}

const (
	ShareVersion_None = ShareVersion(iota)

	// ShareVersion_V1 Initial version.
	ShareVersion_V1

	// ShareVersion_V2 Introduced in P2Pool v3.0+
	ShareVersion_V2

	// ShareVersion_V3 Introduced in P2Pool v4.0+ with merge mining support
	ShareVersion_V3
)

// P2PoolShareVersion
// Different miners can have different timestamps,
// so a temporary mix of old and new blocks is allowed
func P2PoolShareVersion(consensus *Consensus, timestamp uint64) ShareVersion {
	hardForks := consensus.HardForks

	if len(hardForks) == 0 {
		return ShareVersion_None
	}

	result := hardForks[0].Version

	for _, f := range hardForks[1:] {
		if timestamp < f.Time {
			break
		}
		result = f.Version
	}
	return ShareVersion(result)
}
