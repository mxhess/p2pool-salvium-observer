package monero

const (
	HardForkViewTagsVersion  = 3  // Salvium: view tags introduced in v3
	HardForkSupportedVersion = 10 // Salvium: currently at Carrot v1 (version 10)
)

type HardFork struct {
	// Version Numeric epoch of the version
	Version uint8 `json:"version"`
	// Time Block height at which the hardfork occurs (Monero)
	Height uint64 `json:"height"`
	// Threshold Not used currently
	Threshold uint8 `json:"threshold"`
	// Time Unix timestamp at which the hardfork occurs (p2pool), or when it happened (Monero)
	Time uint64 `json:"time"`
}

// List of Salvium hardforks
// Taken from https://github.com/salvium-io/salvium/blob/master/src/hardforks/hardforks.cpp

var mainNetHardForks = []HardFork{
	// version 1 from the start of the blockchain
	{1, 1, 0, 1341378000},

	// version 2 starts from block 89800, which is on or around the 4th of November, 2024. Fork time finalised on 2024-10-21.
	{2, 89800, 0, 1729518000},

	// version 3 starts from block 121100, which is on or around the 19th of December, 2024. Fork time finalised on 2024-12-18.
	{3, 121100, 0, 1734516900},

	// version 4 starts from block 121100, which is on or around the 20th of December, 2024. Fork time finalised on 2024-12-19.
	{4, 121800, 0, 1734607000},

	// version 5 starts from block 136100, which is on or around the 9th of January, 2025. Fork time finalised on 2025-01-08.
	{5, 136100, 0, 1736265945},

	// version 6 starts from block 154750, which is on or around the 4th of February, 2025. Fork time finalised on 2025-01-31.
	{6, 154750, 0, 1738336000},

	// version 7 starts from block 161900, which is on or around the 14th of February, 2025. Fork time finalised on 2025-02-04.
	{7, 161900, 0, 1739264400},

	// version 8 starts from block 172000, which is on or around the 28th of February, 2025. Fork time finalised on 2025-02-24.
	{8, 172000, 0, 1740390000},

	// version 9 starts from block 179200, which is on or around the 10th of March, 2025. Fork time finalised on 2025-02-24.
	{9, 179200, 0, 1740393800},

	// version 10 Carrot - including treasury mint - starts from block 334750, which is on or around the 13th of October, 2025. Fork time finalised on 2025-09-29.
	{10, 334750, 0, 1759142500},
}

var testNetHardForks = []HardFork{
	// version 1 from the start of the blockchain
	{1, 1, 0, 1341378000},

	// version 2 starts from block 250
	{2, 250, 0, 1445355000},

	// version 3 starts from block 500
	{3, 500, 0, 1729518000},

	// version 4 (full proofs) starts from block 600
	{4, 600, 0, 1734607000},

	// version 5 (TX shutdown) starts from block 800
	{5, 800, 0, 1734607005},

	// version 6 (audit 1) starts from block 815
	{6, 815, 0, 1734608000},

	// version 7 (audit 1 pause, blacklist controlling payouts) starts from block 900
	{7, 900, 0, 1739264400},

	// version 8 (audit 1 resume) starts from block 950
	{8, 950, 0, 1739270000},

	// version 9 (audit 1 complete, whitelist controlling payouts) starts from block 1000
	{9, 1000, 0, 1739280000},

	// version 10 Carrot - including treasury mint - starts from block 1100
	{10, 1100, 0, 1739780005},
}

var stageNetHardForks = []HardFork{
	// version 1 from the start of the blockchain
	{1, 1, 0, 1341378000},

	// versions 2-7 in rapid succession from March 13th, 2018
	{2, 32000, 0, 1521000000},
	{3, 33000, 0, 1521120000},
	{4, 34000, 0, 1521240000},
	{5, 35000, 0, 1521360000},
	{6, 36000, 0, 1521480000},
	{7, 37000, 0, 1521600000},
	{8, 176456, 0, 1537821770},
	{9, 177176, 0, 1537821771},
	{10, 269000, 0, 1550153694},
	{11, 269720, 0, 1550225678},
	{12, 454721, 0, 1571419280},
	{13, 675405, 0, 1598180817},
	{14, 676125, 0, 1598180818},
	{15, 1151000, 0, 1656629117},
	{16, 1151720, 0, 1656629118},
}

func NetworkHardFork(network uint8) []HardFork {
	switch network {
	case MainNetwork:
		return mainNetHardForks
	case TestNetwork:
		return testNetHardForks
	case StageNetwork:
		return stageNetHardForks
	default:
		panic("invalid network type for hardfork")
	}
}

func NetworkMajorVersion(network uint8, height uint64) uint8 {
	hardForks := NetworkHardFork(network)

	if len(hardForks) == 0 {
		return 0
	}

	result := hardForks[0].Version

	for _, f := range hardForks[1:] {
		if height < f.Height {
			break
		}
		result = f.Version
	}
	return result
}
